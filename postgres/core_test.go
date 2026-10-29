package postgres

import (
	"context"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// The worker's policy lives in internal/hostcore, shared with every
// backend. These forwarders give the tests here the private names they
// reach for single steps of a worker.

var (
	errLeaseLost    = hostcore.ErrLeaseLost
	errShuttingDown = hostcore.ErrShuttingDown
)

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
