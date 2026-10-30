package hosttest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// retryThenCharge fails fetch's first attempt, retries it after a short
// backoff, then runs charge.
const retryThenCharge = `{"steps": [
  {"name": "fetch", "action": "x", "next": ["charge"],
   "retry": {"max_attempts": 2, "backoff": "50ms"}},
  {"name": "charge", "action": "x"}]}`

// failFirst returns handlers that fail the first call of each step with a
// retryable error, and the StepIDs they were called with.
func failFirst() (map[string]workflow.Handler, func() []workflow.StepID) {
	var (
		mu    sync.Mutex
		calls []workflow.StepID
	)
	h := func(_ context.Context, job workflow.Job) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, job.StepID)
		if job.Attempt == 1 && job.Step == "fetch" {
			return nil, &workflow.StepError{Type: "down", Message: "down", Retryable: true}
		}
		return "ok", nil
	}
	return map[string]workflow.Handler{"action": h}, func() []workflow.StepID {
		mu.Lock()
		defer mu.Unlock()
		return append([]workflow.StepID(nil), calls...)
	}
}

// Pauser is the public Pause and Resume of a backend's Store. A harness's
// Env.Handle implements it, so the suite calls the methods users call.
type Pauser interface {
	Pause(ctx context.Context, runID, reason string) (*Result, error)
	Resume(ctx context.Context, runID string) (*Result, error)
}

func pauser(t *testing.T, s *Env) Pauser {
	t.Helper()
	p, ok := s.Handle.(Pauser)
	require.True(t, ok, "%T does not implement hosttest.Pauser", s.Handle)
	return p
}

func pause(t *testing.T, s *Env, runID, reason string) bool {
	t.Helper()
	res, err := pauser(t, s).Pause(context.Background(), runID, reason)
	require.NoError(t, err)
	return res.Applied
}

func resume(t *testing.T, s *Env, runID string) bool {
	t.Helper()
	res, err := pauser(t, s).Resume(context.Background(), runID)
	require.NoError(t, err)
	return res.Applied
}

// TestPauseHoldsStepsUntilResume: a retry that waits for its backoff when
// the run pauses is not claimed once due, by RunOnce or Execute, and is not
// counted as due. Resume wakes a worker that would otherwise sleep for its
// whole Poll, and the run completes.
func testPauseHoldsStepsUntilResume(t *testing.T, h Harness) {
	ctx := context.Background()
	s := open(t, h)
	startRuns(t, s, retryThenCharge, "r")
	handlers, calls := failFirst()
	w := &Worker{Store: s, Handlers: handlers}
	require.NoError(t, runOnceWithin(t, w, ctx, 10*time.Second))
	require.Equal(t, []workflow.StepID{"main/fetch#1"}, calls())

	require.True(t, pause(t, s, "r", "payment API down"))
	require.False(t, pause(t, s, "r", "again"))
	run, err := s.GetRun(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusPaused, run.Status)
	require.Equal(t, "payment API down", run.State.Reason)

	time.Sleep(100 * time.Millisecond) // past the backoff
	require.NoError(t, runOnceWithin(t, w, ctx, 10*time.Second))
	require.Equal(t, []workflow.StepID{"main/fetch#1"}, calls())
	_, err = w.Execute(ctx, JobRef{RunID: "r", StepID: "main/fetch#2"})
	require.ErrorIs(t, err, ErrNotClaimable)
	require.True(t, strings.Contains(err.Error(), "its run is paused"), err.Error())
	stats, err := s.QueueStats(ctx)
	require.NoError(t, err)
	for _, q := range stats {
		require.Equal(t, 0, q.Due, "queue %s/%s", q.Kind, q.StepKind)
	}

	// A worker that has claimed nothing sleeps for its Poll; Resume wakes it.
	w = &Worker{Store: s, Handlers: handlers, Poll: time.Hour}
	goRun(t, w)
	time.Sleep(50 * time.Millisecond) // let its first claim find nothing
	require.True(t, resume(t, s, "r"))
	require.False(t, resume(t, s, "r"))
	waitUntil(t, "the run completes", func() bool {
		return runStatus(t, s, "r") == workflow.StatusCompleted
	})
	require.Equal(t, []workflow.StepID{"main/fetch#1", "main/fetch#2", "main/charge#1"}, calls())

	events, err := s.Events(ctx, "r")
	require.NoError(t, err)
	var kinds []workflow.EventKind
	for _, ev := range events {
		if ev.Kind == workflow.EventPause || ev.Kind == workflow.EventResume {
			kinds = append(kinds, ev.Kind)
		}
	}
	require.Equal(t, []workflow.EventKind{workflow.EventPause, workflow.EventResume}, kinds)
}

