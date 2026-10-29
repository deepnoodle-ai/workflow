package postgres

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/jackc/pgx/v5"
)

// claimedJob is one claimed job, as the shared worker holds it.
type claimedJob = hostcore.Claimed

// Worker claims jobs from the outbox and runs them.
type Worker struct {
	Store    *Store
	Handlers map[string]workflow.Handler // by step kind
	// ID identifies the worker in lease_owner. Default: random.
	ID string
	// Concurrency caps the jobs run at once, in each Run or RunOnce call.
	// Execute is not limited by it. Default 8.
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

	once sync.Once
	core *hostcore.Worker // the worker policy, shared with every backend

	finishHook    func() error                 // tests: fail finishing a deferred job
	heartbeatHook func() error                 // tests: fail heartbeats
	settleHook    func(claimedJob) error       // tests: fail settling
	handlerDone   func(claimedJob, any, error) // tests: observe a handler's return
	claimHook     func(context.Context)        // tests: run once a claim query returns, before admit
	releaseHook   func(context.Context)        // tests: run before giving a claim back
}

// defaults builds the shared worker once, and syncs it with the exported
// fields (see sync).
func (w *Worker) defaults() {
	w.once.Do(func() {
		w.core = &hostcore.Worker{Prefix: "postgres", Wake: w.listen, Sync: w.sync}
	})
	w.core.Init()
}

// sync copies the exported fields and test hooks into the shared worker,
// then writes back the defaults it chose for fields that were zero. The
// core calls it at each entry point while no work is in progress, so a
// field set between calls takes effect at the next one, as if the worker
// read its fields on every call.
func (w *Worker) sync(c *hostcore.Worker, defaults func()) {
	c.Handlers = w.Handlers
	c.ID = w.ID
	c.Concurrency = w.Concurrency
	c.Lease = w.Lease
	c.Poll = w.Poll
	c.MaxClaims = w.MaxClaims
	c.Logger = w.Logger
	c.Observer = w.Observer
	c.MayClaim = w.MayClaim
	c.Hooks = hostcore.Hooks{
		Finish:      w.finishHook,
		Heartbeat:   w.heartbeatHook,
		Settle:      w.settleHook,
		HandlerDone: w.handlerDone,
		Claim:       w.claimHook,
		Release:     w.releaseHook,
	}
	c.Backend, c.Store = nil, nil
	if w.Store != nil {
		c.Backend, c.Store = w.Store.backend, w.Store.core
	}
	defaults()
	// Write only what changed: a caller may read these fields while the
	// worker runs.
	if w.ID != c.ID {
		w.ID = c.ID
	}
	if w.Concurrency != c.Concurrency {
		w.Concurrency = c.Concurrency
	}
	if w.Lease != c.Lease {
		w.Lease = c.Lease
	}
	if w.Poll != c.Poll {
		w.Poll = c.Poll
	}
	if w.MaxClaims != c.MaxClaims {
		w.MaxClaims = c.MaxClaims
	}
	if w.Logger != c.Logger {
		w.Logger = c.Logger
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
	w.defaults()
	return w.core.Run(ctx)
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
	w.defaults()
	return w.core.Shutdown(ctx)
}

// Active returns the number of active jobs: jobs the worker has claimed and
// not yet settled or given back, across all Run calls. A claim query in
// progress counts as one, so once the claim gate is closed (Shutdown, or
// MayClaim returning false), Active() == 0 means the worker holds no claim.
func (w *Worker) Active() int {
	w.defaults()
	return w.core.Active()
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
