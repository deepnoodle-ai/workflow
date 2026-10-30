package hostcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
)

// Worker is the worker policy: it claims jobs from the outbox, runs them,
// and settles or gives them back. A backend's public Worker builds one from
// its exported fields and forwards to it.
type Worker struct {
	Backend Backend
	Store   *Store
	// Prefix starts the worker's own error messages: "postgres".
	Prefix string

	Handlers    map[string]workflow.Handler // by step kind
	ID          string
	Concurrency int
	Lease       time.Duration
	Poll        time.Duration
	MaxClaims   int
	Logger      *slog.Logger
	Observer    durable.Observer
	MayClaim    func() bool
	// Wake, when set, calls notify when jobs may be due, until ctx is
	// done. Run calls it on its own goroutine; RunOnce does not.
	Wake func(ctx context.Context, notify func())
	// Hooks are for tests.
	Hooks Hooks
	// Sync, when set, copies the caller's current configuration into the
	// worker, so a backend's Worker behaves as if the core read its fields
	// on every call. The worker calls it under its lock, at Init and at
	// the start of Run, RunOnce, Execute and Shutdown, whenever no claim
	// loop, claim query or job is in progress and Shutdown has not been
	// called. defaults sets the zero fields; Sync calls it after copying.
	Sync func(w *Worker, defaults func())

	mu       sync.Mutex
	runs     int                // claim loops in progress
	hardStop bool               // a Run returned with its context done
	active   int                // claimed jobs not yet settled or given back
	claiming int                // claim queries in progress, counted by Active
	stopped  chan struct{}      // closed by Shutdown: claim nothing
	finished chan struct{}      // closed once stopped and no claim loop runs
	cut      context.Context    // done when a Shutdown deadline passes
	cutOff   context.CancelFunc // ends cut
}

// Hooks let tests fail or observe single steps of the worker.
type Hooks struct {
	Finish      func() error              // fail finishing a deferred job
	Heartbeat   func() error              // fail heartbeats
	Settle      func(Claimed) error       // fail settling
	HandlerDone func(Claimed, any, error) // observe a handler's return
	Claim       func(context.Context)     // run once a claim query returns, before admit
	Release     func(context.Context)     // run before giving a claim back
}

// outcome is what processing a claimed job did, for RunOnce and Execute.
type outcome struct {
	settled bool            // this claim settled the job
	result  *durable.Result // the settle's, when it applied an event
	err     error           // the job is still there: try again later
}

const (
	// giveBackTimeout bounds giving a claim back, and other single writes.
	giveBackTimeout = 5 * time.Second
	// cutWriteTimeout bounds each write after a Shutdown deadline passes.
	cutWriteTimeout = time.Second
)

// Init syncs the configuration (see Sync) and sets the defaults of the
// fields that are zero. A backend's Worker calls it from each of its
// methods.
func (w *Worker) Init() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.defaults()
	w.sync()
}

// sync calls Sync while the worker is idle and not shut down. w.mu must be
// held, and defaults must have run.
func (w *Worker) sync() {
	if w.Sync == nil || w.isStopped() || w.runs > 0 || w.active > 0 || w.claiming > 0 {
		return
	}
	w.Sync(w, w.defaults)
}

func (w *Worker) defaults() {
	if w.stopped == nil {
		w.stopped = make(chan struct{})
		w.finished = make(chan struct{})
		w.cut, w.cutOff = context.WithCancel(context.Background())
	}
	if w.ID == "" {
		w.ID = "worker-" + uuid.NewV4().String()[:8]
	}
	if w.Concurrency <= 0 {
		w.Concurrency = 8
	}
	if w.Lease <= 0 {
		w.Lease = 30 * time.Second
	}
	if w.Poll <= 0 {
		w.Poll = time.Second
	}
	if w.MaxClaims <= 0 {
		w.MaxClaims = 5
	}
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
}

