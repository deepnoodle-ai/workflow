// Package local runs workflows in-process: it carries out the commands
// [workflow.Apply] returns by calling Go handlers in goroutines, and feeds
// their results back as events.
//
// Use it for tests, CLIs and single-process programs. A service that must
// survive restarts without losing work should use a durable host (see the
// postgres module), though local can also resume from a saved state: pass
// Save to persist each state and Resume to continue one. Cancel the run's
// context with the cause [Cancel] returns to cancel a run.
package local

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// Handler runs one step attempt and returns its output. Return a
// *[workflow.StepError] to control the error type and whether it is
// retryable; any other error is reported as retryable type "error". A step
// is retried only if it declares retry.
//
// ctx is cancelled when the attempt is no longer wanted: the run was
// cancelled or failed, or the step timed out.
type Handler func(ctx context.Context, cmd workflow.Command) (any, error)

// Runner runs workflows in the current process.
type Runner struct {
	// Handlers maps a step kind ("agent", "action", ...) to its handler.
	Handlers map[string]Handler
	// Save, if set, is called with every state that changed, before its
	// commands are carried out. An error stops the run.
	Save func(ctx context.Context, st *workflow.State) error
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
}

// Run starts a run and blocks until it finishes or ctx is done. It returns
// the last state; inspect its Status and Error for the outcome.
//
// When ctx is done the run is paused, not cancelled: Run returns the state
// with ctx.Err(), and Resume can continue it. To cancel the run instead,
// cancel ctx with the cause [Cancel] returns.
func (r *Runner) Run(ctx context.Context, def *workflow.Definition, input map[string]any) (*workflow.State, error) {
	d := r.newDriver(def)
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{
		At: d.now(), Kind: workflow.EventStart, Input: input,
	})
	if err != nil {
		return nil, err
	}
	return d.loop(ctx, st, cmds, false)
}

// Resume continues a run from a saved state, re-dispatching the work it had
// in flight. Handlers must tolerate running an attempt again. Like Run, it
// pauses when ctx is done and cancels the run when ctx is cancelled with
// [Cancel].
func (r *Runner) Resume(ctx context.Context, def *workflow.Definition, st *workflow.State) (*workflow.State, error) {
	if st.Status.Terminal() {
		return st, nil
	}
	cmds, err := workflow.Outstanding(def, st)
	if err != nil {
		return st, err
	}
	return r.newDriver(def).loop(ctx, st, cmds, true)
}

// Cancel returns a cancellation cause that makes Run or Resume cancel the
// run rather than pause it. Cancel the run's context with it:
//
//	ctx, cancel := context.WithCancelCause(ctx)
//	go func() { st, err = runner.Run(ctx, def, input) }()
//	...
//	cancel(local.Cancel("superseded by a newer push"))
//
// The runner applies a cancel event with the reason, saves the cancelled
// state, and returns it with a nil error once every handler has returned.
// Handlers see their contexts cancelled. A run with finally steps runs
// them first, with contexts the cancel does not reach, and Run returns once
// they are done; nothing stops them early, so give slow cleanup steps a
// timeout. A run that already finished is unaffected.
func Cancel(reason string) error { return &cancelCause{reason: reason} }

type cancelCause struct{ reason string }

func (c *cancelCause) Error() string { return "local: run cancelled: " + c.reason }

// cancelReason reports whether ctx was cancelled with [Cancel].
func cancelReason(ctx context.Context) (string, bool) {
	var c *cancelCause
	if errors.As(context.Cause(ctx), &c) {
		return c.reason, true
	}
	return "", false
}

type driver struct {
	r      *Runner
	def    *workflow.Definition
	events chan workflow.Event
	wg     sync.WaitGroup

	mu      sync.Mutex
	cancels map[workflow.StepID]context.CancelFunc
	timers  map[workflow.StepID]*time.Timer
}

func (r *Runner) newDriver(def *workflow.Definition) *driver {
	return &driver{
		r: r, def: def,
		events:  make(chan workflow.Event, 64),
		cancels: map[workflow.StepID]context.CancelFunc{},
		timers:  map[workflow.StepID]*time.Timer{},
	}
}

func (d *driver) now() time.Time {
	if d.r.Now != nil {
		return d.r.Now()
	}
	return time.Now()
}

// loop carries out commands and applies the events they produce until the
// run finishes or ctx is done. saved reports whether st is already saved.
//
// When ctx is cancelled with Cancel, the run's finally steps still have to
// run: the loop applies the cancel and carries on, detached from ctx, until
// the run finishes.
func (d *driver) loop(ctx context.Context, st *workflow.State, cmds []workflow.Command, saved bool) (*workflow.State, error) {
	// detached outlives a cancel, for the finally steps.
	detached, stopDetached := context.WithCancel(context.WithoutCancel(ctx))
	ctx, stop := context.WithCancel(ctx)
	defer func() {
		stop()
		stopDetached()
		d.stopTimers()
		d.wg.Wait()
	}()
	for {
		if !saved {
			if err := d.save(ctx, st); err != nil {
				return st, err
			}
			saved = true
		}
		if ctx.Err() != nil {
			next, out, err := d.stopped(ctx, st)
			if err != nil || next.Status.Terminal() || next == st {
				return next, err
			}
			// Cancelled: carry on, detached, through the finally steps.
			ctx = detached
			st, cmds, saved = next, out, true
		}
		for _, c := range cmds {
			d.carryOut(ctx, c)
		}
		if st.Status.Terminal() {
			return st, nil
		}
		var ev workflow.Event
		select {
		case <-ctx.Done():
			continue // paused or cancelled: see above
		case ev = <-d.events:
		}
		next, out, err := workflow.Apply(d.def, st, ev)
		if err != nil {
			return st, err
		}
		if ev.Kind == workflow.EventStepCompleted || ev.Kind == workflow.EventStepFailed {
			// The attempt has settled, now or earlier, so its timeout
			// timer is obsolete.
			d.dropTimer(ev.StepID)
		}
		// Apply returns the same state, and no commands, for a
		// duplicate or late event: there is nothing to save.
		saved = next == st
		st, cmds = next, out
	}
}

