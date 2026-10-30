package hosttest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestStaleClaimAfterReleaseAndReclaim extends
// TestReleaseDoesNotRestoreAStaleFence with the full ABA sequence. A
// release gives back a claim, so the claims count returns to where the
// stalled worker left it; the fence must not. Worker A claims and stalls
// past its lease; B claims and releases gracefully; A heartbeats (fenced
// out); C claims and completes the step; A heartbeats and settles with
// its stale claim (fenced out). The run sees the step complete once.
func testStaleClaimAfterReleaseAndReclaim(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "a", "action": "x", "next": ["b"]}, {"name": "b", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "aba", Definition: def})
	require.NoError(t, err)

	var runsOfA atomic.Int32
	// A's lease is short so that each of its heartbeats comes soon (a
	// third of a lease). C's is long: C must settle within it, however
	// slow the machine.
	worker := func(id string, lease time.Duration) *Worker {
		w := &Worker{
			Store: s,
			ID:    id,
			Lease: lease,
			Handlers: map[string]workflow.Handler{
				"action": func(ctx context.Context, job workflow.Job) (any, error) {
					if job.Step == "a" {
						runsOfA.Add(1)
					}
					return job.Step + " by " + id, nil
				},
			},
		}
		w.defaults()
		return w
	}
	claimOne := func(w *Worker) claimedJob {
		t.Helper()
		c, err := w.claim(ctx, 1)
		require.NoError(t, err)
		require.Len(t, c, 1)
		require.Equal(t, workflow.StepID("main/a#1"), c[0].StepID)
		return c[0]
	}
	stored := func() (claims int, fence int64) {
		t.Helper()
		j, ok := s.job(t, "aba", "main/a#1", "step")
		require.True(t, ok)
		return j.Claims, j.Fence
	}
	// heartbeatIsLost runs one of A's heartbeats and reports whether it
	// found its claim gone.
	heartbeatIsLost := func(w *Worker, c claimedJob) bool {
		t.Helper()
		lost := make(chan struct{})
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		w.heartbeat(hctx, c, func(error) { close(lost) })
		select {
		case <-lost:
			return true
		default:
			return false
		}
	}

	a := worker("a", 300*time.Millisecond)
	ca := claimOne(a)
	require.Equal(t, 1, ca.Claims)
	require.Equal(t, int64(1), ca.Fence)
	expireLeases(t, s, "aba") // A stalls past its lease

	b := worker("b", time.Minute)
	cb := claimOne(b)
	require.Equal(t, 2, cb.Claims)
	require.Equal(t, int64(2), cb.Fence)
	b.release(cb) // a graceful shutdown
	claims, fence := stored()
	require.Equal(t, 1, claims, "the release refunds its claim")
	require.Equal(t, int64(2), fence, "the release keeps the fence")

	// A's claims count matches the job's again; its fence does not.
	require.True(t, heartbeatIsLost(a, ca), "A's heartbeat extended a lease it no longer holds")

	c := worker("c", time.Minute)
	cc := claimOne(c)
	require.Equal(t, 2, cc.Claims)
	require.Equal(t, int64(3), cc.Fence)
	c.process(ctx, cc)

	require.True(t, heartbeatIsLost(a, ca))
	a.settle(ctx, ca, time.Now(), workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/a#1",
		Output: "stale",
	}, a.Logger)

	require.Equal(t, int32(1), runsOfA.Load())
	events, err := s.Events(ctx, "aba")
	require.NoError(t, err)
	require.Len(t, events, 2, "one completion of main/a#1")
	require.Equal(t, workflow.EventStepCompleted, events[1].Kind)
	require.Equal(t, "a by c", events[1].Output)
	steps, err := s.Steps(ctx, "aba")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCompleted, steps[0].Status)
	require.Equal(t, "a by c", steps[0].Output)
	require.Equal(t, workflow.StepID("main/b#1"), steps[1].StepID)
	require.Equal(t, StepDispatched, steps[1].Status)
	r, err := s.GetRun(ctx, "aba")
	require.NoError(t, err)
	require.Equal(t, []workflow.StepID{"main/b#1"}, r.State.InFlight())
}
