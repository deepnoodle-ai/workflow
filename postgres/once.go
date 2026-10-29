package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotClaimable is the error Execute returns for a job that exists but
// that it must not claim now: another worker holds its lease, it is not due
// yet, this worker has no handler for its step kind, or the worker is not
// claiming (Shutdown, MayClaim). The message says which. Retry later: an
// HTTP handler answers 503 so that the task queue retries.
var ErrNotClaimable = errors.New("postgres: the job cannot be claimed now")

// errShuttingDown is the outcome of a job that a Shutdown deadline cut off
// and that was given back.
var errShuttingDown = errors.New("postgres: the worker is shutting down; the job was given back")

// Execution is what Execute did.
type Execution struct {
	// Settled is true when this call ran the job and recorded what came
	// of it: a result, a failure, a hand-off, or a failed run. False means
	// there was nothing to do: the job is gone (done, cancelled, its run
	// finished), or another worker took it over.
	Settled bool
	// Jobs are the jobs the job's result wrote: its run's next steps and
	// timers. Push them, as for [Result.Jobs].
	Jobs []JobRef
}

// claimKeySQL leases one job by its key if it is due and not leased:
// $1 run, $2 StepID, $3 kind, $4 worker, $5 lease seconds, $6 queues.
// It returns the same columns as claimSQL.
const claimKeySQL = `
		UPDATE {{schema}}.jobs t
		SET lease_owner = $4, lease_expires = now() + make_interval(secs => $5),
		    claims = t.claims + 1, fence = t.fence + 1
		FROM {{schema}}.runs r
		WHERE t.run_id = $1 AND t.step_id = $2 AND t.kind = $3 AND r.id = t.run_id
		  AND t.queue = ANY($6::text[]) AND t.run_at <= now()
		  AND (t.lease_expires IS NULL OR t.lease_expires < now())
		RETURNING t.id, t.run_id, t.command, t.claims, t.fence, r.definition_hash`

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
// fails as abandoned. A handler that ignores ctx keeps its lease, through
// its heartbeat, until it returns or a Shutdown deadline passes, so no
// other claim runs its step beside it. RunOnce waits for it.
//
// Once no job is in flight, RunOnce returns nil if Shutdown stopped it,
// else ctx's error if ctx ended, else the first claim or job error, else
// nil. A job that another worker took over is not an error.
func (w *Worker) RunOnce(ctx context.Context) error {
	if !w.enter() {
		return nil
	}
	defer w.leave()
	var cutoff time.Time
	if err := w.Store.pool.QueryRow(ctx, `SELECT now()`).Scan(&cutoff); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("postgres: read the database clock: %w", err)
	}

	hctx, cancel := w.handlerContext(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		running  int
		next     []JobRef // steps that this call's jobs dispatched, due at once
		claimErr error
		firstErr error // the first claim or job error
	)
	defer wg.Wait()
	done := make(chan struct{}, 1)
	ctxDone, stopped := ctx.Done(), w.stopped

	for {
		mu.Lock()
		free := w.Concurrency - running
		refs := next
		next = nil
		mu.Unlock()
		claiming := claimErr == nil && ctx.Err() == nil
		full := false // the claim filled every free slot
		if claiming && free > 0 && !w.openClaim() {
			claiming = false // the gate is closed: finish what is held
		}
		if claiming && free > 0 {
			// As in Run: only a claim query that hangs past a Shutdown
			// deadline is cancelled, and admit always follows openClaim.
			cctx, cancelClaim := w.cutAfter(ctx)
			var jobs []claimedJob
			jobs, refs, claimErr = w.claimDue(cctx, free, cutoff, refs)
			live := cctx.Err() == nil // not a claim that ctx or Shutdown cut off
			cancelClaim()
			if claimErr != nil && live {
				w.Logger.Error("workflow worker: claim", "error", claimErr)
				mu.Lock()
				firstErr = cmp.Or(firstErr, claimErr)
				mu.Unlock()
			}
			jobs = w.admit(jobs)
			full = len(jobs) == free
			for _, t := range jobs {
				t.request = true
				mu.Lock()
				running++
				mu.Unlock()
				wg.Go(func() {
					o := w.process(hctx, t)
					w.settled()
					mu.Lock()
					running--
					next = append(next, dueSteps(o.result)...)
					if !errors.Is(o.err, errShuttingDown) {
						firstErr = cmp.Or(firstErr, o.err)
					}
					mu.Unlock()
					select {
					case done <- struct{}{}:
					default:
					}
				})
			}
		}

		mu.Lock()
		next = append(refs, next...)
		finished := running == 0 && (len(next) == 0 || !claiming)
		mu.Unlock()
		if full {
			// As in Run: more may be due. Those jobs may all have settled
			// already, so "nothing running" does not mean "nothing left".
			continue
		}
		if finished {
			break
		}
		select {
		case <-done:
		case <-ctxDone:
			ctxDone = nil // seen; a closed channel would spin the loop
		case <-stopped:
			stopped = nil
		}
	}

	if w.isStopped() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return firstErr // no job in flight: no lock needed
}

