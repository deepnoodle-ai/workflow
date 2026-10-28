package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestGeneratedRunIDRoundTrip starts a run without an ID and then reads,
// advances and cancels it by the ID the result names.
// (TestResultNamesTheRun covers GetRun and Deliver; this adds Cancel and
// the run's history.)
func TestGeneratedRunIDRoundTrip(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "approve", "agent": "human", "next": ["wait"]}, {"name": "wait", "agent": "human"}]}`)
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{Definition: def})
	require.NoError(t, err)
	id := res.RunID
	require.NotEqual(t, "", id)

	r, err := s.GetRun(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, r.ID)
	require.Equal(t, []workflow.StepID{"main/approve#1"}, r.State.InFlight())

	res, err = s.Deliver(ctx, id, workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/approve#1"})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, id, res.RunID)
	require.Equal(t, []workflow.StepID{"main/wait#1"}, res.State.InFlight())

	res, err = s.Cancel(ctx, id, "withdrawn")
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, id, res.RunID)
	require.Equal(t, workflow.StatusCancelled, res.State.Status)

	r, err = s.GetRun(ctx, id)
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelled, r.Status)
	require.Equal(t, "withdrawn", r.State.Reason)
	events, err := s.Events(ctx, id)
	require.NoError(t, err)
	require.Len(t, events, 3)
	steps, err := s.Steps(ctx, id)
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCancelled, steps[1].Status)
	require.Equal(t, 0, countJobs(t, s, id))
}

// TestLostCancelResponseKeepsCleanupRunning: a cancel commits, but its
// response is lost and the caller sends the same cancel again. The retry
// changes nothing, and the finally step the first cancel started keeps
// running on its worker. A forced cancel then stops it: the heartbeat finds
// its job gone and cancels the handler's context, the step is reported in
// FinallyErrors as cancelled, and the outcome is still the first cancel's.
// (TestCancelTwiceKeepsCleanup covers the same events without a worker.)
func TestLostCancelResponseKeepsCleanupRunning(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "work", "agent": "x"}],
	  "finally": [{"name": "release", "action": "x"}]}`)
	started := make(chan struct{})
	stopped := make(chan error, 1)
	startWorker(t, &Worker{Store: s, Lease: 300 * time.Millisecond, Handlers: map[string]workflow.Handler{
		"agent": func(ctx context.Context, job workflow.Job) (any, error) { return nil, workflow.ErrDeferred },
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			close(started)
			select {
			case <-ctx.Done():
				stopped <- ctx.Err()
			case <-time.After(10 * time.Second):
			}
			return nil, ctx.Err()
		},
	}})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "lost-response", Definition: def})
	require.NoError(t, err)
	waitFor(t, "work handed off", func() bool { return countJobs(t, s, "lost-response") == 0 })

	res, err := s.Cancel(ctx, "lost-response", "operator")
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusCancelling, res.State.Status)
	<-started

	// The response was lost; the caller retries the identical request.
	res, err = s.Cancel(ctx, "lost-response", "operator")
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Len(t, res.Commands, 0)
	require.Equal(t, workflow.StatusCancelling, res.State.Status)
	require.Equal(t, []workflow.StepID{"finally/release#1"}, res.State.InFlight())
	// Cleanup keeps running past several heartbeats.
	select {
	case err := <-stopped:
		t.Fatalf("the retried cancel stopped cleanup: %v", err)
	case <-time.After(400 * time.Millisecond):
	}
	require.Equal(t, 1, countJobs(t, s, "lost-response"))

	res, err = s.Deliver(ctx, "lost-response", workflow.Event{Kind: workflow.EventCancel, Reason: "stop cleanup", Force: true})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusCancelled, res.State.Status)
	select {
	case err := <-stopped:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("the finally handler was not cancelled")
	}

	r := waitStatus(t, s, "lost-response", workflow.StatusCancelled)
	require.Equal(t, "operator", r.State.Reason, "the first cancel decided the outcome")
	require.Len(t, r.State.FinallyErrors, 1)
	fe := r.State.FinallyErrors[0]
	require.Equal(t, "release", fe.Step)
	require.Equal(t, workflow.StepID("finally/release#1"), fe.StepID)
	require.Equal(t, workflow.ErrorTypeCancelled, fe.Type)
	steps, err := s.Steps(ctx, "lost-response")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCancelled, steps[1].Status)
	require.Equal(t, 0, countJobs(t, s, "lost-response"))

	// Another retry, after the end, still changes nothing.
	res, err = s.Cancel(ctx, "lost-response", "operator")
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, workflow.StatusCancelled, res.State.Status)
}
