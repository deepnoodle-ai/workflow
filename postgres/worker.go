package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrDeferred tells the worker that a handler handed the step off and its
// result will arrive later through [Store.Deliver]: an agent turn on another
// service, a human approval, a webhook. The task is marked done; the step
// stays in flight in the run until the result is delivered or its timeout
// fires.
var ErrDeferred = errors.New("postgres: step completes later through Deliver")

var errLeaseLost = errors.New("postgres: lease lost")

// Task is one claimed unit of work: a step attempt to run.
type Task struct {
	ID    int64
	RunID string
	// Claims is how many times the task has been claimed, including this
	// one. A claim given back on graceful shutdown does not count.
	Claims int
	workflow.Command

	fence   int64     // this claim's fencing token
	claimed time.Time // when the claim was made; its lease runs from here
	defHash string    // the run's definition
}

// Handler runs one step attempt and returns its output. Return a
// *[workflow.StepError] to set the error type and whether it is retryable;
// any other error is reported as retryable type "error". Return ErrDeferred
// when the result will be delivered later.
//
// A task can run more than once if a worker dies or loses its lease, so
// handlers must be idempotent by Task.StepID. ctx is cancelled when the
// attempt is no longer wanted.
type Handler func(ctx context.Context, t Task) (any, error)

// Worker claims tasks from the outbox and runs them.
type Worker struct {
	Store    *Store
	Handlers map[string]Handler // by step kind
	// ID identifies the worker in lease_owner. Default: random.
	ID string
	// Concurrency caps the tasks run at once. Default 8.
	Concurrency int
	// Lease is how long a claim lasts without a heartbeat. Default 30s.
	Lease time.Duration
	// Poll is how often to look for due tasks without a notification.
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
	// as tasks are written. It is a dedicated connection, not one from the
	// pool, closed when Run returns. Default: the pool's connection config.
	// LISTEN needs a session, so when the pool goes through a
	// transaction-mode pooler (Neon's -pooler endpoint, PgBouncer), point
	// this at the direct endpoint; otherwise the worker still works, but
	// only wakes every Poll.
	ListenConfig *pgx.ConnConfig

	finishHook    func() error           // tests: fail finishing a deferred task
	heartbeatHook func() error           // tests: fail heartbeats
	settleHook    func(Task) error       // tests: fail settling
	handlerDone   func(Task, any, error) // tests: observe a handler's return
}

