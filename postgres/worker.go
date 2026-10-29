package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errLeaseLost = errors.New("postgres: lease lost")

// ErrHardStop is returned by Shutdown when the context of a Run call ended
// before Shutdown finished. That Run stopped hard: it cancelled its active
// jobs at once instead of letting them run until the Shutdown deadline. Give
// Run a context that you cancel only for a hard stop, not the one that a
// SIGTERM cancels.
var ErrHardStop = errors.New("postgres: Run's context ended before Shutdown finished; " +
	"active jobs were cancelled at once (a hard stop)")

// claimedJob is one claimed job: a step attempt or a timer, with what the
// worker needs to settle it.
type claimedJob struct {
	ID int64
	workflow.Job

	fence   int64         // this claim's fencing token
	claimed time.Time     // when the claim was made; its lease runs from here
	defHash string        // the run's definition
	ran     time.Duration // how long the handler ran
	renewed time.Time     // when the heartbeat last extended the lease
}

// Worker claims jobs from the outbox and runs them.
type Worker struct {
	Store    *Store
	Handlers map[string]workflow.Handler // by step kind
	// ID identifies the worker in lease_owner. Default: random.
	ID string
	// Concurrency caps the jobs run at once. Default 8.
	Concurrency int
	// Lease is how long a claim lasts without a heartbeat. Default 30s.
	Lease time.Duration
	// Poll is how often to look for due jobs without a notification.
	// Default 1s.
	Poll time.Duration
	// MaxClaims fails a step that has been claimed this many times without
	// finishing, so a step that crashes its worker cannot loop forever.
	// It bounds timers too, and how long a run whose definition cannot be
	// loaded waits for the configuration to be fixed (see
	// ErrDefinitionUnavailable). Default 5.
	MaxClaims int
	Logger    *slog.Logger
	// ListenConfig is the connection the worker LISTENs on to wake as soon
	// as jobs are written. It is a dedicated connection, not one from the
	// pool, closed when Run returns. Default: the pool's connection config.
	// LISTEN needs a session, so when the pool goes through a
	// transaction-mode pooler (Neon's -pooler endpoint, PgBouncer), point
	// this at the direct endpoint; otherwise the worker still works, but
	// only wakes every Poll.
	ListenConfig *pgx.ConnConfig
	// Observer is called as the worker claims and ends jobs, for metrics.
	// The zero value calls nothing.
	Observer Observer
	// MayClaim, when set, is called before each claim and when a claim
	// returns. While it returns false the worker claims nothing, timers
	// included, and gives back a claim that returns; the jobs it holds keep
	// running and settle. Claiming resumes at the next wake-up or poll once
	// it returns true. It must be fast and safe for concurrent use. Nil
	// means always.
	MayClaim func() bool

	mu       sync.Mutex
	runs     int                // claim loops in progress
	hardStop bool               // a Run returned with its context done
	active   int                // claimed jobs not yet settled or given back
	claiming int                // claim queries in progress, counted by Active
	stopped  chan struct{}      // closed by Shutdown: claim nothing
	finished chan struct{}      // closed once stopped and no claim loop runs
	cut      context.Context    // done when a Shutdown deadline passes
	cutOff   context.CancelFunc // ends cut

	finishHook    func() error                 // tests: fail finishing a deferred job
	heartbeatHook func() error                 // tests: fail heartbeats
	settleHook    func(claimedJob) error       // tests: fail settling
	handlerDone   func(claimedJob, any, error) // tests: observe a handler's return
	claimHook     func(context.Context)        // tests: run while a claim query is open
	releaseHook   func(context.Context)        // tests: run before giving a claim back
}

