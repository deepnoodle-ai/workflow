package hostcore

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/durable"
)

// ErrShuttingDown is the outcome of a job that a Shutdown deadline cut off
// and that was given back. The worker adds its backend's prefix.
var ErrShuttingDown = errors.New("the worker is shutting down; the job was given back")

// RunOnce runs the jobs that are due now, then returns. See the backend's
// Worker.RunOnce.
func (w *Worker) RunOnce(ctx context.Context) error {
	if !w.enter() {
		return nil
	}
	defer w.leave()
	cutoff, err := w.Backend.Now(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: read the database clock: %w", w.Prefix, err)
	}

	hctx, cancel := w.handlerContext(ctx)
	defer cancel()
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		running  int
		next     []durable.JobRef // steps that this call's jobs dispatched, due at once
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
			var jobs []Claimed
			jobs, refs, claimErr = w.claimDue(cctx, free, cutoff, refs)
			live := cctx.Err() == nil // not a claim that ctx or Shutdown cut off
			cancelClaim()
			if claimErr != nil && live {
				w.Logger.Error("workflow worker: claim", "error", claimErr)
				mu.Lock()
				firstErr = cmp.Or(firstErr, claimErr)
				mu.Unlock()
			}
			jobs = w.admit(jobs, false)
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
					if !errors.Is(o.err, ErrShuttingDown) {
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
	ctx context.Context, limit int, cutoff time.Time, refs []durable.JobRef,
) ([]Claimed, []durable.JobRef, error) {
	var jobs []Claimed
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
		more, err := w.claimUntil(ctx, limit-len(jobs), cutoff)
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
func dueSteps(res *durable.Result) []durable.JobRef {
	var out []durable.JobRef
	now := time.Now()
	for _, ref := range res.Jobs() {
		if !ref.Timer && !ref.RunAt.After(now) {
			out = append(out, ref)
		}
	}
	return out
}

// Execute runs one job, named by ref, and returns once it is settled. See
// the backend's Worker.Execute.
func (w *Worker) Execute(ctx context.Context, ref durable.JobRef) (durable.Execution, error) {
	if ref.RunID == "" || ref.StepID == "" {
		// Likely a task body that does not match JobRef's JSON. Not "nothing
		// to do": the task would be acknowledged and its job never run.
		return durable.Execution{}, fmt.Errorf(
			"%s: Execute needs a JobRef with a run_id and a step_id, got %+v", w.Prefix, ref)
	}
	if !w.enter() {
		return durable.Execution{}, fmt.Errorf("%w: the worker is shut down", durable.ErrNotClaimable)
	}
	defer w.leave()
	if !w.openClaim() {
		return durable.Execution{}, fmt.Errorf("%w: the worker is not claiming", durable.ErrNotClaimable)
	}
	cctx, cancelClaim := w.cutAfter(ctx)
	t, ok, err := w.claimKey(cctx, ref)
	cancelClaim()
	var claimed []Claimed
	if ok {
		claimed = []Claimed{t}
	}
	if len(w.admit(claimed, false)) == 0 { // admit always follows openClaim
		switch {
		case err != nil:
			return durable.Execution{}, err
		case !ok:
			return durable.Execution{}, w.whyNotClaimed(ctx, ref)
		}
		return durable.Execution{}, fmt.Errorf("%w: the worker stopped claiming", durable.ErrNotClaimable)
	}

	hctx, cancel := w.handlerContext(ctx)
	defer cancel()
	t.request = true
	o := w.process(hctx, t)
	w.settled()
	if o.err != nil {
		return durable.Execution{}, o.err
	}
	return durable.Execution{Settled: o.settled, Jobs: o.result.Jobs()}, nil
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
func (w *Worker) claimKey(ctx context.Context, ref durable.JobRef) (_ Claimed, ok bool, _ error) {
	c, err := w.Backend.ClaimKey(ctx, ref, ClaimRequest{
		Owner:  w.ID,
		Queues: w.queues(),
		Limit:  1,
		Lease:  w.Lease,
	})
	if err != nil {
		if ctx.Err() == nil && w.Observer.ClaimError != nil {
			w.Observer.ClaimError(err)
		}
		return Claimed{}, false, err
	}
	if c == nil {
		return Claimed{}, false, nil
	}
	return *c, true, nil
}

// whyNotClaimed explains a claim by key that leased nothing: nil if the job
// is gone, else ErrNotClaimable with the reason. It reads the job without a
// lock, so it can race with another worker; either answer is safe.
func (w *Worker) whyNotClaimed(ctx context.Context, ref durable.JobRef) error {
	j, err := w.Backend.JobStatus(ctx, ref)
	switch {
	case err != nil:
		return err
	case !j.Found:
		return nil
	case !slices.Contains(w.queues(), j.Queue):
		return fmt.Errorf("%w: this worker has no handler for step kind %q",
			durable.ErrNotClaimable, strings.TrimPrefix(j.Queue, "step:"))
	case j.RunPaused && !ref.Timer:
		return fmt.Errorf("%w: its run is paused", durable.ErrNotClaimable)
	case j.RunAt.After(j.Now):
		return fmt.Errorf("%w: it is due at %s", durable.ErrNotClaimable, j.RunAt.UTC().Format(time.RFC3339))
	case !j.LeaseExpires.IsZero() && !j.LeaseExpires.Before(j.Now):
		return fmt.Errorf("%w: another worker holds its lease until %s",
			durable.ErrNotClaimable, j.LeaseExpires.UTC().Format(time.RFC3339))
	}
	return fmt.Errorf("%w: another worker claimed it at the same time", durable.ErrNotClaimable)
}
