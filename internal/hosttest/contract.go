package hosttest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// These tests check the storage contract (internal/hostcore, Backend) and
// the behaviours PRD 007 asks every backend to share.

// TestFencedWritesTellGoneFromLost checks rule 1 for each fenced write: on
// the claim's own fence it changes the job as it should; on a job the run
// deleted it returns ErrJobGone; on a job another claim holds it returns
// ErrLeaseLost, which is not ErrJobGone.
func testFencedWritesTellGoneFromLost(t *testing.T, h Harness) {
	writes := []struct {
		name  string
		write func(ctx context.Context, b hostcore.Backend, l hostcore.Lease) error
		// changed checks the job after a write on the claim's own fence.
		changed func(t *testing.T, before, after JobRow, found bool)
	}{
		{"renew", func(ctx context.Context, b hostcore.Backend, l hostcore.Lease) error {
			return b.Renew(ctx, l, time.Hour)
		}, func(t *testing.T, before, after JobRow, found bool) {
			require.True(t, found)
			require.True(t, after.LeaseExpires.After(before.LeaseExpires.Add(time.Minute)),
				"the lease moved from %s to %s", before.LeaseExpires, after.LeaseExpires)
		}},
		{"release", func(ctx context.Context, b hostcore.Backend, l hostcore.Lease) error {
			return b.Release(ctx, l, true)
		}, func(t *testing.T, before, after JobRow, found bool) {
			require.True(t, found)
			require.Equal(t, "", after.LeaseOwner)
			require.True(t, after.LeaseExpires.IsZero())
			require.Equal(t, before.Claims-1, after.Claims, "refunded")
		}},
		{"postpone", func(ctx context.Context, b hostcore.Backend, l hostcore.Lease) error {
			return b.Postpone(ctx, l, time.Hour)
		}, func(t *testing.T, before, after JobRow, found bool) {
			require.True(t, found)
			require.Equal(t, "", after.LeaseOwner)
			require.True(t, after.RunAt.After(before.RunAt.Add(time.Minute)),
				"due moved from %s to %s", before.RunAt, after.RunAt)
		}},
		{"delete", func(ctx context.Context, b hostcore.Backend, l hostcore.Lease) error {
			return b.Write(ctx, func(tx hostcore.Tx) error { return tx.DeleteJob(ctx, l) })
		}, func(t *testing.T, _, _ JobRow, found bool) {
			require.False(t, found, "deleted")
		}},
	}
	ctx := context.Background()
	claimOne := func(t *testing.T, s *Env) claimedJob {
		t.Helper()
		w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": nil}}
		c, err := w.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		return c[0]
	}
	for _, wr := range writes {
		t.Run(wr.name, func(t *testing.T) {
			// Each case on a store of its own, so a claim takes its run.
			fresh := func(runID string) *Env {
				s := open(t, h)
				startRuns(t, s, oneStep, runID)
				return s
			}
			s := fresh("held")
			held := claimOne(t, s)
			before, _ := s.job(t, "held", held.StepID, "step")
			require.NoError(t, wr.write(ctx, s.Backend, held.Lease))
			after, found := s.job(t, "held", held.StepID, "step")
			wr.changed(t, before, after, found)

			s = fresh("gone")
			gone := claimOne(t, s)
			_, err := s.Deliver(ctx, gone.RunID, completed(gone))
			require.NoError(t, err)
			require.ErrorIs(t, wr.write(ctx, s.Backend, gone.Lease), hostcore.ErrJobGone)

			s = fresh("lost")
			lost := claimOne(t, s)
			s.h.ExpireLeases(t, s, lost.RunID)
			newer := claimOne(t, s)
			require.Equal(t, lost.JobID, newer.JobID)
			require.True(t, newer.Fence > lost.Fence)
			err = wr.write(ctx, s.Backend, lost.Lease)
			require.ErrorIs(t, err, hostcore.ErrLeaseLost)
			require.False(t, errors.Is(err, hostcore.ErrJobGone), err)
		})
	}
}