// Run claims and runs jobs until ctx is done or Shutdown is called, then
// returns nil. See the backend's Worker.Run.
func (w *Worker) Run(ctx context.Context) error {
	if !w.enter() {
		w.Logger.Warn("workflow worker: Run called after Shutdown; not claiming")
		return nil
	}
	defer w.leave()
	// A Run that returns with ctx done stopped hard, by whatever path it
	// returns. This runs after its goroutines and before leave, so a
	// waiting Shutdown sees it. It is never cleared.
	defer func() {
		if ctx.Err() != nil {
			w.mu.Lock()
			w.hardStop = true
			w.mu.Unlock()
		}
	}()
	wake := make(chan struct{}, 1)
	notify := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		running int
	)
	defer wg.Wait()
	// Handlers stop on a hard stop or when a Shutdown deadline passes.
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopCut := context.AfterFunc(w.cut, cancel)
	defer stopCut()
	lctx, stopListen := context.WithCancel(ctx)
	defer stopListen()
	if w.Wake != nil {
		wg.Go(func() {
			w.Wake(lctx, notify)
		})
	}

	stopped := w.stopped
	for {
		mu.Lock()
		free := w.Concurrency - running
		idle := running == 0
		mu.Unlock()
		if idle && w.isStopped() {
			return nil
		}
		// After a hard stop no claim starts: one would only be given back.
		if free > 0 && ctx.Err() == nil && w.openClaim() {
			// Neither the gate nor a hard stop cancels a claim query: its
			// rows could be lost after the claim committed. See
			// claimContext. Jobs it returns after a hard stop go back.
			cctx, cancelClaim := w.claimContext(ctx)
			jobs, err := w.claim(cctx, free)
			// A claim that the cut-off or the time limit cancelled is no error.
			if err != nil && cctx.Err() == nil {
				w.Logger.Error("workflow worker: claim", "error", err)
			}
			cancelClaim()
			jobs = w.admit(jobs, ctx.Err() != nil)
			for _, t := range jobs {
				mu.Lock()
				running++
				mu.Unlock()
				wg.Go(func() {
					w.process(hctx, t)
					w.settled()
					mu.Lock()
					running--
					mu.Unlock()
					notify()
				})
			}
			if len(jobs) == free {
				continue
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-stopped:
			stopped = nil // seen; a closed channel would spin the loop
		case <-time.After(w.Poll):
		}
	}
}

// Shutdown stops the worker gracefully. See the backend's Worker.Shutdown.
func (w *Worker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	w.defaults()
	first := !w.isStopped()
	if first {
		w.sync()
		close(w.stopped)
	}
	w.finishLocked()
	active, finished := w.active+w.claiming, w.finished
	w.mu.Unlock()
	if first {
		w.Logger.Info("workflow worker: shutting down", "active", active)
	}

	var err error
	select {
	case <-finished:
	case <-ctx.Done():
		w.Logger.Warn("workflow worker: shutdown deadline passed; cancelling active jobs",
			"active", w.Active())
		w.cutOff()
		<-finished
		err = ctx.Err()
	}

	w.mu.Lock()
	hard := w.hardStop
	w.mu.Unlock()
	if hard {
		w.Logger.Warn("workflow worker: Run's context ended before Shutdown finished; " +
			"active jobs were cancelled at once (a hard stop). Give Run a context " +
			"that you cancel only for a hard stop")
		err = errors.Join(durable.ErrHardStop, err)
	}
	return err
}

// Active returns the number of active jobs. See the backend's
// Worker.Active.
func (w *Worker) Active() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active + w.claiming
}

// enter registers a claim loop. It returns false after Shutdown.
func (w *Worker) enter() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.defaults()
	if w.isStopped() {
		return false
	}
	w.sync()
	w.runs++
	return true
}

// leave unregisters a claim loop once its jobs are settled or given back.
func (w *Worker) leave() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.runs--
	w.finishLocked()
}

// finishLocked closes finished once Shutdown was called and no claim loop
// runs. w.mu must be held.
func (w *Worker) finishLocked() {
	if w.runs > 0 || !w.isStopped() {
		return
	}
	select {
	case <-w.finished:
	default:
		close(w.finished)
	}
}

