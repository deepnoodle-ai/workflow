// Package local runs workflows in-process: it carries out the commands
// [workflow.Apply] returns by calling [workflow.Handler]s in goroutines,
// and feeds their results back as events.
//
// Use it for tests, CLIs and single-process programs. Handlers have the
// same signature as the postgres host's, so a handler tested here runs
// there unchanged, deferred steps included. A service that must survive
// restarts without losing work should use a durable host (see the postgres
// module), though local can also resume from a saved state: pass Save to
// persist each state and Resume to continue one.
package local

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// ErrNotRunning is returned by [Run.Deliver] and [Run.Cancel] for a run
// that has finished or stopped.
var ErrNotRunning = errors.New("local: the run is not running")

// Runner runs workflows in the current process. One Runner can run many
// runs at once.
type Runner struct {
	// Handlers maps a step kind ("agent", "action", ...) to its handler.
	Handlers map[string]workflow.Handler
	// Save, if set, is called with every state that changed, before its
	// commands are carried out. An error stops the run.
	Save func(ctx context.Context, runID string, st *workflow.State) error
}

// Run is a run in progress, returned by [Runner.Start] and
// [Runner.Resume].
type Run struct {
	// ID is the run's ID, passed to handlers as Job.RunID.
	ID string

	d      *driver
	events chan request
	done   chan struct{}
	st     *workflow.State
	err    error
}

// request is an event for the run's loop. reply is set for an event from
// outside the run, which gets Apply's error back instead of stopping the
// run.
type request struct {
	ev    workflow.Event
	reply chan error
}

// Run starts a run and waits for it: it is [Runner.Start] then
// [Run.Wait].
func (r *Runner) Run(
	ctx context.Context, runID string, def *workflow.Definition, input map[string]any,
) (*workflow.State, error) {
	run, err := r.Start(ctx, runID, def, input)
	if err != nil {
		return nil, err
	}
	return run.Wait()
}

// Start starts a run in the background. It returns an error, and no run,
// when the start event is rejected, such as for an input that does not
// match the definition's input schema.
//
// The run continues until it finishes or ctx is done. When ctx is done
// the run stops, without being cancelled: [Run.Wait] returns the state
// with ctx.Err(), and Resume can continue it. To cancel the run, call
// [Run.Cancel].
func (r *Runner) Start(
	ctx context.Context, runID string, def *workflow.Definition, input map[string]any,
) (*Run, error) {
	d := r.newDriver(def, runID)
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{
		At:    time.Now(),
		Kind:  workflow.EventStart,
		Input: input,
	})
	if err != nil {
		return nil, err
	}
	return d.start(ctx, st, cmds, false, 1), nil
}

// Resume continues a run from a saved state, dispatching the work it had
// in flight again with Job.Claims 2, so handlers must tolerate running an
// attempt again. Deferred steps are dispatched again too. A finished run
// is returned as it is.
func (r *Runner) Resume(
	ctx context.Context, runID string, def *workflow.Definition, st *workflow.State,
) (*Run, error) {
	d := r.newDriver(def, runID)
	if st.Status.Terminal() {
		return d.start(ctx, st, nil, true, 1), nil
	}
	cmds, err := workflow.Outstanding(def, st)
	if err != nil {
		return nil, err
	}
	return d.start(ctx, st, cmds, true, 2), nil
}

// Wait blocks until the run finishes or stops, and returns its last
// state. The error is nil when the run finished; inspect the state's
// Status and Error for the outcome. It is ctx.Err() when the run's context
// ended first, and the error that stopped it otherwise, such as one from
// Save.
func (run *Run) Wait() (*workflow.State, error) {
	<-run.done
	return run.st, run.err
}

// Done is closed when the run finishes or stops.
func (run *Run) Done() <-chan struct{} { return run.done }

// Deliver applies an event to the run, such as the result of a wait step
// or of a step whose handler returned [workflow.ErrDeferred]. The event's
// StepID may be the step's name (approve); see [workflow.Resolve]. A zero
// At is set to now. It returns the error Resolve or Apply rejects the
// event with, and ErrNotRunning once the run has finished or stopped.
func (run *Run) Deliver(ev workflow.Event) error {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	req := request{ev: ev, reply: make(chan error, 1)}
	select {
	case run.events <- req:
	case <-run.done:
		return ErrNotRunning
	}
	select {
	case err := <-req.reply:
		return err
	case <-run.done:
		// The loop replies before it ends, so a reply now is the event's.
		select {
		case err := <-req.reply:
			return err
		default:
			return ErrNotRunning
		}
	}
}

