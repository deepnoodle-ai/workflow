// Package local runs workflows in-process: it carries out the commands
// [workflow.Apply] returns by calling Go handlers in goroutines, and feeds
// their results back as events.
//
// Use it for tests, CLIs and single-process programs. A service that must
// survive restarts without losing work should use a durable host (see the
// postgres module), though local can also resume from a saved state: pass
// Save to persist each state and Resume to continue one.
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
	// Save, if set, is called with every new state before its commands
	// are carried out. An error stops the run.
	Save func(ctx context.Context, st *workflow.State) error
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
}

// Run starts a run and blocks until it finishes or ctx is done. It returns
// the last state; inspect its Status and Error for the outcome. When ctx is
// done Run returns the state with ctx.Err(), and Resume can continue it.
func (r *Runner) Run(ctx context.Context, def *workflow.Definition, inputs, trigger map[string]any) (*workflow.State, error) {
	d := r.newDriver(def)
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{
		At: d.now(), Kind: workflow.EventStart, Inputs: inputs, Trigger: trigger,
	})
	if err != nil {
		return nil, err
	}
	return d.loop(ctx, st, cmds)
}

// Resume continues a run from a saved state, re-dispatching the work it had
// in flight. Handlers must tolerate running an attempt again.
func (r *Runner) Resume(ctx context.Context, def *workflow.Definition, st *workflow.State) (*workflow.State, error) {
	if st.Status.Terminal() {
		return st, nil
	}
	cmds, err := workflow.Outstanding(def, st)
	if err != nil {
		return st, err
	}
	return r.newDriver(def).loop(ctx, st, cmds)
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

func (d *driver) loop(ctx context.Context, st *workflow.State, cmds []workflow.Command) (*workflow.State, error) {
	ctx, stop := context.WithCancel(ctx)
	defer func() {
		stop()
		d.stopTimers()
		d.wg.Wait()
	}()
	for {
		if d.r.Save != nil {
			if err := d.r.Save(ctx, st); err != nil {
				return st, fmt.Errorf("local: save state: %w", err)
			}
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
			return st, ctx.Err()
		case ev = <-d.events:
		}
		next, out, err := workflow.Apply(d.def, st, ev)
		if err != nil {
			return st, err
		}
		st, cmds = next, out
	}
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
		}
		if t := d.timers[c.StepID]; t != nil {
			t.Stop()
		}
		d.mu.Unlock()
	case workflow.CommandScheduleTimer:
		id := c.StepID
		t := time.AfterFunc(max(c.NotBefore.Sub(d.now()), 0), func() {
			d.send(ctx, workflow.Event{At: d.now(), Kind: workflow.EventTimerFired, StepID: id})
		})
		d.mu.Lock()
		if old := d.timers[id]; old != nil {
			old.Stop()
		}
		d.timers[id] = t
		d.mu.Unlock()
	}
}

func (d *driver) runStep(ctx context.Context, c workflow.Command) {
	defer d.wg.Done()
	defer func() {
		d.mu.Lock()
		delete(d.cancels, c.StepID)
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
