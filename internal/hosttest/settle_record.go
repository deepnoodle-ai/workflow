package hosttest

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestDeliverOfADeferredStepLeavesNoJob covers a worker that claimed a
// deferred step and died before marking its job done. The result then
// arrives through Deliver; the orphaned job must go with it, or another
// worker would run the handler again (a second approval request).
func testDeliverOfADeferredStepLeavesNoJob(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [
	  {"name": "approve", "agent": "human", "timeout": "1h", "next": ["ship"]},
	  {"name": "ship", "action": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "orphan", Definition: def})
	require.NoError(t, err)

	dead := &Worker{
		Store:    s,
		ID:       "dead",
		Lease:    100 * time.Millisecond,
		Handlers: map[string]workflow.Handler{"agent": nil},
	}
	dead.defaults()
	claimed, err := dead.claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	res, err := s.Deliver(ctx, "orphan", workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/approve#1",
		Output: "approved",
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusRunning, res.State.Status)
	var left []string
	for _, j := range s.jobs(t, "orphan") {
		left = append(left, string(j.StepID)+" "+j.Kind)
	}
	require.Equal(t, []string{"main/ship#1 step"}, left)
}

// TestTimedOutAttemptThatRetriesIsRecordedFailed checks the step record of
// an attempt that timed out and was retried.
func testTimedOutAttemptThatRetriesIsRecordedFailed(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "w", "agent": "x", "timeout": "150ms",
	  "retry": {"max_attempts": 2, "backoff": "10ms"}}]}`)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				return nil, workflow.ErrDeferred
			},
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "retry-timeout", Definition: def})
	require.NoError(t, err)
	waitStatus(t, s, "retry-timeout", workflow.StatusFailed)
	steps, err := s.Steps(ctx, "retry-timeout")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	for _, st := range steps {
		require.Equal(t, StepFailed, st.Status, st.StepID)
		require.NotNil(t, st.Error, st.StepID)
		require.Equal(t, workflow.ErrorTypeTimeout, st.Error.Type, st.StepID)
	}
	require.Equal(t, 0, countJobs(t, s, "retry-timeout"))
}
