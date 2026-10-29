package sqlite

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// Worker claims jobs from the outbox and runs them. Its fields, defaults
// and behaviour are the postgres module's Worker's, except that it wakes
// at once only for jobs written through the same Store; jobs another
// process writes wait up to Poll.
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
	// Poll is how often to look for due jobs without a wake-up. Default 1s.
	Poll time.Duration
	// MaxClaims fails a step that has been claimed this many times without
	// finishing, so a step that crashes its worker cannot loop forever.
	// It bounds timers too, and how long a run whose definition cannot be
	// loaded waits for the configuration to be fixed (see
	// ErrDefinitionUnavailable). Default 5.
	MaxClaims int
	Logger    *slog.Logger
	// Observer is called as the worker claims and ends jobs, for metrics.
	// The zero value calls nothing.
	Observer Observer
	// MayClaim, when set, is called before each claim and when a claim
	// returns. While it returns false the worker claims nothing, timers
	// included, and gives back a claim that returns; the jobs it holds keep
	// running and settle. It must be fast and safe for concurrent use. Nil
	// means always.
	MayClaim func() bool

	once sync.Once
	core *hostcore.Worker // the worker policy, shared with every backend

	finishHook    func() error                 // tests: fail finishing a deferred job
	heartbeatHook func() error                 // tests: fail heartbeats
	settleHook    func(hostcore.Claimed) error // tests: fail settling
	handlerDone   func(hostcore.Claimed, any, error)
	claimHook     func(context.Context) // tests: run once a claim query returns, before admit
	releaseHook   func(context.Context) // tests: run before giving a claim back
}

// defaults builds the shared worker once, and syncs it with the exported
// fields (see sync).
func (w *Worker) defaults() {
	w.once.Do(func() {
		w.core = &hostcore.Worker{Prefix: "sqlite", Sync: w.sync}
	})
	w.core.Init()
}

// sync copies the exported fields into the shared worker, then writes back
// the defaults it chose for fields that were zero. The core calls it at
// each entry point while no work is in progress, so a field set between
// calls takes effect at the next one.
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
	c.Backend, c.Store, c.Wake = nil, nil, nil
	if w.Store != nil {
		c.Backend, c.Store, c.Wake = w.Store.backend, w.Store.core, w.Store.hub.wake
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

// Run checks the database (as Migrate does), then claims and runs jobs
// until ctx is done or Shutdown is called, and returns nil. Cancelling ctx
// is a hard stop: every handler's context is cancelled at once, and
// settling is bounded by the claim's lease. For a graceful stop, give Run
// a context that you cancel only for a hard stop, and call Shutdown. On
// return every handler has finished, and each claim was either settled or
// given back.
func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	if w.Store != nil {
		if err := w.Store.check(ctx); err != nil {
			return err
		}
	}
	return w.core.Run(ctx)
}

// Shutdown stops the worker gracefully, like http.Server.Shutdown. It stops
// claiming at once. Active jobs keep running, with their heartbeats, until
// ctx is done; then their contexts are cancelled, and a job whose handler
// returns the context's error or a retryable error is given back without
// counting toward MaxClaims. After that point each write takes at most a
// second with modernc.org/sqlite, and up to the busy timeout with
// mattn/go-sqlite3, which does not end a wait for the lock when its
// context ends. A result that cannot be recorded is given back; while
// another connection holds the database's write lock, nothing can be
// written, and the claim is left to its lease.
//
// Shutdown returns once every handler has returned and every claim is
// settled or given back, and Run has returned. It returns nil if every
// active job finished before ctx was done, else ctx's error, joined with
// ErrHardStop if a Run call returned with its context done. A worker that
// was shut down does not start again.
func (w *Worker) Shutdown(ctx context.Context) error {
	w.defaults()
	return w.core.Shutdown(ctx)
}

// Active returns the number of active jobs: jobs the worker has claimed and
// not yet settled or given back, across all calls. A claim query in
// progress counts as one, so once the claim gate is closed (Shutdown, or
// MayClaim returning false), Active() == 0 means the worker holds no claim.
func (w *Worker) Active() int {
	w.defaults()
	return w.core.Active()
}

// RunOnce runs the jobs that are due now, then returns: the jobs due when
// it starts, and the steps that those jobs dispatch with no delay. Timers
// that come due during the call wait for the next one. It runs up to
// Concurrency jobs at once, with the heartbeats, settling and fencing of
// Run. When ctx ends, a job whose handler returns the context's error or a
// retryable error is given back, and the claim counts toward MaxClaims.
//
// Once no job is in flight, RunOnce returns nil if Shutdown stopped it,
// else ctx's error if ctx ended, else the first claim or job error, else
// nil.
func (w *Worker) RunOnce(ctx context.Context) error {
	w.defaults()
	return w.core.RunOnce(ctx)
}

// Execute runs one job, named by ref, and returns once it is settled: the
// handler of a request that a task queue pushes. A nil error means done
// or nothing to do; Settled says which. Push the jobs in Execution.Jobs.
// An error means try again later. It is [ErrNotClaimable] when the job is
// not due, is leased by another worker, has a step kind this worker has
// no handler for, or the worker is not claiming. Execute is safe for
// concurrent use and is not limited by Concurrency.
func (w *Worker) Execute(ctx context.Context, ref JobRef) (Execution, error) {
	w.defaults()
	return w.core.Execute(ctx, ref)
}

// hub wakes this store's workers when a transaction writes jobs.
type hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// signal wakes every subscriber, without blocking.
func (h *hub) signal() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
}

// wake calls notify on each signal until ctx is done.
func (h *hub) wake(ctx context.Context, notify func()) {
	c := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = map[chan struct{}]struct{}{}
	}
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.subs, c)
		h.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c:
			notify()
		}
	}
}