const (
	// giveBackTimeout bounds giving a claim back, and other single writes.
	giveBackTimeout = 5 * time.Second
	// cutWriteTimeout bounds each write after a Shutdown deadline passes.
	cutWriteTimeout = time.Second
)

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
// returns nil. Cancelling ctx is a hard stop: every handler's context is
// cancelled at once, and settling is bounded by the claim's lease, so Run
// returns within about Lease of ctx being done (plus however long handlers
// take to stop). For a graceful stop, give Run a context that you cancel
// only for a hard stop, and call Shutdown. On return every handler has
// finished, and each claim was either settled or given back.
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
	wg.Go(func() {
		w.listen(lctx, notify)
	})

	stopped := w.stopped
	for {
		mu.Lock()
		free := w.Concurrency - running
		idle := running == 0
		mu.Unlock()
		if idle && w.isStopped() {
			return nil
		}
		if free > 0 && w.openClaim() {
			// The gate never cancels a claim query: its rows could be lost
			// after the claim committed. Only one that hangs past a
			// Shutdown deadline is cancelled, so Shutdown stays bounded.
			cctx, cancelClaim := w.cutAfter(ctx)
			jobs, err := w.claim(cctx, free)
			// A claim that a hard stop or the cut-off cancelled is no error.
			if err != nil && cctx.Err() == nil {
				w.Logger.Error("workflow worker: claim", "error", err)
			}
			cancelClaim()
			jobs = w.admit(jobs)
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

// Shutdown stops the worker gracefully, like http.Server.Shutdown. It stops
// claiming at once, steps and timers alike, for every Run call on the
// worker. Active jobs keep running, with their heartbeats, until ctx is
// done; then their contexts are cancelled, and a job whose handler returns
// the context's error or a retryable error is given back without counting
// toward MaxClaims. After that point each write takes at most a second, and
// a result that cannot be recorded is given back.
//
// Shutdown returns once every handler has returned and every claim is
// settled or given back, and Run has returned nil. It waits without limit
// for a handler that ignores its context. It returns nil if every active
// job finished before ctx was done, else ctx's error. If a Run call
// returned with its context done, at any time in the worker's life, that
// Run stopped hard, and Shutdown returns ErrHardStop (joined with ctx's
// error if both apply), even with no Run in progress. A worker that was
// shut down does not start again.
func (w *Worker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	w.defaults()
	first := !w.isStopped()
	if first {
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
		err = errors.Join(ErrHardStop, err)
	}
	return err
}

// Active returns the number of active jobs: jobs the worker has claimed and
// not yet settled or given back, across all Run calls. A claim query in
// progress counts as one, so once the claim gate is closed (Shutdown, or
// MayClaim returning false), Active() == 0 means the worker holds no claim.
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
// counting them as active. If the gate closed while the claim query ran, it
// gives them back unrun.
func (w *Worker) admit(jobs []claimedJob) []claimedJob {
	if len(jobs) > 0 && !w.mayClaim() {
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

// listen wakes the claim loop on each notification, on its own connection
// so it never holds one of the pool's. It reconnects after an error and
// closes the connection when ctx is done.
func (w *Worker) listen(ctx context.Context, notify func()) {
	cfg := w.ListenConfig
	if cfg == nil {
		cfg = w.Store.pool.Config().ConnConfig
	}
	for ctx.Err() == nil {
		conn, err := pgx.ConnectConfig(ctx, cfg.Copy())
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+pgx.Identifier{w.Store.NotifyChannel()}.Sanitize())
			if err == nil {
				notify() // catch up on what was written while not listening
			}
			for err == nil {
				if _, err = conn.WaitForNotification(ctx); err == nil {
					notify()
				}
			}
			cctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Close(cctx)
			cancel()
		}
		if ctx.Err() == nil {
			w.Logger.Warn("workflow worker: listen; polling until reconnected", "error", err)
			sleep(ctx, w.Poll)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
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

// claimSQL leases due jobs: $1 limit, $2 worker, $3 lease seconds,
// $4 queues.
const claimSQL = `
		WITH due AS (
		    SELECT d.id FROM unnest($4::text[]) AS q(queue)
		    CROSS JOIN LATERAL (
		        SELECT id, run_at FROM {{schema}}.jobs
		        WHERE queue = q.queue AND run_at <= now()
		          AND (lease_expires IS NULL OR lease_expires < now())
		        ORDER BY run_at, id
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED
		    ) d
		    ORDER BY d.run_at, d.id
		    LIMIT $1
		)
		UPDATE {{schema}}.jobs t
		SET lease_owner = $2, lease_expires = now() + make_interval(secs => $3),
		    claims = t.claims + 1, fence = t.fence + 1
		FROM due, {{schema}}.runs r
		WHERE t.id = due.id AND r.id = t.run_id
		RETURNING t.id, t.run_id, t.command, t.claims, t.fence, r.definition_hash`

// claim leases up to limit due jobs. It takes the earliest of each queue
// through the jobs_claim index, then the earliest of those.
func (w *Worker) claim(ctx context.Context, limit int) ([]claimedJob, error) {
	claimed := time.Now() // no later than the database's now()
	rows, err := w.Store.pool.Query(ctx, w.Store.sql(claimSQL),
		limit, w.ID, w.Lease.Seconds(), w.queues())
	var jobs []claimedJob
	if err == nil {
		if w.claimHook != nil {
			w.claimHook(ctx)
		}
		jobs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (claimedJob, error) {
			var t claimedJob
			err := row.Scan(&t.ID, &t.RunID, &t.Command, &t.Claims, &t.fence, &t.defHash)
			t.claimed = claimed
			return t, err
		})
	}
	if err != nil && ctx.Err() == nil && w.Observer.ClaimError != nil {
		w.Observer.ClaimError(err)
	}
	return jobs, err
}

func (w *Worker) process(ctx context.Context, t claimedJob) {
	log := w.Logger.With("run", t.RunID, "step_id", t.StepID)
	w.observeClaimed(t)
	// Nothing runs for a run this store cannot advance.
	if _, err := w.Store.definition(ctx, w.Store.pool, t.defHash); err != nil {
		w.unloadable(ctx, t, err, log)
		return
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
		w.abort(ctx, t, abandoned("timer"), log)
		return
	case t.Kind == workflow.CommandScheduleTimer:
		w.settle(ctx, t, t.claimed, workflow.Event{
			Kind:   workflow.EventTimerFired,
			StepID: t.StepID,
		}, log)
		return
	case t.Claims > w.MaxClaims+1:
		// Not even the abandoned failure below could be applied.
		log.Error("workflow worker: step could not be failed; failing the run",
			"claims", t.Claims-1)
		w.abort(ctx, t, abandoned("step"), log)
		return
	case t.Claims > w.MaxClaims:
		w.settle(ctx, t, t.claimed, workflow.Event{
			Kind:   workflow.EventStepFailed,
			StepID: t.StepID,
			Error: &workflow.StepError{
				Type:    "abandoned",
				Message: fmt.Sprintf("claimed %d times without finishing", t.Claims-1),
			},
		}, log)
		return
	}

	stepCtx, cancel := context.WithCancel(ctx)
	lost := make(chan struct{})
	beat := make(chan struct{})
	var (
		renewed time.Time // when the lease was last extended
		lostErr error     // why the claim was lost
	)
	go func() {
		defer close(beat)
		renewed = w.heartbeat(stepCtx, t, func(err error) {
			lostErr = err
			close(lost)
			cancel()
		})
	}()
	start := time.Now()
	output, err := w.call(stepCtx, t)
	ran := time.Since(start)
	cancel()
	<-beat
	t.ran, t.renewed = ran, renewed // the heartbeat goroutine reads t until it is done
	if w.handlerDone != nil {
		w.handlerDone(t, output, err)
	}

	select {
	case <-lost:
		log.Info("workflow worker: dropping result; the job was cancelled or reclaimed")
		if errors.Is(err, workflow.ErrDeferred) && errors.Is(lostErr, errJobGone) {
			w.observeDeferred(t) // handed off, and its result may already be in
			return
		}
		w.observeDropped(t, lostErr)
		return
	default:
	}
	if ctx.Err() != nil && interrupted(err) {
		// Shutting down, and the error may be only the cancellation: give
		// the claim back so another worker runs the step. A result, a
		// hand-off or a permanent failure is recorded below, because that
		// work is done and running it again would repeat it.
		w.release(t)
		return
	}
	ev := workflow.Event{StepID: t.StepID}
	switch {
	case errors.Is(err, workflow.ErrDeferred):
		// Past the lease, another worker may claim the job anyway.
		fctx, cancel := w.bound(ctx, renewed.Add(w.Lease))
		defer cancel()
		w.finishDeferred(fctx, t, log)
		return
	case err != nil:
		ev.Kind, ev.Error = workflow.EventStepFailed, workflow.StepErrorOf(err)
	default:
		ev.Kind, ev.Output = workflow.EventStepCompleted, output
	}
	w.settle(ctx, t, renewed, ev, log)
}

// unloadable handles a job whose run's definition the store cannot load.
// A database error is left to the lease. A definition that does not load
// is a configuration problem, perhaps a deploy that has not reached every
// instance: the job waits a lease, doubling each time, and once it has
// been claimed MaxClaims times the run fails with
// ErrorTypeDefinitionUnavailable.
func (w *Worker) unloadable(ctx context.Context, t claimedJob, err error, log *slog.Logger) {
	if !errors.Is(err, ErrDefinitionUnavailable) {
		if ctx.Err() != nil {
			w.release(t) // shutting down before anything ran
			return
		}
		log.Error("workflow worker: load definition", "error", err)
		w.observeDropped(t, err) // left to its lease
		return
	}
	if t.Claims >= w.MaxClaims {
		log.Error("workflow worker: failing the run; its definition cannot be loaded", "error", err)
		w.abort(ctx, t, &workflow.RunError{
			Type:    ErrorTypeDefinitionUnavailable,
			Message: err.Error(),
		}, log)
		return
	}
	delay := w.Lease
	for i := 1; i < t.Claims && delay < time.Hour; i++ {
		delay *= 2
	}
	log.Error("workflow worker: the run's definition cannot be loaded; check the store's kinds",
		"error", err, "claims", t.Claims, "max_claims", w.MaxClaims, "retry_in", delay)
	pctx, cancel := w.bound(ctx, time.Now().Add(giveBackTimeout))
	defer cancel()
	if _, err := w.Store.pool.Exec(pctx, w.Store.sql(`
		UPDATE {{schema}}.jobs
		SET lease_owner = NULL, lease_expires = NULL, run_at = now() + make_interval(secs => $3)
		WHERE id = $1 AND fence = $2`), t.ID, t.fence, delay.Seconds()); err != nil {
		log.Warn("workflow worker: postpone job", "error", err)
	}
}

// abort fails the job's run without applying an event, fenced by the
// claim, within the claim's lease.
func (w *Worker) abort(
	ctx context.Context, t claimedJob, runErr *workflow.RunError, log *slog.Logger,
) {
	actx, cancel := w.bound(ctx, t.claimed.Add(w.Lease))
	defer cancel()
	err := pgx.BeginFunc(actx, w.Store.pool, func(tx pgx.Tx) error {
		st, _, err := w.Store.lockRun(actx, tx, t.RunID)
		if err != nil {
			return err
		}
		if err := w.deleteJob(actx, tx, t); err != nil {
			return err
		}
		return w.Store.abort(actx, tx, t.RunID, st, runErr, time.Now().UTC())
	})
	if err == nil {
		w.observeAbandoned(t)
		return
	}
	if !errors.Is(err, errLeaseLost) && !errors.Is(err, ErrRunNotFound) {
		log.Error("workflow worker: fail run", "error", err)
	}
	w.observeDropped(t, err)
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

func (w *Worker) call(ctx context.Context, t claimedJob) (output any, err error) {
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

// heartbeat extends the lease until ctx is done. It calls lost when the
// job is gone (errJobGone), another worker has claimed it, or the lease has
// run out without a successful heartbeat (another worker may claim it now).
// It returns when the lease was last extended.
func (w *Worker) heartbeat(ctx context.Context, t claimedJob, lost func(error)) time.Time {
	tick := time.NewTicker(w.Lease / 3)
	defer tick.Stop()
	lastOK := t.claimed

	for {
		select {
		case <-ctx.Done():
			return lastOK
		case <-tick.C:
		}

		var (
			tag pgconn.CommandTag
			err error
		)
		if w.heartbeatHook != nil {
			err = w.heartbeatHook()
		}
		attempt := time.Now()
		if err == nil {
			tag, err = w.Store.pool.Exec(ctx, w.Store.sql(`
				UPDATE {{schema}}.jobs SET lease_expires = now() + make_interval(secs => $3)
				WHERE id = $1 AND fence = $2`), t.ID, t.fence, w.Lease.Seconds())
		}
		if err != nil {
			if ctx.Err() != nil {
				return lastOK
			}
			if time.Since(lastOK) >= w.Lease {
				w.Logger.Warn("workflow worker: lease expired without a heartbeat; stopping the step",
					"run", t.RunID, "step_id", t.StepID, "error", err)
				lost(errLeaseLost)
				return lastOK
			}
			continue // transient; the lease has slack
		}
		if tag.RowsAffected() == 0 {
			lost(w.lostClaim(ctx, w.Store.pool, t, attempt.Before(lastOK.Add(w.Lease))))
			return lastOK
		}
		lastOK = attempt // the database's now() is no earlier
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
	parent context.Context, t claimedJob, renewed time.Time, ev workflow.Event, log *slog.Logger,
) {
	ctx, cancel := w.bound(parent, renewed.Add(w.Lease))
	defer cancel()
	backoff := 50 * time.Millisecond
	ev = storable(ev) // once, so Settled reports the event the store records

	for {
		rejected, err := w.settleOnce(ctx, t, ev, log)
		switch {
		case err == nil && rejected:
			w.observeCancelled(t)
			return
		case err == nil:
			w.observeSettled(t, ev)
			return
		case errors.Is(err, errLeaseLost):
			log.Info("workflow worker: dropping result; the job was cancelled or reclaimed")
			w.observeDropped(t, err)
			return
		case errors.Is(err, ErrRunNotFound):
			log.Warn("workflow worker: run no longer exists")
			w.observeDropped(t, err)
			return
		case errors.Is(err, ErrDefinitionUnavailable):
			// The next claim checks the definition before anything runs.
			log.Error("workflow worker: deliver result", "error", err)
			w.observeDropped(t, err) // left to its lease
			return
		case ev.Kind == workflow.EventStepCompleted && isUnstorable(err):
			// Postgres rejected what the result made of the run. The work
			// is done and running it again would end the same way: record
			// the step as failed instead.
			log.Warn("workflow worker: result cannot be stored; failing the step", "error", err)
			ev = storable(invalidOutput(ev, "the run cannot be stored with this output: "+err.Error()))
			continue
		}

		if t.Kind == workflow.CommandScheduleTimer && (parent.Err() != nil || w.cutDone()) {
			log.Warn("workflow worker: shutting down before a timer fired; giving it back",
				"error", err)
			w.release(t)
			return
		}
		if w.cutDone() {
			log.Warn("workflow worker: could not deliver the result before the shutdown deadline; giving the job back",
				"error", err)
			w.release(t)
			return
		}
		if ctx.Err() != nil {
			log.Error("workflow worker: could not deliver the result before the lease ran out; the step may run again",
				"error", err)
			w.observeDropped(t, err)
			return
		}
		log.Warn("workflow worker: deliver result; retrying", "error", err)
		w.pause(ctx, backoff)
		backoff = min(backoff*2, w.Lease/3)
	}
}

// settleOnce reports rejected when the run will never accept the event: the
// job is deleted and nothing is recorded.
func (w *Worker) settleOnce(
	ctx context.Context, t claimedJob, ev workflow.Event, log *slog.Logger,
) (rejected bool, err error) {
	if w.settleHook != nil {
		if err := w.settleHook(t); err != nil {
			return false, err
		}
	}
	err = pgx.BeginFunc(ctx, w.Store.pool, func(tx pgx.Tx) error {
		// Lock the run before the job, the same order DeliverTx uses
		// when it deletes jobs, so the two cannot deadlock.
		st, hash, err := w.Store.lockRun(ctx, tx, t.RunID)
		if err != nil {
			return err
		}
		if err := w.deleteJob(ctx, tx, t); err != nil {
			return err
		}
		_, err = w.Store.deliverLocked(ctx, tx, t.RunID, st, hash, ev)
		if isPermanent(err) {
			// The run will never accept this event; keep the job
			// deletion so it does not come back.
			log.Warn("workflow worker: run rejected event", "error", err)
			rejected = true
			return nil
		}
		return err
	})
	return rejected, err
}

// finishDeferred marks a handed-off job done. It retries until the delete
// succeeds, because a job left claimed would run its handler again when the
// lease expires: a second agent turn, a second approval request.
func (w *Worker) finishDeferred(ctx context.Context, t claimedJob, log *slog.Logger) {
	backoff := 50 * time.Millisecond
	for {
		err := w.finish(ctx, t)
		if err == nil || errors.Is(err, errJobGone) {
			// A gone job was settled by Store.Deliver, or the run no longer
			// wants it; either way the hand-off happened and nothing runs
			// again.
			w.observeDeferred(t)
			return
		}
		if errors.Is(err, errLeaseLost) {
			w.observeDropped(t, err)
			return
		}
		if ctx.Err() != nil || w.cutDone() {
			// Not given back: that would run the handler again at once.
			log.Error("workflow worker: stopped before marking a deferred job done; it may run again",
				"error", err)
			w.observeDropped(t, err)
			return
		}
		log.Warn("workflow worker: finish deferred job; retrying", "error", err)
		w.pause(ctx, backoff)
		backoff = min(backoff*2, w.Lease/3)
	}
}

func (w *Worker) finish(ctx context.Context, t claimedJob) error {
	if w.finishHook != nil {
		if err := w.finishHook(); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, w.Store.pool, func(tx pgx.Tx) error {
		return w.deleteJob(ctx, tx, t)
	})
}

// deleteJob removes claim t's job. Its callers bound ctx by the claim's
// lease, so a delete that starts at all starts while the lease is live.
func (w *Worker) deleteJob(ctx context.Context, tx pgx.Tx, t claimedJob) error {
	start := time.Now()
	tag, err := tx.Exec(ctx,
		w.Store.sql(`DELETE FROM {{schema}}.jobs WHERE id = $1 AND fence = $2`), t.ID, t.fence)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		end, ok := ctx.Deadline()
		return w.lostClaim(ctx, tx, t, ok && start.Before(end))
	}
	return nil
}

// release gives a claim back on shutdown so another worker can take it
// without waiting for the lease to expire. A graceful release does not count
// toward MaxClaims, so it refunds the claim; the fence stays, so this claim
// stays fenced out.
//
// It takes giveBackTimeout, and during Shutdown no longer than a write
// after the deadline (see cutAfter).
func (w *Worker) release(t claimedJob) {
	ctx, cancel := context.WithTimeout(context.Background(), giveBackTimeout)
	defer cancel()
	ctx, cancelCut := w.cutAfter(ctx)
	defer cancelCut()
	renewed := t.claimed
	if t.renewed.After(renewed) {
		renewed = t.renewed
	}
	if w.releaseHook != nil {
		w.releaseHook(ctx)
	}
	start := time.Now()
	tag, err := w.Store.pool.Exec(ctx, w.Store.sql(`
		UPDATE {{schema}}.jobs SET lease_owner = NULL, lease_expires = NULL, claims = claims - 1
		WHERE id = $1 AND fence = $2`), t.ID, t.fence)
	switch {
	case err != nil:
		w.observeDropped(t, err) // left to its lease
	case tag.RowsAffected() == 0:
		live := start.Before(renewed.Add(w.Lease))
		w.observeDropped(t, w.lostClaim(ctx, w.Store.pool, t, live))
	default:
		if f := w.Observer.GivenBack; f != nil {
			f(t.Job)
		}
	}
}

// isUnstorable reports whether Postgres rejected a value: a data exception
// (class 22, such as an invalid jsonb escape) or a program limit (class 54,
// such as a jsonb too large).
func isUnstorable(err error) bool {
	pe, ok := errors.AsType[*pgconn.PgError](err)
	return ok && (strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "54"))
}

func isPermanent(err error) bool {
	for _, target := range []error{
		workflow.ErrUnknownStep, workflow.ErrInvalidEvent, workflow.ErrNotStarted,
		workflow.ErrDefinitionMismatch, workflow.ErrNotValidated,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