func (w *Worker) isStopped() bool {
	select {
	case <-w.stopped:
		return true
	default:
		return false
	}
}

// cutDone reports whether a Shutdown deadline has passed.
func (w *Worker) cutDone() bool {
	return w.cut != nil && w.cut.Err() != nil
}

// mayClaim is the claim gate that Shutdown and MayClaim share.
func (w *Worker) mayClaim() bool {
	if w.isStopped() {
		return false
	}
	return w.MayClaim == nil || w.MayClaim()
}

// openClaim passes the claim gate for one claim query. The query counts as
// active from before the gate check until admit, so a caller that closes
// the gate and then sees Active() == 0 knows that no claim is held. Every
// claim after a true result must be followed by admit, even when it fails.
func (w *Worker) openClaim() bool {
	w.mu.Lock()
	w.claiming++
	w.mu.Unlock()
	if w.mayClaim() {
		return true
	}
	w.mu.Lock()
	w.claiming--
	w.mu.Unlock()
	return false
}

// admit ends a claim that openClaim allowed and takes ownership of its jobs,
// counting them as active. If the gate closed while the claim query ran, or
// hardStop says Run's context ended, it gives them back unrun.
func (w *Worker) admit(jobs []Claimed, hardStop bool) []Claimed {
	if len(jobs) > 0 && (hardStop || !w.mayClaim()) {
		// In parallel, so a batch takes no longer than one give-back. Each
		// was claimed, so it fires Claimed, then GivenBack from release.
		var wg sync.WaitGroup
		for _, t := range jobs {
			wg.Go(func() {
				w.observeClaimed(t)
				w.release(t)
			})
		}
		wg.Wait()
		jobs = nil
	}
	w.mu.Lock()
	w.active += len(jobs)
	w.claiming--
	w.mu.Unlock()
	return jobs
}

// settled marks one admitted job as settled or given back.
func (w *Worker) settled() {
	w.mu.Lock()
	w.active--
	w.mu.Unlock()
}

// bound limits a write to deadline and, once a Shutdown deadline passes, to
// cutWriteTimeout (see cutAfter). It ignores parent's cancellation.
func (w *Worker) bound(
	parent context.Context, deadline time.Time,
) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), deadline)
	cctx, ccancel := w.cutAfter(ctx)
	return cctx, func() { ccancel(); cancel() }
}

// claimContext is the context of Run's claim query. A hard stop (ctx ends)
// does not cancel it, because a claim that commits as its query is
// cancelled loses its rows: the jobs wait out a lease and the claim counts
// toward MaxClaims. It ends giveBackTimeout after the hard stop instead, so
// Run still returns, and as cutAfter says after a Shutdown deadline.
func (w *Worker) claimContext(ctx context.Context) (context.Context, context.CancelFunc) {
	cctx, cancel := w.cutAfter(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() {
		time.AfterFunc(giveBackTimeout, cancel)
	})
	return cctx, func() {
		stop()
		cancel()
	}
}

// cutAfter ends ctx cutWriteTimeout after a Shutdown deadline passes, so a
// write in progress at that moment gets one more second; if it has passed
// already, cutWriteTimeout from now, so a write that starts later (a
// handler that ignored its context) gets a second of its own.
func (w *Worker) cutAfter(ctx context.Context) (context.Context, context.CancelFunc) {
	if w.cut == nil {
		return context.WithCancel(ctx)
	}
	if w.cutDone() {
		// AfterFunc on a done context would cancel the write at once.
		return context.WithTimeout(ctx, cutWriteTimeout)
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.cut, func() {
		time.AfterFunc(cutWriteTimeout, cancel)
	})
	return ctx, func() { stop(); cancel() }
}

// pause waits d between attempts of a write. It ends early when ctx is done
// or a Shutdown deadline passes; the caller then makes its last attempt.
func (w *Worker) pause(ctx context.Context, d time.Duration) {
	var cut <-chan struct{}
	if w.cut != nil {
		cut = w.cut.Done()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-cut:
	case <-t.C:
	}
}

