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
// exported fields and private test hooks. Each call goes to the backend's
// own Worker (see [Harness.Worker]), which takes these fields at each sync
// and writes back the defaults it chose.
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

	once    sync.Once
	backend BackendWorker

	finishHook    func() error
	heartbeatHook func() error
	settleHook    func(claimedJob) error
	handlerDone   func(claimedJob, any, error)
	beforeClaim   func(context.Context)
	claimHook     func(context.Context)
	releaseHook   func(context.Context)
}

// BackendWorker is a backend's own Worker, as [Harness.Worker] returns it.
type BackendWorker interface {
	Run(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Active() int
	RunOnce(ctx context.Context) error
	Execute(ctx context.Context, ref JobRef) (Execution, error)
	// Core syncs the Worker, as each of its methods does, and returns the
	// shared worker it runs, for the tests of single steps.
	Core() *hostcore.Worker
}

// Hooks returns the test hooks set on w, for the harness to copy into the
// backend's Worker.
func (w *Worker) Hooks() hostcore.Hooks {
	return hostcore.Hooks{
		Finish:      w.finishHook,
		Heartbeat:   w.heartbeatHook,
		Settle:      w.settleHook,
		HandlerDone: w.handlerDone,
		BeforeClaim: w.beforeClaim,
		Claim:       w.claimHook,
		Release:     w.releaseHook,
	}
}

// worker returns the backend's Worker for w, built by its Store's harness
// on the first call.
func (w *Worker) worker() BackendWorker {
	w.once.Do(func() {
		if w.Store == nil {
			panic("hosttest: a Worker needs a Store")
		}
		w.backend = w.Store.h.Worker(w)
	})
	return w.backend
}

// defaults syncs the backend's Worker, as each of its methods does, so
// that w holds the defaults it chose.
func (w *Worker) defaults() { w.worker().Core() }

func (w *Worker) Run(ctx context.Context) error { return w.worker().Run(ctx) }

func (w *Worker) Shutdown(ctx context.Context) error { return w.worker().Shutdown(ctx) }

func (w *Worker) Active() int { return w.worker().Active() }

func (w *Worker) RunOnce(ctx context.Context) error { return w.worker().RunOnce(ctx) }

func (w *Worker) Execute(ctx context.Context, ref JobRef) (Execution, error) {
	return w.worker().Execute(ctx, ref)
}

// outcome is what processing a claimed job did.
type outcome struct {
	settled bool
	result  *Result
	err     error
}

func (w *Worker) claim(ctx context.Context, limit int) ([]claimedJob, error) {
	return w.worker().Core().Claim(ctx, limit)
}

func (w *Worker) process(ctx context.Context, t claimedJob) outcome {
	settled, res, err := w.worker().Core().Process(ctx, t)
	return outcome{settled: settled, result: res, err: err}
}

func (w *Worker) call(ctx context.Context, t claimedJob) (any, error) {
	return w.worker().Core().Call(ctx, t)
}

func (w *Worker) heartbeat(ctx context.Context, t claimedJob, lost func(error)) time.Time {
	return w.worker().Core().Heartbeat(ctx, t, lost)
}

func (w *Worker) settle(
	ctx context.Context, t claimedJob, renewed time.Time, ev workflow.Event, log *slog.Logger,
) outcome {
	settled, err := w.worker().Core().Settle(ctx, t, renewed, ev, log)
	return outcome{settled: settled, err: err}
}

func (w *Worker) finishDeferred(ctx context.Context, t claimedJob, log *slog.Logger) {
	w.worker().Core().FinishDeferred(ctx, t, log)
}

func (w *Worker) release(t claimedJob) { w.worker().Core().Release(t) }

func (w *Worker) isStopped() bool { return w.worker().Core().IsStopped() }

func (w *Worker) cutDone() bool { return w.worker().Core().CutDone() }