// Cancel cancels the run with a reason. Handlers in flight see their
// contexts cancelled. A run with finally steps runs them before it
// finishes, so give slow cleanup steps a timeout; to stop them too,
// deliver a cancel event with Force. Cancelling again changes nothing.
func (run *Run) Cancel(reason string) error {
	return run.Deliver(workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

type driver struct {
	r     *Runner
	def   *workflow.Definition
	runID string
	run   *Run
	wg    sync.WaitGroup

	mu      sync.Mutex
	cancels map[workflow.StepID]context.CancelFunc
	timers  map[workflow.StepID]*time.Timer
}

func (r *Runner) newDriver(def *workflow.Definition, runID string) *driver {
	return &driver{
		r:       r,
		def:     def,
		runID:   runID,
		cancels: map[workflow.StepID]context.CancelFunc{},
		timers:  map[workflow.StepID]*time.Timer{},
	}
}

// start runs the loop in the background. claims is Job.Claims for the
// first commands.
func (d *driver) start(
	ctx context.Context, st *workflow.State, cmds []workflow.Command, saved bool, claims int,
) *Run {
	d.run = &Run{
		ID:     d.runID,
		d:      d,
		events: make(chan request, 64),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(d.run.done)
		d.run.st, d.run.err = d.loop(ctx, st, cmds, saved, claims)
	}()
	return d.run
}

// loop carries out commands and applies the events they produce until the
// run finishes or ctx is done. saved reports whether st is already saved.
func (d *driver) loop(
	ctx context.Context, st *workflow.State, cmds []workflow.Command, saved bool, claims int,
) (*workflow.State, error) {
	ctx, stop := context.WithCancel(ctx)
	defer func() {
		stop()
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
		for _, c := range cmds {
			d.carryOut(ctx, c, claims)
		}
		// Carried out: a rejected event must not carry them out again.
		cmds, claims = nil, 1
		if st.Status.Terminal() {
			return st, nil
		}

		var req request
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case req = <-d.run.events:
		}
		// A step name resolves to the attempt, so the event, and the
		// timer dropped below, carry the full StepID.
		var next *workflow.State
		var out []workflow.Command
		var err error
		req.ev.StepID, err = workflow.Resolve(d.def, st, req.ev.StepID)
		if err == nil {
			next, out, err = workflow.Apply(d.def, st, req.ev)
		}
		if req.reply != nil {
			req.reply <- err
			if err != nil {
				continue
			}
		} else if err != nil {
			return st, err
		}

		if req.ev.Kind == workflow.EventStepCompleted || req.ev.Kind == workflow.EventStepFailed {
			// The attempt has settled, now or earlier, so its timeout
			// timer is obsolete.
			d.dropTimer(req.ev.StepID)
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
	if err := d.r.Save(ctx, d.runID, st); err != nil {
		return fmt.Errorf("local: save state: %w", err)
	}
	return nil
}

func (d *driver) carryOut(ctx context.Context, c workflow.Command, claims int) {
	switch c.Kind {
	case workflow.CommandDispatchStep:
		stepCtx, cancel := context.WithCancel(ctx)
		d.mu.Lock()
		d.cancels[c.StepID] = cancel
		d.mu.Unlock()
		job := workflow.Job{RunID: d.runID, Claims: claims, Command: c}
		d.wg.Go(func() { d.runStep(stepCtx, job) })
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
		t = time.AfterFunc(max(time.Until(c.NotBefore), 0), func() {
			d.mu.Lock()
			if d.timers[id] == t {
				delete(d.timers, id)
			}
			d.mu.Unlock()
			d.send(ctx, workflow.Event{At: time.Now(), Kind: workflow.EventTimerFired, StepID: id})
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

func (d *driver) runStep(ctx context.Context, job workflow.Job) {
	defer func() {
		d.mu.Lock()
		if cancel := d.cancels[job.StepID]; cancel != nil {
			cancel() // release the context
			delete(d.cancels, job.StepID)
		}
		d.mu.Unlock()
	}()
	if wait := time.Until(job.NotBefore); wait > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	output, err := d.call(ctx, job)
	if ctx.Err() != nil || errors.Is(err, workflow.ErrDeferred) {
		// Cancelled, so the result is not wanted, or handed off: the
		// result arrives through Deliver.
		return
	}
	ev := workflow.Event{At: time.Now(), StepID: job.StepID}
	if err != nil {
		ev.Kind, ev.Error = workflow.EventStepFailed, workflow.StepErrorOf(err)
	} else {
		ev.Kind, ev.Output = workflow.EventStepCompleted, output
	}
	d.send(ctx, ev)
}

func (d *driver) call(ctx context.Context, job workflow.Job) (output any, err error) {
	h := d.r.Handlers[job.StepKind]
	if h == nil {
		return nil, &workflow.StepError{
			Type:    "unknown_kind",
			Message: fmt.Sprintf("no handler for step kind %q", job.StepKind),
		}
	}
	defer func() {
		if p := recover(); p != nil {
			err = &workflow.StepError{Type: "panic", Message: fmt.Sprint(p)}
		}
	}()
	return h(ctx, job)
}

// send queues a handler's result or a timer for the loop.
func (d *driver) send(ctx context.Context, ev workflow.Event) {
	select {
	case d.run.events <- request{ev: ev}:
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
