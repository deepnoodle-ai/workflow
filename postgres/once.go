package postgres

import "context"

// RunOnce runs the jobs that are due now, then returns. Use it where no
// process runs all the time: from an endpoint that a scheduler calls, or
// in a job that exits when it is done.
//
// Due now means the jobs due when RunOnce starts, and the steps that those
// jobs dispatch with no delay, so a run of several steps in a row finishes
// in one call. A timer that comes due during the call, and work that other
// callers add during it, wait for the next call. So RunOnce always returns.
// It runs up to Concurrency jobs at once, with the heartbeats, settling and
// fencing of Run. It does not LISTEN.
//
// A job whose result could not be recorded, or that waits for its run's
// definition to load, does not stop the call: it stays in the outbox, and
// the other due jobs still run. A claim error stops claiming. When ctx ends,
// handlers are cancelled, and a job whose handler returns the context's
// error or a retryable error is given back, so the next call can take it at
// once. That claim counts toward MaxClaims, unlike a Shutdown, because a
// context that ends is a request deadline: a step that always outlives it
// fails as abandoned. A handler that ignores ctx runs on, and RunOnce
// waits for it. Its heartbeat extends its lease until it returns or a
// Shutdown deadline passes, but only while the process gets CPU. Where the
// platform throttles CPU after a request ends (Cloud Run with request-based
// billing), the lease can run out, another claim can run the step again,
// and the fence then drops the first result. Keep CPU allocated until the
// handler returns, or accept that the step can run twice.
//
// Once no job is in flight, RunOnce returns nil if Shutdown stopped it,
// else ctx's error if ctx ended, else the first claim or job error, else
// nil. A job that another worker took over is not an error.
func (w *Worker) RunOnce(ctx context.Context) error {
	w.defaults()
	return w.core.RunOnce(ctx)
}

// Execute runs one job, named by ref, and returns once it is settled: the
// handler of a request that a task queue (such as Cloud Tasks) pushes. It
// claims the job with a new fence, runs the handler with a heartbeat, and
// records the result, as Run does. A task delivered twice is safe: the
// second claim finds the job gone, or leased by the first.
//
// A nil error means done or nothing to do: answer 2xx. Settled says which.
// Push the jobs in Execution.Jobs. An error means try again later: answer
// 5xx. It is [ErrNotClaimable] when the job is not due, is leased by
// another worker, has a step kind this worker has no handler for, or the
// worker is not claiming. Execute claims nothing then. A ref without a
// RunID or a StepID is an error too, so that a task body that does not
// match JobRef's JSON is not acknowledged and dropped.
//
// If ctx ends while the handler runs and the handler returns the context's
// error or a retryable error, the claim is given back and counts toward
// MaxClaims (a request deadline, as for RunOnce), and Execute returns ctx's
// error. A handler that ignores ctx runs on, with a heartbeat that holds
// its lease only while the process gets CPU, as for RunOnce: on throttled
// CPU the step can run twice. Execute is safe for concurrent use and is
// not limited by Concurrency: set the platform's request concurrency
// instead.
// Shutdown waits for it, and Active counts its job.
func (w *Worker) Execute(ctx context.Context, ref JobRef) (Execution, error) {
	w.defaults()
	return w.core.Execute(ctx, ref)
}