// claimDue claims up to limit jobs for RunOnce: first the follow-up steps
// in refs, by key, then jobs due by cutoff. It returns the refs it did not
// try.
func (w *Worker) claimDue(
	ctx context.Context, limit int, cutoff time.Time, refs []JobRef,
) ([]claimedJob, []JobRef, error) {
	var jobs []claimedJob
	for len(refs) > 0 && len(jobs) < limit {
		t, ok, err := w.claimKey(ctx, refs[0])
		if err != nil {
			return jobs, refs, err
		}
		refs = refs[1:] // not claimed: another worker has it, or it is done
		if ok {
			jobs = append(jobs, t)
		}
	}
	if len(jobs) < limit {
		more, err := w.claimUntil(ctx, limit-len(jobs), &cutoff)
		jobs = append(jobs, more...)
		if err != nil {
			return jobs, refs, err
		}
	}
	return jobs, refs, nil
}

// dueSteps are the steps a settle dispatched that are due at once. RunOnce
// runs them in the same call; it never follows timers, which is what makes
// it return: every cycle in a definition has max_visits or a sleep.
func dueSteps(res *Result) []JobRef {
	var out []JobRef
	now := time.Now()
	for _, ref := range res.Jobs() {
		if !ref.Timer && !ref.RunAt.After(now) {
			out = append(out, ref)
		}
	}
	return out
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
// error. A handler that ignores ctx keeps its lease until it returns or a
// Shutdown deadline passes, as for RunOnce. Execute is safe for concurrent use and is not
// limited by Concurrency: set the platform's request concurrency instead.
// Shutdown waits for it, and Active counts its job.
func (w *Worker) Execute(ctx context.Context, ref JobRef) (Execution, error) {
	if ref.RunID == "" || ref.StepID == "" {
		// Likely a task body that does not match JobRef's JSON. Not "nothing
		// to do": the task would be acknowledged and its job never run.
		return Execution{}, fmt.Errorf(
			"postgres: Execute needs a JobRef with a run_id and a step_id, got %+v", ref)
	}
	if !w.enter() {
		return Execution{}, fmt.Errorf("%w: the worker is shut down", ErrNotClaimable)
	}
	defer w.leave()
	if !w.openClaim() {
		return Execution{}, fmt.Errorf("%w: the worker is not claiming", ErrNotClaimable)
	}
	cctx, cancelClaim := w.cutAfter(ctx)
	t, ok, err := w.claimKey(cctx, ref)
	cancelClaim()
	var claimed []claimedJob
	if ok {
		claimed = []claimedJob{t}
	}
	if len(w.admit(claimed)) == 0 { // admit always follows openClaim
		switch {
		case err != nil:
			return Execution{}, err
		case !ok:
			return Execution{}, w.whyNotClaimed(ctx, ref)
		}
		return Execution{}, fmt.Errorf("%w: the worker stopped claiming", ErrNotClaimable)
	}

	hctx, cancel := w.handlerContext(ctx)
	defer cancel()
	t.request = true
	o := w.process(hctx, t)
	w.settled()
	if o.err != nil {
		return Execution{}, o.err
	}
	return Execution{Settled: o.settled, Jobs: o.result.Jobs()}, nil
}

// handlerContext is ctx, also cancelled when a Shutdown deadline passes.
func (w *Worker) handlerContext(ctx context.Context) (context.Context, context.CancelFunc) {
	hctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.cut, cancel)
	return hctx, func() {
		stop()
		cancel()
	}
}

// claimKey leases the job ref names if it is due, not leased, and of a
// kind this worker handles. ok is false if it did not.
func (w *Worker) claimKey(ctx context.Context, ref JobRef) (_ claimedJob, ok bool, _ error) {
	claimed := time.Now() // no later than the database's now()
	var t claimedJob
	err := w.Store.pool.QueryRow(ctx, w.Store.sql(claimKeySQL),
		ref.RunID, string(ref.StepID), ref.kind(), w.ID, w.Lease.Seconds(), w.queues()).
		Scan(&t.ID, &t.RunID, &t.Command, &t.Claims, &t.fence, &t.defHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return claimedJob{}, false, nil
	}
	if err != nil {
		if ctx.Err() == nil && w.Observer.ClaimError != nil {
			w.Observer.ClaimError(err)
		}
		return claimedJob{}, false, err
	}
	t.claimed = claimed
	return t, true, nil
}

// whyNotClaimed explains a claim by key that leased nothing: nil if the job
// is gone, else ErrNotClaimable with the reason. It reads the job without a
// lock, so it can race with another worker; either answer is safe.
func (w *Worker) whyNotClaimed(ctx context.Context, ref JobRef) error {
	var (
		queue      string
		runAt, now time.Time
		lease      *time.Time
	)
	err := w.Store.pool.QueryRow(ctx, w.Store.sql(`
		SELECT queue, run_at, lease_expires, now() FROM {{schema}}.jobs
		WHERE run_id = $1 AND step_id = $2 AND kind = $3`),
		ref.RunID, string(ref.StepID), ref.kind()).Scan(&queue, &runAt, &lease, &now)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return err
	case !slices.Contains(w.queues(), queue):
		return fmt.Errorf("%w: this worker has no handler for step kind %q",
			ErrNotClaimable, strings.TrimPrefix(queue, "step:"))
	case runAt.After(now):
		return fmt.Errorf("%w: it is due at %s", ErrNotClaimable, runAt.UTC().Format(time.RFC3339))
	case lease != nil && !lease.Before(now):
		return fmt.Errorf("%w: another worker holds its lease until %s",
			ErrNotClaimable, lease.UTC().Format(time.RFC3339))
	}
	return fmt.Errorf("%w: another worker claimed it at the same time", ErrNotClaimable)
}