// TestPauseStopsTheTimeoutClock: a retry is pending when the run pauses,
// and the pause outlasts the step's timeout. The timer is claimed and
// fires, but uses up no attempt: the handler is not called and the run
// stays paused. Resume re-arms the timeout, and the run completes.
func testPauseStopsTheTimeoutClock(t *testing.T, h Harness) {
	ctx := context.Background()
	s := open(t, h)
	startRuns(t, s, `{"steps": [
	  {"name": "fetch", "action": "x", "next": ["charge"], "timeout": "50ms",
	   "retry": {"max_attempts": 2, "backoff": "10ms"}},
	  {"name": "charge", "action": "x"}]}`, "r")
	handlers, calls := failFirst()
	w := &Worker{Store: s, Handlers: handlers}
	require.NoError(t, runOnceWithin(t, w, ctx, 10*time.Second))
	require.True(t, pause(t, s, "r", "look"))

	time.Sleep(150 * time.Millisecond) // past the backoff and the timeout
	require.NoError(t, runOnceWithin(t, w, ctx, 10*time.Second))
	require.Equal(t, []workflow.StepID{"main/fetch#1"}, calls())
	require.Equal(t, workflow.StatusPaused, runStatus(t, s, "r"))
	var timers int
	for _, j := range s.jobs(t, "r") {
		if j.Kind == "timer" {
			timers++
		}
	}
	require.Equal(t, 0, timers) // fired, and held until Resume

	res, err := pauser(t, s).Resume(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, []JobRef{{
		RunID:    "r",
		StepID:   "main/fetch#2",
		Timer:    true,
		StepKind: "action",
		RunAt:    res.Commands[0].NotBefore,
	}}, res.Jobs())
	require.NoError(t, runOnceWithin(t, w, ctx, 10*time.Second))
	require.Equal(t, workflow.StatusCompleted, runStatus(t, s, "r"))
	require.Equal(t, []workflow.StepID{"main/fetch#1", "main/fetch#2", "main/charge#1"}, calls())
}

// TestCancelAPausedRun: the held step is cancelled without running, the
// finally step runs, and the run ends cancelled with the cancel's reason.
func testCancelAPausedRun(t *testing.T, h Harness) {
	ctx := context.Background()
	s := open(t, h)
	startRuns(t, s, `{
	  "steps": [{"name": "a", "action": "x"}],
	  "finally": [{"name": "release", "action": "x"}]}`, "r")
	require.True(t, pause(t, s, "r", "look"))
	res, err := s.Cancel(ctx, "r", "give up")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelling, res.State.Status)

	handlers, calls := failFirst()
	w := &Worker{Store: s, Handlers: handlers}
	require.NoError(t, runOnceWithin(t, w, ctx, 10*time.Second))
	require.Equal(t, []workflow.StepID{"finally/release#1"}, calls())
	run, err := s.GetRun(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelled, run.Status)
	require.Equal(t, "give up", run.State.Reason)
	steps, err := s.Steps(ctx, "r")
	require.NoError(t, err)
	require.Equal(t, "cancelled", string(steps[0].Status))
}