// queues are what this worker claims: timers, and steps of the kinds it
// has handlers for. Workers with different handlers can share one outbox.
func (w *Worker) queues() []string {
	out := []string{"timer"}
	for k := range w.Handlers {
		out = append(out, "step:"+k)
	}
	return out
}

// claim leases up to limit due jobs.
func (w *Worker) claim(ctx context.Context, limit int) ([]Claimed, error) {
	return w.claimUntil(ctx, limit, time.Time{})
}

// claimUntil is claim, leaving out jobs due after a non-zero dueBy.
func (w *Worker) claimUntil(
	ctx context.Context, limit int, dueBy time.Time,
) ([]Claimed, error) {
	jobs, err := w.Backend.Claim(ctx, ClaimRequest{
		Owner:  w.ID,
		Queues: w.queues(),
		Limit:  limit,
		Lease:  w.Lease,
		DueBy:  dueBy,
	})
	if err == nil && w.Hooks.Claim != nil {
		w.Hooks.Claim(ctx)
	}
	if err != nil && ctx.Err() == nil && w.Observer.ClaimError != nil {
		w.Observer.ClaimError(err)
	}
	return jobs, err
}

// process runs a claimed job and settles it. Run ignores the outcome;
// RunOnce and Execute read it.
func (w *Worker) process(ctx context.Context, t Claimed) outcome {
	log := w.Logger.With("run", t.RunID, "step_id", t.StepID)
	w.observeClaimed(t)
	// Nothing runs for a run this store cannot advance.
	if _, err := w.Store.Definition(ctx, w.Backend, t.DefHash); err != nil {
		return w.unloadable(ctx, t, err, log)
	}

	abandoned := func(what string) *workflow.RunError {
		return &workflow.RunError{
			Step:    t.Step,
			StepID:  t.StepID,
			Type:    "abandoned",
			Message: fmt.Sprintf("%s claimed %d times without finishing", what, t.Claims-1),
		}
	}
	switch {
	case t.Kind == workflow.CommandScheduleTimer && t.Claims > w.MaxClaims:
		// A timer that cannot fire leaves nothing to fail but the run.
		log.Error("workflow worker: timer could not be fired; failing the run",
			"claims", t.Claims-1)
		return w.abort(ctx, t, abandoned("timer"), log)
	case t.Kind == workflow.CommandScheduleTimer:
		return w.settle(ctx, t, t.At, workflow.Event{
			Kind:   workflow.EventTimerFired,
			StepID: t.StepID,
		}, log)
	case t.Claims > w.MaxClaims+1:
		// Not even the abandoned failure below could be applied.
		log.Error("workflow worker: step could not be failed; failing the run",
			"claims", t.Claims-1)
		return w.abort(ctx, t, abandoned("step"), log)
	case t.Claims > w.MaxClaims:
		return w.settle(ctx, t, t.At, workflow.Event{
			Kind:   workflow.EventStepFailed,
			StepID: t.StepID,
			Error: &workflow.StepError{
				Type:    "abandoned",
				Message: fmt.Sprintf("claimed %d times without finishing", t.Claims-1),
			},
		}, log)
	}

	stepCtx, cancel := context.WithCancel(ctx)
	// The heartbeat holds the lease until the handler returns. In Run it
	// stops with ctx, a hard stop. For a request (RunOnce, Execute) a
	// handler that ignores its context runs on after the request ends, so
	// the lease must too, or another claim runs the step again beside it.
	// There only a Shutdown deadline stops it.
	beatCtx, stopBeat := stepCtx, cancel
	if t.request {
		beatCtx, stopBeat = w.handlerContext(context.WithoutCancel(ctx))
	}
	lost := make(chan struct{})
	beat := make(chan struct{})
	var (
		renewed time.Time // when the lease was last extended
		lostErr error     // why the claim was lost
	)
	go func() {
		defer close(beat)
		renewed = w.heartbeat(beatCtx, t, func(err error) {
			lostErr = err
			close(lost)
			cancel()
		})
	}()
	start := time.Now()
	output, err := w.call(stepCtx, t)
	ran := time.Since(start)
	cancel()
	stopBeat()
	<-beat
	t.ran, t.renewed = ran, renewed // the heartbeat goroutine reads t until it is done
	if w.Hooks.HandlerDone != nil {
		w.Hooks.HandlerDone(t, output, err)
	}

	select {
	case <-lost:
		if errors.Is(lostErr, errNoHeartbeat) {
			// The database did not answer for a whole lease: an error,
			// not "nothing to do".
			w.observeDropped(t, lostErr)
			return outcome{err: fmt.Errorf("%s: %w", w.Prefix, lostErr)}
		}
		log.Info("workflow worker: dropping result; the job was cancelled or reclaimed")
		if errors.Is(err, workflow.ErrDeferred) && errors.Is(lostErr, ErrJobGone) {
			w.observeDeferred(t) // handed off, and its result may already be in
			return outcome{}
		}
		w.observeDropped(t, lostErr)
		return outcome{}
	default:
	}
	if ctx.Err() != nil && interrupted(err) {
		// Shutting down, and the error may be only the cancellation: give
		// the claim back so another worker runs the step. A result, a
		// hand-off or a permanent failure is recorded below, because that
		// work is done and running it again would repeat it.
		if t.request && !w.cutDone() {
			// The caller's context ended, not the worker: a request
			// deadline. Count the claim, so a step that always outlives
			// its request fails as abandoned instead of running forever.
			w.giveBack(t, false)
			return outcome{err: ctx.Err()}
		}
		w.release(t)
		return outcome{err: fmt.Errorf("%s: %w", w.Prefix, ErrShuttingDown)}
	}
	ev := workflow.Event{StepID: t.StepID}
	switch {
	case errors.Is(err, workflow.ErrDeferred):
		// Past the lease, another worker may claim the job anyway.
		fctx, cancel := w.bound(ctx, renewed.Add(w.Lease))
		defer cancel()
		return w.finishDeferred(fctx, t, log)
	case err != nil:
		ev.Kind, ev.Error = workflow.EventStepFailed, workflow.StepErrorOf(err)
	default:
		ev.Kind, ev.Output = workflow.EventStepCompleted, output
	}
	return w.settle(ctx, t, renewed, ev, log)
}