// TestTimeBounds: a job due now is claimed by the next claim, and a claim
// whose lease expires is taken by another worker within the lease plus
// Poll. A backend that stores times in a form that sorts wrongly fails
// both.
func testTimeBounds(t *testing.T, h Harness) {
	ctx := context.Background()
	t.Run("due now", func(t *testing.T) {
		s := open(t, h)
		startRuns(t, s, oneStep, "now")
		jobs := s.jobs(t, "now")
		require.Len(t, jobs, 1)
		require.True(t, time.Since(jobs[0].RunAt) < time.Minute, jobs[0].RunAt)
		w := &Worker{Store: s, Handlers: map[string]workflow.Handler{"action": nil}}
		c, err := w.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
	})

	t.Run("expired lease", func(t *testing.T) {
		const (
			lease = time.Second
			poll  = 50 * time.Millisecond
		)
		s := open(t, h)
		startRuns(t, s, oneStep, "expires")
		stale := &Worker{
			Store:    s,
			Lease:    lease,
			Handlers: map[string]workflow.Handler{"action": nil},
		}
		c, err := stale.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		claimed := time.Now()

		var claims atomic.Int32
		startWorker(t, &Worker{
			Store: s,
			Poll:  poll,
			Handlers: map[string]workflow.Handler{
				"action": func(_ context.Context, job workflow.Job) (any, error) {
					claims.Store(int32(job.Claims))
					return "done", nil
				},
			},
		})
		waitStatus(t, s, "expires", workflow.StatusCompleted)
		took := time.Since(claimed)
		require.Equal(t, int32(2), claims.Load())
		require.True(t, took < lease+poll+time.Second, took)
	})
}

// TestALateStampedEventKeepsTheClock: an event stamped before the run's
// last one (a worker with a slow clock) is applied at the run's time, and
// the run and step rows take that time too, so Steps keeps dispatch order.
func testALateStampedEventKeepsTheClock(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	startRuns(t, s, chain, "slow")
	_, err := s.Deliver(ctx, "slow", workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/a#1",
		Output: "done",
		At:     time.Now().Add(-time.Hour),
	})
	require.NoError(t, err)

	r, err := s.GetRun(ctx, "slow")
	require.NoError(t, err)
	require.False(t, r.UpdatedAt.Before(r.CreatedAt), r.UpdatedAt, r.CreatedAt)
	steps, err := s.Steps(ctx, "slow")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, "a", steps[0].Step)
	require.Equal(t, "b", steps[1].Step)
	require.False(t, steps[0].SettledAt.Before(steps[0].DispatchedAt),
		*steps[0].SettledAt, steps[0].DispatchedAt)
	require.False(t, steps[1].DispatchedAt.Before(steps[0].DispatchedAt),
		steps[1].DispatchedAt, steps[0].DispatchedAt)
}

// TestTwoHandlesOnOneDatabase stands for two processes on one database
// (PRD 007, UC-3): a job one store writes is run by a worker on the other,
// and a fence taken through one handle shuts out a claim made through the
// other.
func testTwoHandlesOnOneDatabase(t *testing.T, h Harness) {
	ctx := context.Background()
	t.Run("job written by one, run by the other", func(t *testing.T) {
		s := open(t, h)
		other := s.reopen(t, kinds, Options{})
		const poll = 100 * time.Millisecond
		startWorker(t, &Worker{Store: other, Poll: poll, Handlers: returning("done")})
		start := time.Now()
		startRuns(t, s, oneStep, "web")
		waitStatus(t, s, "web", workflow.StatusCompleted)
		// Within Poll (PRD 007, UC-3), with a margin for the claim and the
		// settle.
		require.True(t, time.Since(start) < poll+500*time.Millisecond, time.Since(start))
	})

	t.Run("fence across handles", func(t *testing.T) {
		s := open(t, h)
		other := s.reopen(t, kinds, Options{})
		startRuns(t, s, oneStep, "fenced")
		handlers := map[string]workflow.Handler{"action": nil}
		w1 := &Worker{Store: s, Handlers: handlers}
		c1, err := w1.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c1, 1)
		s.h.ExpireLeases(t, s, "fenced")
		w2 := &Worker{Store: other, Handlers: handlers}
		c2, err := w2.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c2, 1)
		require.True(t, c2[0].Fence > c1[0].Fence)

		stale := workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: c1[0].StepID,
			Output: "stale",
		}
		w1.settle(ctx, c1[0], time.Now(), stale, w1.Logger)
		fresh := workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: c2[0].StepID,
			Output: "fresh",
		}
		w2.settle(ctx, c2[0], time.Now(), fresh, w2.Logger)
		r := waitStatus(t, s, "fenced", workflow.StatusCompleted)
		require.Equal(t, "fresh", r.State.Unit(c2[0].StepID).Output)
	})
}

// returning is the suite's handlers with an "action" that returns out.
func returning(out string) map[string]workflow.Handler {
	return map[string]workflow.Handler{
		"action": func(context.Context, workflow.Job) (any, error) { return out, nil },
	}
}