func (w *Worker) defaults() {
	if w.ID == "" {
		w.ID = "worker-" + newID()[:8]
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

// Run claims and runs tasks until ctx is done. On return every handler has
// finished and unfinished claims are released.
func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.listen(ctx, notify)
	}()
	for {
		mu.Lock()
		free := w.Concurrency - running
		mu.Unlock()
		if free > 0 {
			tasks, err := w.claim(ctx, free)
			if err != nil && ctx.Err() == nil {
				w.Logger.Error("workflow worker: claim", "error", err)
			}
			for _, t := range tasks {
				mu.Lock()
				running++
				mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					w.process(ctx, t)
					mu.Lock()
					running--
					mu.Unlock()
					notify()
				}()
			}
			if len(tasks) == free {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(w.Poll):
		}
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

// kinds are the step kinds this worker claims: the ones it has handlers
// for. Workers with different handlers can share one outbox.
func (w *Worker) kinds() []string {
	out := make([]string, 0, len(w.Handlers))
	for k := range w.Handlers {
		out = append(out, k)
	}
	return out
}

func (w *Worker) claim(ctx context.Context, limit int) ([]Task, error) {
	claimed := time.Now() // no later than the database's now()
	rows, err := w.Store.pool.Query(ctx, w.Store.sql(`
		WITH due AS (
		    SELECT id FROM {{schema}}.tasks
		    WHERE run_at <= now() AND (lease_expires IS NULL OR lease_expires < now())
		      AND (kind = 'timer' OR step_kind = ANY($4))
		    ORDER BY run_at, id
		    LIMIT $1
		    FOR UPDATE SKIP LOCKED
		)
		UPDATE {{schema}}.tasks t
		SET lease_owner = $2, lease_expires = now() + make_interval(secs => $3),
		    claims = t.claims + 1, fence = t.fence + 1
		FROM due, {{schema}}.runs r
		WHERE t.id = due.id AND r.id = t.run_id
		RETURNING t.id, t.run_id, t.command, t.claims, t.fence, r.definition_hash`),
		limit, w.ID, w.Lease.Seconds(), w.kinds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Task, error) {
		var t Task
		err := row.Scan(&t.ID, &t.RunID, &t.Command, &t.Claims, &t.fence, &t.defHash)
		t.claimed = claimed
		return t, err
	})
}

func (w *Worker) process(ctx context.Context, t Task) {
	log := w.Logger.With("run", t.RunID, "step_id", t.StepID)
	// Nothing runs for a run this store cannot advance.
	if _, err := w.Store.definition(ctx, w.Store.pool, t.defHash); err != nil {
		w.unloadable(ctx, t, err, log)
		return
	}
	abandoned := func(what string) *workflow.RunError {
		return &workflow.RunError{Step: t.Step, StepID: t.StepID, Type: "abandoned",
			Message: fmt.Sprintf("%s claimed %d times without finishing", what, t.Claims-1)}
	}
	switch {
	case t.Kind == workflow.CommandScheduleTimer && t.Claims > w.MaxClaims:
		// A timer that cannot fire leaves nothing to fail but the run.
		log.Error("workflow worker: timer could not be fired; failing the run", "claims", t.Claims-1)
		w.abort(ctx, t, abandoned("timer"), log)
		return
	case t.Kind == workflow.CommandScheduleTimer:
		w.settle(ctx, t, t.claimed, workflow.Event{Kind: workflow.EventTimerFired, StepID: t.StepID}, log)
		return
	case t.Claims > w.MaxClaims+1:
		// Not even the abandoned failure below could be applied.
		log.Error("workflow worker: step could not be failed; failing the run", "claims", t.Claims-1)
		w.abort(ctx, t, abandoned("step"), log)
		return
	case t.Claims > w.MaxClaims:
		w.settle(ctx, t, t.claimed, workflow.Event{Kind: workflow.EventStepFailed, StepID: t.StepID, Error: &workflow.StepError{
			Type: "abandoned", Message: fmt.Sprintf("claimed %d times without finishing", t.Claims-1),
		}}, log)
		return
	}

	stepCtx, cancel := context.WithCancel(ctx)
	lost := make(chan struct{})
	beat := make(chan struct{})
	var renewed time.Time // when the lease was last extended
	go func() {
		defer close(beat)
		renewed = w.heartbeat(stepCtx, t, func() { close(lost); cancel() })
	}()
	output, err := w.call(stepCtx, t)
	cancel()
	<-beat
	if w.handlerDone != nil {
		w.handlerDone(t, output, err)
	}

	select {
	case <-lost:
		log.Info("workflow worker: dropping result; the task was cancelled or reclaimed")
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
	case errors.Is(err, ErrDeferred):
		// Past the lease, another worker may claim the task anyway.
		fctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), renewed.Add(w.Lease))
		defer cancel()
		w.finishDeferred(fctx, t, log)
		return
	case err != nil:
		ev.Kind, ev.Error = workflow.EventStepFailed, toStepError(err)
	default:
		ev.Kind, ev.Output = workflow.EventStepCompleted, output
	}
	w.settle(ctx, t, renewed, ev, log)
}

// unloadable handles a task whose run's definition the store cannot load.
// A database error is left to the lease. A definition that does not load
// is a configuration problem, perhaps a deploy that has not reached every
// instance: the task waits a lease, doubling each time, and once it has
// been claimed MaxClaims times the run fails with
// ErrorTypeDefinitionUnavailable.
func (w *Worker) unloadable(ctx context.Context, t Task, err error, log *slog.Logger) {
	if !errors.Is(err, ErrDefinitionUnavailable) {
		log.Error("workflow worker: load definition", "error", err)
		return
	}
	if t.Claims >= w.MaxClaims {
		log.Error("workflow worker: failing the run; its definition cannot be loaded", "error", err)
		w.abort(ctx, t, &workflow.RunError{Type: ErrorTypeDefinitionUnavailable, Message: err.Error()}, log)
		return
	}
	delay := w.Lease
	for i := 1; i < t.Claims && delay < time.Hour; i++ {
		delay *= 2
	}
	log.Error("workflow worker: the run's definition cannot be loaded; check the store's kinds",
		"error", err, "claims", t.Claims, "max_claims", w.MaxClaims, "retry_in", delay)
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := w.Store.pool.Exec(pctx, w.Store.sql(`
		UPDATE {{schema}}.tasks
		SET lease_owner = NULL, lease_expires = NULL, run_at = now() + make_interval(secs => $3)
		WHERE id = $1 AND fence = $2`), t.ID, t.fence, delay.Seconds()); err != nil {
		log.Warn("workflow worker: postpone task", "error", err)
	}
}