// unloadable handles a job whose run's definition the store cannot load.
// A database error is left to the lease. A definition that does not load
// is a configuration problem, perhaps a deploy that has not reached every
// instance: the job waits a lease, doubling each time, and once it has
// been claimed MaxClaims times the run fails with
// ErrorTypeDefinitionUnavailable.
func (w *Worker) unloadable(
	ctx context.Context, t Claimed, err error, log *slog.Logger,
) outcome {
	if !errors.Is(err, durable.ErrDefinitionUnavailable) {
		if ctx.Err() != nil {
			w.release(t) // shutting down before anything ran
			return outcome{err: ctx.Err()}
		}
		log.Error("workflow worker: load definition", "error", err)
		w.observeDropped(t, err) // left to its lease
		return outcome{err: err}
	}
	if t.Claims >= w.MaxClaims {
		log.Error("workflow worker: failing the run; its definition cannot be loaded", "error", err)
		return w.abort(ctx, t, &workflow.RunError{
			Type:    durable.ErrorTypeDefinitionUnavailable,
			Message: err.Error(),
		}, log)
	}
	delay := w.Lease
	for i := 1; i < t.Claims && delay < time.Hour; i++ {
		delay *= 2
	}
	log.Error("workflow worker: the run's definition cannot be loaded; check the store's kinds",
		"error", err, "claims", t.Claims, "max_claims", w.MaxClaims, "retry_in", delay)
	pctx, cancel := w.bound(ctx, time.Now().Add(giveBackTimeout))
	defer cancel()
	// A claim that is over (the job is gone or reclaimed) has nothing to
	// postpone.
	if err := w.Backend.Postpone(pctx, t.Lease, delay); err != nil && !errors.Is(err, ErrLeaseLost) {
		log.Warn("workflow worker: postpone job", "error", err)
	}
	return outcome{err: err}
}

