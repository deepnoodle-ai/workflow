package hostcore

import (
	"context"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
)

// These methods let a backend's tests drive single steps of the worker.
// Call Init first.

// Claim leases up to limit due jobs, as Run does.
func (w *Worker) Claim(ctx context.Context, limit int) ([]Claimed, error) {
	return w.claim(ctx, limit)
}

// Process runs a claimed job and settles it, as Run does. settled is true
// when this claim settled the job; res is the settle's result; err means
// the job is still there.
func (w *Worker) Process(ctx context.Context, t Claimed) (settled bool, res *durable.Result, err error) {
	o := w.process(ctx, t)
	return o.settled, o.result, o.err
}

// Call runs t's handler, as a running job does, with no heartbeat.
func (w *Worker) Call(ctx context.Context, t Claimed) (any, error) { return w.call(ctx, t) }

// Heartbeat extends t's lease until ctx is done, as a running job's
// heartbeat does, and calls lost when the claim is lost.
func (w *Worker) Heartbeat(ctx context.Context, t Claimed, lost func(error)) time.Time {
	return w.heartbeat(ctx, t, lost)
}

// Settle records ev for t, as a handler's return does. renewed is when
// t's lease was last extended. settled and err are as Process's.
func (w *Worker) Settle(
	ctx context.Context, t Claimed, renewed time.Time, ev workflow.Event, log *slog.Logger,
) (settled bool, err error) {
	o := w.settle(ctx, t, renewed, ev, log)
	return o.settled, o.err
}

// FinishDeferred marks t's handed-off job done.
func (w *Worker) FinishDeferred(ctx context.Context, t Claimed, log *slog.Logger) {
	w.finishDeferred(ctx, t, log)
}

// Release gives t back without counting it toward MaxClaims.
func (w *Worker) Release(t Claimed) { w.release(t) }

// IsStopped reports whether Shutdown was called.
func (w *Worker) IsStopped() bool { return w.isStopped() }

// CutDone reports whether a Shutdown deadline has passed.
func (w *Worker) CutDone() bool { return w.cutDone() }