// shutdownBound is how long Shutdown with a 300 ms deadline may take while
// another connection holds the run's lock, by the backend's Caps (tech
// spec 007, "Shutdown under a held lock").
func shutdownBound(c Caps) time.Duration {
	const deadline, slack = 300 * time.Millisecond, 500 * time.Millisecond
	switch {
	case c.RowLocks:
		return 2 * time.Second
	case c.CancelEndsLockWait:
		return deadline + 2*time.Second + slack
	}
	return deadline + 2*c.BusyTimeout + slack
}

// TestShutdownUnderAHeldLock: a handler returns its result after another
// connection has locked its run. Shutdown with a 300 ms deadline returns
// within the backend's bound with DeadlineExceeded, and the handler has
// returned. With row locks the claim is given back; where one lock covers
// the whole database the give-back waits on the same lock, so the claim is
// left to its lease. After the lock is released another worker runs the
// job once its lease expires.
func testShutdownUnderAHeldLock(t *testing.T, h Harness) {
	s := open(t, h)
	startRuns(t, s, oneStep, "locked")
	started, locked := make(chan struct{}), make(chan struct{})
	var returned atomic.Bool
	w := &Worker{
		Store: s,
		Lease: time.Minute,
		Poll:  10 * time.Millisecond,
		Handlers: map[string]workflow.Handler{
			"action": func(context.Context, workflow.Job) (any, error) {
				close(started)
				<-locked
				return "done", nil
			},
		},
		handlerDone: func(claimedJob, any, error) { returned.Store(true) },
	}
	goRun(t, w)
	// Release the handler on every path. If the test fails before it
	// takes the lock, this cleanup runs before goRun's, which waits for
	// the handler.
	unlock := sync.OnceFunc(func() { close(locked) })
	t.Cleanup(unlock)
	await(t, "the handler starts", started)
	release := s.holdRunLock(t, "locked")
	unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	err := w.Shutdown(ctx)
	took := time.Since(begin)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.True(t, returned.Load(), "the handler has returned")
	bound := shutdownBound(h.Caps())
	require.True(t, took < bound, "Shutdown took %s, bound %s", took, bound)

	claims, leased := soleJob(t, s, "locked")
	if h.Caps().RowLocks {
		require.Equal(t, 0, claims, "given back, refunded")
		require.False(t, leased)
	} else {
		require.Equal(t, 1, claims, "left to its lease")
		require.True(t, leased)
	}

	release()
	s.h.ExpireLeases(t, s, "locked")
	startWorker(t, &Worker{Store: s, Handlers: returning("again")})
	waitStatus(t, s, "locked", workflow.StatusCompleted)
}

// TestCallerTxCommitMakesTheRunClaimable: a run started in the
// application's transaction (a SQLTx) runs once the transaction commits.
func testCallerTxCommitMakesTheRunClaimable(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	_, htx, commit, _ := h.CallerTx(t, s)
	res, err := s.Store.Start(ctx, htx, NewRun{ID: "caller", Definition: load(t, oneStep)})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.NoError(t, commit())

	startWorker(t, &Worker{Store: s, Handlers: returning("done")})
	waitStatus(t, s, "caller", workflow.StatusCompleted)
}

// TestCallerTxRollbackLeavesNothing: a run started and advanced in the
// application's transaction leaves no definition, run, step record, event
// or job when the transaction rolls back (PRD 007, UC-4).
func testCallerTxRollbackLeavesNothing(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	_, htx, _, rollback := h.CallerTx(t, s)
	_, err := s.Store.Start(ctx, htx, NewRun{ID: "rolled", Definition: load(t, stepDoc("a"))})
	require.NoError(t, err)
	_, err = s.Store.Deliver(ctx, htx, "rolled",
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/a#1", Output: "done"})
	require.NoError(t, err)
	require.NoError(t, rollback())

	_, err = s.GetRun(ctx, "rolled")
	require.ErrorIs(t, err, ErrRunNotFound)
	require.Equal(t, 0, s.count(t, "definitions", ""))
	for _, table := range []string{"runs", "steps", "events", "jobs"} {
		require.Equal(t, 0, s.count(t, table, "rolled"), table)
	}
}

// TestAnUnstorableStartWritesNothing: a Start refused as unstorable writes
// nothing to the caller's transaction, which goes on to commit.
func testAnUnstorableStartWritesNothing(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	_, htx, commit, _ := h.CallerTx(t, s)
	def := load(t, stepDoc("a"))
	for _, run := range []NewRun{
		{ID: "input", Definition: def, Input: map[string]any{"x": "\x00"}},
		{ID: "meta", Definition: def, Metadata: map[string]any{"x": "\x00"}},
	} {
		_, err := s.Store.Start(ctx, htx, run)
		require.ErrorIs(t, err, ErrUnstorable)
	}
	require.NoError(t, commit())
	require.Equal(t, 0, s.count(t, "definitions", ""))
}
