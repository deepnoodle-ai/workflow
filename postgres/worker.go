package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
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
	ID     int64
	RunID  string
	Claims int // how many times the task has been claimed, including this one
	workflow.Command
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
	// Default 5.
	MaxClaims int
	Logger    *slog.Logger
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
	go w.listen(ctx, notify)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		running int
	)
	defer wg.Wait()
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

func (w *Worker) listen(ctx context.Context, notify func()) {
	for ctx.Err() == nil {
		conn, err := w.Store.pool.Acquire(ctx)
		if err != nil {
			time.Sleep(w.Poll)
			continue
		}
		_, err = conn.Exec(ctx, fmt.Sprintf(`LISTEN %q`, w.Store.NotifyChannel()))
		for err == nil {
			_, err = conn.Conn().WaitForNotification(ctx)
			if err == nil {
				notify()
			}
		}
		conn.Release()
		if ctx.Err() == nil {
			time.Sleep(w.Poll)
		}
	}
}

func (w *Worker) claim(ctx context.Context, limit int) ([]Task, error) {
	rows, err := w.Store.pool.Query(ctx, w.Store.sql(`
		WITH due AS (
		    SELECT id FROM {{schema}}.tasks
		    WHERE run_at <= now() AND (lease_expires IS NULL OR lease_expires < now())
		    ORDER BY run_at, id
		    LIMIT $1
		    FOR UPDATE SKIP LOCKED
		)
		UPDATE {{schema}}.tasks t
		SET lease_owner = $2, lease_expires = now() + make_interval(secs => $3), claims = t.claims + 1
		FROM due WHERE t.id = due.id
		RETURNING t.id, t.run_id, t.command, t.claims`),
		limit, w.ID, w.Lease.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Task, error) {
		var t Task
		err := row.Scan(&t.ID, &t.RunID, &t.Command, &t.Claims)
		return t, err
	})
}

func (w *Worker) process(ctx context.Context, t Task) {
	log := w.Logger.With("run", t.RunID, "step_id", t.StepID)
	if t.Kind == workflow.CommandScheduleTimer {
		w.settle(ctx, t, workflow.Event{Kind: workflow.EventTimerFired, StepID: t.StepID}, log)
		return
	}
	if t.Claims > w.MaxClaims {
		w.settle(ctx, t, workflow.Event{Kind: workflow.EventStepFailed, StepID: t.StepID, Error: &workflow.StepError{
			Type: "abandoned", Message: fmt.Sprintf("claimed %d times without finishing", t.Claims-1),
		}}, log)
		return
	}

	stepCtx, cancel := context.WithCancel(ctx)
	lost := make(chan struct{})
	beat := make(chan struct{})
	go func() {
		defer close(beat)
		w.heartbeat(stepCtx, t, func() { close(lost); cancel() })
	}()
	output, err := w.call(stepCtx, t)
	cancel()
	<-beat

	select {
	case <-lost:
		log.Info("workflow worker: dropping result; the task was cancelled or reclaimed")
		return
	default:
	}
	if ctx.Err() != nil {
		w.release(t)
		return
	}
	ev := workflow.Event{StepID: t.StepID}
	switch {
	case errors.Is(err, ErrDeferred):
		if err := w.finish(context.WithoutCancel(ctx), t); err != nil && !errors.Is(err, errLeaseLost) {
			log.Error("workflow worker: finish deferred task", "error", err)
		}
		return
	case err != nil:
		ev.Kind, ev.Error = workflow.EventStepFailed, toStepError(err)
	default:
		ev.Kind, ev.Output = workflow.EventStepCompleted, output
	}
	w.settle(context.WithoutCancel(ctx), t, ev, log)
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

// heartbeat extends the lease until ctx is done. If the task is gone or
// claimed by someone else, it calls lost.
func (w *Worker) heartbeat(ctx context.Context, t Task, lost func()) {
	tick := time.NewTicker(w.Lease / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		tag, err := w.Store.pool.Exec(ctx, w.Store.sql(`
			UPDATE {{schema}}.tasks SET lease_expires = now() + make_interval(secs => $3)
			WHERE id = $1 AND claims = $2`), t.ID, t.Claims, w.Lease.Seconds())
		if err != nil {
			continue // transient; the lease has slack
		}
		if tag.RowsAffected() == 0 {
			lost()
			return
		}
	}
}

// settle delivers a task's result and removes the task in one transaction.
// The claim count fences out a worker whose lease expired.
func (w *Worker) settle(ctx context.Context, t Task, ev workflow.Event, log *slog.Logger) {
	err := pgx.BeginFunc(ctx, w.Store.pool, func(tx pgx.Tx) error {
		// Lock the run before the task, the same order DeliverTx uses
		// when it cancels tasks, so the two cannot deadlock.
		if _, _, err := w.Store.lockRun(ctx, tx, t.RunID); err != nil {
			return err
		}
		if err := w.deleteTask(ctx, tx, t); err != nil {
			return err
		}
		_, _, err := w.Store.DeliverTx(ctx, tx, t.RunID, ev)
		if isPermanent(err) {
			// The run will never accept this event; keep the task
			// deletion so it does not come back.
			log.Warn("workflow worker: run rejected event", "error", err)
			return nil
		}
		return err
	})
	switch {
	case err == nil:
	case errors.Is(err, errLeaseLost):
		log.Info("workflow worker: dropping result; the task was cancelled or reclaimed")
	case errors.Is(err, ErrRunNotFound):
		log.Warn("workflow worker: run no longer exists")
	default:
		// The lease will expire and another claim will retry.
		log.Error("workflow worker: deliver result", "error", err)
	}
}

func (w *Worker) finish(ctx context.Context, t Task) error {
	return pgx.BeginFunc(ctx, w.Store.pool, func(tx pgx.Tx) error { return w.deleteTask(ctx, tx, t) })
}

func (w *Worker) deleteTask(ctx context.Context, tx pgx.Tx, t Task) error {
	tag, err := tx.Exec(ctx, w.Store.sql(`DELETE FROM {{schema}}.tasks WHERE id = $1 AND claims = $2`), t.ID, t.Claims)
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
// toward MaxClaims. This worker will not settle the task, so the claim count
// it rolls back cannot fence anyone.
func (w *Worker) release(t Task) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = w.Store.pool.Exec(ctx, w.Store.sql(`
		UPDATE {{schema}}.tasks SET lease_owner = NULL, lease_expires = NULL, claims = claims - 1
		WHERE id = $1 AND claims = $2`), t.ID, t.Claims)
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