// abort fails the job's run without applying an event, fenced by the
// claim, within the claim's lease.
func (w *Worker) abort(
	ctx context.Context, t Claimed, runErr *workflow.RunError, log *slog.Logger,
) outcome {
	actx, cancel := w.bound(ctx, t.At.Add(w.Lease))
	defer cancel()
	err := w.Backend.Write(actx, func(tx Tx) error {
		st, _, err := w.Store.LockRun(actx, tx, t.RunID)
		if err != nil {
			return err
		}
		if err := w.deleteJob(actx, tx, t); err != nil {
			return err
		}
		return w.Store.Abort(actx, tx, t.RunID, st, runErr, time.Now().UTC())
	})
	if err == nil {
		w.observeAbandoned(t)
		return outcome{settled: true}
	}
	w.observeDropped(t, err)
	if errors.Is(err, ErrLeaseLost) || errors.Is(err, durable.ErrRunNotFound) {
		return outcome{}
	}
	log.Error("workflow worker: fail run", "error", err)
	return outcome{err: err}
}

// interrupted reports whether a handler's error, returned while the worker
// shuts down, may be due to the shutdown: the context's own error, or any
// retryable error (a cancelled request often surfaces as one). Running such
// an attempt again is safe. A permanent failure is a real result.
func interrupted(err error) bool {
	switch {
	case err == nil, errors.Is(err, workflow.ErrDeferred):
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return true
	}
	return workflow.StepErrorOf(err).Retryable
}

func (w *Worker) call(ctx context.Context, t Claimed) (output any, err error) {
	h := w.Handlers[t.StepKind]
	if h == nil {
		return nil, &workflow.StepError{
			Type:    "unknown_kind",
			Message: fmt.Sprintf("no handler for step kind %q", t.StepKind),
		}
	}
	defer func() {
		if p := recover(); p != nil {
			err = &workflow.StepError{Type: "panic", Message: fmt.Sprint(p)}
		}
	}()
	return h(ctx, t.Job)
}

// errNoHeartbeat is a lease that ran out because no heartbeat succeeded
// for a whole lease, such as when the database is unreachable. It matches
// ErrLeaseLost.
var errNoHeartbeat = fmt.Errorf("%w: no heartbeat succeeded for a whole lease", ErrLeaseLost)

// heartbeat extends the lease until ctx is done. It calls lost when the
// job is gone (ErrJobGone), another worker has claimed it, or the lease has
// run out without a successful heartbeat (errNoHeartbeat, with the last
// error; another worker may claim it now). It returns when the lease was
// last extended.
func (w *Worker) heartbeat(ctx context.Context, t Claimed, lost func(error)) time.Time {
	tick := time.NewTicker(w.Lease / 3)
	defer tick.Stop()
	lastOK := t.At

	for {
		select {
		case <-ctx.Done():
			return lastOK
		case <-tick.C:
		}

		var err error
		if w.Hooks.Heartbeat != nil {
			err = w.Hooks.Heartbeat()
		}
		attempt := time.Now()
		if err == nil {
			err = w.Backend.Renew(ctx, t.Lease, w.Lease)
		}
		if errors.Is(err, ErrLeaseLost) {
			lost(lostClaim(err, attempt.Before(lastOK.Add(w.Lease))))
			return lastOK
		}
		if err != nil {
			if ctx.Err() != nil {
				return lastOK
			}
			if time.Since(lastOK) >= w.Lease {
				w.Logger.Warn("workflow worker: lease expired without a heartbeat; stopping the step",
					"run", t.RunID, "step_id", t.StepID, "error", err)
				lost(fmt.Errorf("%w: %w", errNoHeartbeat, err))
				return lastOK
			}
			continue // transient; the lease has slack
		}
		lastOK = attempt // the database's clock is no earlier
	}
}