// abort fails the task's run without applying an event, fenced by the
// claim, within the claim's lease.
func (w *Worker) abort(ctx context.Context, t Task, runErr *workflow.RunError, log *slog.Logger) {
	actx, cancel := context.WithDeadline(context.WithoutCancel(ctx), t.claimed.Add(w.Lease))
	defer cancel()
	err := pgx.BeginFunc(actx, w.Store.pool, func(tx pgx.Tx) error {
		st, _, err := w.Store.lockRun(actx, tx, t.RunID)
		if err != nil {
			return err
		}
		if err := w.deleteTask(actx, tx, t); err != nil {
			return err
		}
		return w.Store.abort(actx, tx, t.RunID, st, runErr, time.Now().UTC())
	})
	if err != nil && !errors.Is(err, errLeaseLost) && !errors.Is(err, ErrRunNotFound) {
		log.Error("workflow worker: fail run", "error", err)
	}
}

// interrupted reports whether a handler's error, returned while the worker
// shuts down, may be due to the shutdown: the context's own error, or any
// retryable error (a cancelled request often surfaces as one). Running such
// an attempt again is safe. A permanent failure is a real result.
func interrupted(err error) bool {
	switch {
	case err == nil, errors.Is(err, ErrDeferred):
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return true
	}
	return toStepError(err).Retryable
}

func (w *Worker) call(ctx context.Context, t Task) (output any, err error) {
	h := w.Handlers[t.StepKind]
	if h == nil {
		return nil, &workflow.StepError{Type: "unknown_kind", Message: fmt.Sprintf("no handler for step kind %q", t.StepKind)}
	}
	defer func() {
		if p := recover(); p != nil {
			err = &workflow.StepError{Type: "panic", Message: fmt.Sprint(p)}
		}
	}()
	return h(ctx, t)
}

// heartbeat extends the lease until ctx is done. It calls lost when the
// task is gone, another worker has claimed it, or the lease has run out
// without a successful heartbeat (another worker may claim it now). It
// returns when the lease was last extended.
func (w *Worker) heartbeat(ctx context.Context, t Task, lost func()) time.Time {
	tick := time.NewTicker(w.Lease / 3)
	defer tick.Stop()
	lastOK := t.claimed
	for {
		select {
		case <-ctx.Done():
			return lastOK
		case <-tick.C:
		}
		attempt := time.Now()
		var (
			tag pgconn.CommandTag
			err error
		)
		if w.heartbeatHook != nil {
			err = w.heartbeatHook()
		}
		if err == nil {
			tag, err = w.Store.pool.Exec(ctx, w.Store.sql(`
				UPDATE {{schema}}.tasks SET lease_expires = now() + make_interval(secs => $3)
				WHERE id = $1 AND fence = $2`), t.ID, t.fence, w.Lease.Seconds())
		}
		if err != nil {
			if ctx.Err() != nil {
				return lastOK
			}
			if time.Since(lastOK) >= w.Lease {
				w.Logger.Warn("workflow worker: lease expired without a heartbeat; stopping the step",
					"run", t.RunID, "step_id", t.StepID, "error", err)
				lost()
				return lastOK
			}
			continue // transient; the lease has slack
		}
		if tag.RowsAffected() == 0 {
			lost()
			return lastOK
		}
		lastOK = attempt // the database's now() is no earlier
	}
}

