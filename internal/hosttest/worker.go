package hosttest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// Worker is a backend's public Worker as the suite sees it: the same
// exported fields and private test hooks, on the shared core.
type Worker struct {
	Store       *Env
	Handlers    map[string]workflow.Handler
	ID          string
	Concurrency int
	Lease       time.Duration
	Poll        time.Duration
	MaxClaims   int
	Logger      *slog.Logger
	Observer    Observer
	MayClaim    func() bool

	once sync.Once
	core *hostcore.Worker

	finishHook    func() error
	heartbeatHook func() error
	settleHook    func(claimedJob) error
	handlerDone   func(claimedJob, any, error)
	claimHook     func(context.Context)
	releaseHook   func(context.Context)
}

// defaults builds the shared worker once and syncs it with the exported
// fields, as a backend's Worker does.
func (w *Worker) defaults() {
	w.once.Do(func() { w.core = &hostcore.Worker{Sync: w.sync} })
	w.core.Init()
}

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
	c.Backend, c.Store, c.Prefix, c.Wake = nil, nil, "", nil
	if w.Store != nil {
		c.Backend, c.Store = w.Store.Backend, w.Store.Store
		c.Prefix, c.Wake = w.Store.Store.Prefix, w.Store.Wake
	}
	defaults()
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

func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	return w.core.Run(ctx)
}

func (w *Worker) Shutdown(ctx context.Context) error {
	w.defaults()
	return w.core.Shutdown(ctx)
}

func (w *Worker) Active() int {
	w.defaults()
	return w.core.Active()
}

func (w *Worker) RunOnce(ctx context.Context) error {
	w.defaults()
	return w.core.RunOnce(ctx)
}

func (w *Worker) Execute(ctx context.Context, ref JobRef) (Execution, error) {
	w.defaults()
	return w.core.Execute(ctx, ref)
}

// outcome is what processing a claimed job did.
type outcome struct {
	settled bool
	result  *Result
	err     error
}

func (w *Worker) claim(ctx context.Context, limit int) ([]claimedJob, error) {
	w.defaults()
	return w.core.Claim(ctx, limit)
}

func (w *Worker) process(ctx context.Context, t claimedJob) outcome {
	w.defaults()
	settled, res, err := w.core.Process(ctx, t)
	return outcome{settled: settled, result: res, err: err}
}

func (w *Worker) call(ctx context.Context, t claimedJob) (any, error) {
	w.defaults()
	return w.core.Call(ctx, t)
}

func (w *Worker) heartbeat(ctx context.Context, t claimedJob, lost func(error)) time.Time {
	w.defaults()
	return w.core.Heartbeat(ctx, t, lost)
}

func (w *Worker) settle(
	ctx context.Context, t claimedJob, renewed time.Time, ev workflow.Event, log *slog.Logger,
) {
	w.defaults()
	w.core.Settle(ctx, t, renewed, ev, log)
}

func (w *Worker) finishDeferred(ctx context.Context, t claimedJob, log *slog.Logger) {
	w.defaults()
	w.core.FinishDeferred(ctx, t, log)
}

func (w *Worker) release(t claimedJob) {
	w.defaults()
	w.core.Release(t)
}

func (w *Worker) isStopped() bool {
	w.defaults()
	return w.core.IsStopped()
}

func (w *Worker) cutDone() bool {
	w.defaults()
	return w.core.CutDone()
}