// settle delivers a job's result and removes the job in one transaction.
// The fence shuts out a worker whose job another worker has claimed since.
//
// It retries transient errors with backoff until the lease that started at
// renewed runs out: past it another worker may claim the job, and a step
// result that was not delivered is then lost and its attempt runs again.
// So settling is bounded by the lease even during a hard stop. A timer whose
// settle fails during a hard stop is given back at once; firing it is cheap
// and safe to repeat. During Shutdown, retrying also stops when its deadline
// passes; a result still not delivered then is given back: the step runs
// again either way, and a give-back does not count toward MaxClaims.
func (w *Worker) settle(
	parent context.Context, t Claimed, renewed time.Time, ev workflow.Event, log *slog.Logger,
) outcome {
	ctx, cancel := w.bound(parent, renewed.Add(w.Lease))
	defer cancel()
	backoff := 50 * time.Millisecond
	ev = Storable(ev) // once, so Settled reports the event the store records

	for {
		res, rejected, err := w.settleOnce(ctx, t, ev, log)
		switch {
		case err == nil && rejected:
			w.observeCancelled(t)
			return outcome{settled: true}
		case err == nil:
			w.observeSettled(t, ev)
			return outcome{settled: true, result: res}
		case errors.Is(err, ErrLeaseLost):
			log.Info("workflow worker: dropping result; the job was cancelled or reclaimed")
			w.observeDropped(t, err)
			return outcome{}
		case errors.Is(err, durable.ErrRunNotFound):
			log.Warn("workflow worker: run no longer exists")
			w.observeDropped(t, err)
			return outcome{}
		case errors.Is(err, durable.ErrDefinitionUnavailable):
			// The next claim checks the definition before anything runs.
			log.Error("workflow worker: deliver result", "error", err)
			w.observeDropped(t, err) // left to its lease
			return outcome{err: err}
		case ev.Kind == workflow.EventStepCompleted && w.Backend.Unstorable(err):
			// The database rejected what the result made of the run. The
			// work is done and running it again would end the same way:
			// record the step as failed instead.
			log.Warn("workflow worker: result cannot be stored; failing the step", "error", err)
			ev = Storable(invalidOutput(ev, "the run cannot be stored with this output: "+err.Error()))
			continue
		}

		if t.Kind == workflow.CommandScheduleTimer && (parent.Err() != nil || w.cutDone()) {
			log.Warn("workflow worker: shutting down before a timer fired; giving it back",
				"error", err)
			w.release(t)
			return outcome{err: err}
		}
		if w.cutDone() {
			log.Warn("workflow worker: could not deliver the result before the shutdown deadline; giving the job back",
				"error", err)
			w.release(t)
			return outcome{err: err}
		}
		if ctx.Err() != nil {
			log.Error("workflow worker: could not deliver the result before the lease ran out; the step may run again",
				"error", err)
			w.observeDropped(t, err)
			return outcome{err: err}
		}
		log.Warn("workflow worker: deliver result; retrying", "error", err)
		w.pause(ctx, backoff)
		backoff = min(backoff*2, w.Lease/3)
	}
}