func (d *driver) save(ctx context.Context, st *workflow.State) error {
	if d.r.Save == nil {
		return nil
	}
	if err := d.r.Save(ctx, st); err != nil {
		return fmt.Errorf("local: save state: %w", err)
	}
	return nil
}

// stopped handles ctx being done: it cancels the run if ctx was cancelled
// with Cancel, and otherwise leaves it running for Resume. It carries out
// the cancel's CancelStep commands and returns the rest: the dispatches of
// the run's finally steps, if it has any.
func (d *driver) stopped(ctx context.Context, st *workflow.State) (*workflow.State, []workflow.Command, error) {
	reason, ok := cancelReason(ctx)
	if !ok {
		return st, nil, ctx.Err()
	}
	next, cmds, err := workflow.Apply(d.def, st, workflow.Event{At: d.now(), Kind: workflow.EventCancel, Reason: reason})
	if err != nil || next == st {
		return st, nil, err
	}
	if err := d.save(context.WithoutCancel(ctx), next); err != nil {
		return next, nil, err
	}
	var rest []workflow.Command
	for _, c := range cmds {
		if c.Kind == workflow.CommandCancelStep {
			d.carryOut(ctx, c) // cancel handlers, drop timers
		} else {
			rest = append(rest, c)
		}
	}
	return next, rest, nil
}

func (d *driver) carryOut(ctx context.Context, c workflow.Command) {
	switch c.Kind {
	case workflow.CommandDispatchStep:
		stepCtx, cancel := context.WithCancel(ctx)
		d.mu.Lock()
		d.cancels[c.StepID] = cancel
		d.mu.Unlock()
		d.wg.Add(1)
		go d.runStep(stepCtx, c)
	case workflow.CommandCancelStep:
		d.mu.Lock()
		if cancel := d.cancels[c.StepID]; cancel != nil {
			cancel()
			delete(d.cancels, c.StepID)
		}
		d.mu.Unlock()
		d.dropTimer(c.StepID)
	case workflow.CommandScheduleTimer:
		id := c.StepID
		d.mu.Lock()
		defer d.mu.Unlock()
		if old := d.timers[id]; old != nil {
			old.Stop()
		}
		var t *time.Timer
		t = time.AfterFunc(max(c.NotBefore.Sub(d.now()), 0), func() {
			d.mu.Lock()
			if d.timers[id] == t {
				delete(d.timers, id)
			}
			d.mu.Unlock()
			d.send(ctx, workflow.Event{At: d.now(), Kind: workflow.EventTimerFired, StepID: id})
		})
		d.timers[id] = t
	}
}

// dropTimer stops and forgets the timer for a StepID, if there is one.
func (d *driver) dropTimer(id workflow.StepID) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.timers[id]; t != nil {
		t.Stop()
		delete(d.timers, id)
	}
}

func (d *driver) runStep(ctx context.Context, c workflow.Command) {
	defer d.wg.Done()
	defer func() {
		d.mu.Lock()
		if cancel := d.cancels[c.StepID]; cancel != nil {
			cancel() // release the context
			delete(d.cancels, c.StepID)
		}
		d.mu.Unlock()
	}()
	if wait := c.NotBefore.Sub(d.now()); wait > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	output, err := d.call(ctx, c)
	if ctx.Err() != nil {
		return // cancelled: the result is not wanted
	}
	ev := workflow.Event{At: d.now(), StepID: c.StepID}
	if err != nil {
		ev.Kind, ev.Error = workflow.EventStepFailed, toStepError(err)
	} else {
		ev.Kind, ev.Output = workflow.EventStepCompleted, output
	}
	d.send(ctx, ev)
}

func (d *driver) call(ctx context.Context, c workflow.Command) (output any, err error) {
	h := d.r.Handlers[c.StepKind]
	if h == nil {
		return nil, &workflow.StepError{Type: "unknown_kind", Message: fmt.Sprintf("no handler for step kind %q", c.StepKind)}
	}
	defer func() {
		if p := recover(); p != nil {
			err = &workflow.StepError{Type: "panic", Message: fmt.Sprint(p)}
		}
	}()
	return h(ctx, c)
}

func (d *driver) send(ctx context.Context, ev workflow.Event) {
	select {
	case d.events <- ev:
	case <-ctx.Done():
	}
}

func (d *driver) stopTimers() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.timers {
		t.Stop()
	}
}

func toStepError(err error) *workflow.StepError {
	var se *workflow.StepError
	if errors.As(err, &se) {
		return se
	}
	return &workflow.StepError{Type: "error", Message: err.Error(), Retryable: true}
}