// settle delivers a task's result and removes the task in one transaction.
// The fence shuts out a worker whose task another worker has claimed since.
//
// It retries transient errors with backoff until the lease that started at
// renewed runs out: past it another worker may claim the task, and a step
// result that was not delivered is then lost and its attempt runs again.
// So settling is bounded by the lease even during shutdown. A timer whose
// settle fails during shutdown is given back at once; firing it is cheap
// and safe to repeat.
func (w *Worker) settle(parent context.Context, t Task, renewed time.Time, ev workflow.Event, log *slog.Logger) {
	ctx, cancel := context.WithDeadline(context.WithoutCancel(parent), renewed.Add(w.Lease))
	defer cancel()
	backoff := 50 * time.Millisecond
	for {
		err := w.settleOnce(ctx, t, ev, log)
		switch {
		case err == nil:
			return
		case errors.Is(err, errLeaseLost):
			log.Info("workflow worker: dropping result; the task was cancelled or reclaimed")
			return
		case errors.Is(err, ErrRunNotFound):
			log.Warn("workflow worker: run no longer exists")
			return
		case errors.Is(err, ErrDefinitionUnavailable):
			// The next claim checks the definition before anything runs.
			log.Error("workflow worker: deliver result", "error", err)
			return
		case ev.Kind == workflow.EventStepCompleted && isUnstorable(err):
			// Postgres rejected what the result made of the run. The work
			// is done and running it again would end the same way: record
			// the step as failed instead.
			log.Warn("workflow worker: result cannot be stored; failing the step", "error", err)
			ev = invalidOutput(ev, "the run cannot be stored with this output: "+err.Error())
			continue
		}
		if t.Kind == workflow.CommandScheduleTimer && parent.Err() != nil {
			log.Warn("workflow worker: shutting down before a timer fired; giving it back", "error", err)
			w.release(t)
			return
		}
		if ctx.Err() != nil {
			log.Error("workflow worker: could not deliver the result before the lease ran out; the step may run again",
				"error", err)
			return
		}
		log.Warn("workflow worker: deliver result; retrying", "error", err)
		sleep(ctx, backoff)
		backoff = min(backoff*2, w.Lease/3)
	}
}

func (w *Worker) settleOnce(ctx context.Context, t Task, ev workflow.Event, log *slog.Logger) error {
	if w.settleHook != nil {
		if err := w.settleHook(t); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, w.Store.pool, func(tx pgx.Tx) error {
		// Lock the run before the task, the same order DeliverTx uses
		// when it deletes tasks, so the two cannot deadlock.
		st, hash, err := w.Store.lockRun(ctx, tx, t.RunID)
		if err != nil {
			return err
		}
		if err := w.deleteTask(ctx, tx, t); err != nil {
			return err
		}
		_, err = w.Store.deliverLocked(ctx, tx, t.RunID, st, hash, ev)
		if isPermanent(err) {
			// The run will never accept this event; keep the task
			// deletion so it does not come back.
			log.Warn("workflow worker: run rejected event", "error", err)
			return nil
		}
		return err
	})
}

// finishDeferred marks a handed-off task done. It retries until the delete
// succeeds, because a task left claimed would run its handler again when the
// lease expires: a second agent turn, a second approval request.
func (w *Worker) finishDeferred(ctx context.Context, t Task, log *slog.Logger) {
	backoff := 50 * time.Millisecond
	for {
		err := w.finish(ctx, t)
		if err == nil || errors.Is(err, errLeaseLost) {
			return
		}
		if ctx.Err() != nil {
			log.Error("workflow worker: stopped before marking a deferred task done; it may run again", "error", err)
			return
		}
		log.Warn("workflow worker: finish deferred task; retrying", "error", err)
		sleep(ctx, backoff)
		backoff = min(backoff*2, w.Lease/3)
	}
}

func (w *Worker) finish(ctx context.Context, t Task) error {
	if w.finishHook != nil {
		if err := w.finishHook(); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, w.Store.pool, func(tx pgx.Tx) error { return w.deleteTask(ctx, tx, t) })
}

func (w *Worker) deleteTask(ctx context.Context, tx pgx.Tx, t Task) error {
	tag, err := tx.Exec(ctx, w.Store.sql(`DELETE FROM {{schema}}.tasks WHERE id = $1 AND fence = $2`), t.ID, t.fence)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLeaseLost
	}
	return nil
}

// release gives a claim back on shutdown so another worker can take it
// without waiting for the lease to expire. A graceful release does not count
// toward MaxClaims, so it refunds the claim; the fence stays, so this claim
// stays fenced out.
func (w *Worker) release(t Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = w.Store.pool.Exec(ctx, w.Store.sql(`
		UPDATE {{schema}}.tasks SET lease_owner = NULL, lease_expires = NULL, claims = claims - 1
		WHERE id = $1 AND fence = $2`), t.ID, t.fence)
}

// isUnstorable reports whether Postgres rejected a value: a data exception
// (class 22, such as an invalid jsonb escape) or a program limit (class 54,
// such as a jsonb too large).
func isUnstorable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "54"))
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

func toStepError(err error) *workflow.StepError {
	var se *workflow.StepError
	if errors.As(err, &se) {
		return se
	}
	return &workflow.StepError{Type: "error", Message: err.Error(), Retryable: true}
}