// settleOnce reports rejected when the run will never accept the event: the
// job is deleted and nothing is recorded.
func (w *Worker) settleOnce(
	ctx context.Context, t Claimed, ev workflow.Event, log *slog.Logger,
) (res *durable.Result, rejected bool, err error) {
	if w.Hooks.Settle != nil {
		if err := w.Hooks.Settle(t); err != nil {
			return nil, false, err
		}
	}
	err = w.Backend.Write(ctx, func(tx Tx) error {
		// Lock the run before the job, the same order Deliver uses when
		// it deletes jobs, so the two cannot deadlock.
		st, hash, err := w.Store.LockRun(ctx, tx, t.RunID)
		if err != nil {
			return err
		}
		if err := w.deleteJob(ctx, tx, t); err != nil {
			return err
		}
		res, err = w.Store.DeliverLocked(ctx, tx, t.RunID, st, hash, ev)
		if IsPermanent(err) {
			// The run will never accept this event; keep the job
			// deletion so it does not come back.
			log.Warn("workflow worker: run rejected event", "error", err)
			rejected = true
			return nil
		}
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return res, rejected, nil
}

// finishDeferred marks a handed-off job done. It retries until the delete
// succeeds, because a job left claimed would run its handler again when the
// lease expires: a second agent turn, a second approval request.
func (w *Worker) finishDeferred(ctx context.Context, t Claimed, log *slog.Logger) outcome {
	backoff := 50 * time.Millisecond
	for {
		err := w.finish(ctx, t)
		if err == nil || errors.Is(err, ErrJobGone) {
			// A gone job was settled by Store.Deliver, or the run no longer
			// wants it; either way the hand-off happened and nothing runs
			// again.
			w.observeDeferred(t)
			return outcome{settled: err == nil}
		}
		if errors.Is(err, ErrLeaseLost) {
			w.observeDropped(t, err)
			return outcome{}
		}
		if ctx.Err() != nil || w.cutDone() {
			// Not given back: that would run the handler again at once.
			log.Error("workflow worker: stopped before marking a deferred job done; it may run again",
				"error", err)
			w.observeDropped(t, err)
			return outcome{err: err}
		}
		log.Warn("workflow worker: finish deferred job; retrying", "error", err)
		w.pause(ctx, backoff)
		backoff = min(backoff*2, w.Lease/3)
	}
}

func (w *Worker) finish(ctx context.Context, t Claimed) error {
	if w.Hooks.Finish != nil {
		if err := w.Hooks.Finish(); err != nil {
			return err
		}
	}
	return w.Backend.Write(ctx, func(tx Tx) error {
		return w.deleteJob(ctx, tx, t)
	})
}

// deleteJob removes claim t's job. Its callers bound ctx by the claim's
// lease, so a delete that starts at all starts while the lease is live.
func (w *Worker) deleteJob(ctx context.Context, tx Tx, t Claimed) error {
	start := time.Now()
	err := tx.DeleteJob(ctx, t.Lease)
	if errors.Is(err, ErrLeaseLost) {
		end, ok := ctx.Deadline()
		return lostClaim(err, ok && start.Before(end))
	}
	return err
}

// release gives a claim back on shutdown so another worker can take it
// without waiting for the lease to expire. A graceful release does not count
// toward MaxClaims, so it refunds the claim; the fence stays, so this claim
// stays fenced out.
//
// It takes giveBackTimeout, and during Shutdown no longer than a write
// after the deadline (see cutAfter). It fires the claim's ending event.
func (w *Worker) release(t Claimed) { w.giveBack(t, true) }

// giveBack clears a claim's lease so the job can be claimed at once, with
// release's time bounds, and fires the claim's ending event. refund takes
// the claim back off the job's count toward MaxClaims.
func (w *Worker) giveBack(t Claimed, refund bool) {
	ctx, cancel := context.WithTimeout(context.Background(), giveBackTimeout)
	defer cancel()
	ctx, cancelCut := w.cutAfter(ctx)
	defer cancelCut()
	renewed := t.At
	if t.renewed.After(renewed) {
		renewed = t.renewed
	}
	if w.Hooks.Release != nil {
		w.Hooks.Release(ctx)
	}
	start := time.Now()
	err := w.Backend.Release(ctx, t.Lease, refund)
	switch {
	case errors.Is(err, ErrLeaseLost):
		live := start.Before(renewed.Add(w.Lease))
		w.observeDropped(t, lostClaim(err, live))
	case err != nil:
		w.observeDropped(t, err) // left to its lease
	default:
		if f := w.Observer.GivenBack; f != nil {
			f(t.Job)
		}
	}
}

// lostClaim says why a fenced write changed no row, given the backend's
// answer (rule 1) and whether the write started before the claim's lease
// ran out.
//
// A newer claim holds the job: ErrLeaseLost. The job is gone and the lease
// was live: ErrJobGone, because no other worker can claim a job under a
// live lease, so the run removed it. The job is gone after the lease: also
// ErrLeaseLost, because another worker may have claimed the job and
// settled it, which deletes the row too.
func lostClaim(err error, live bool) error {
	if live && errors.Is(err, ErrJobGone) {
		return ErrJobGone
	}
	return ErrLeaseLost
}
