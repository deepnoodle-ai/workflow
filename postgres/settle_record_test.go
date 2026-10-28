package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/jackc/pgx/v5"
)

// TestDeliverOfADeferredStepLeavesNoJob covers a worker that claimed a
// deferred step and died before marking its job done. The result then
// arrives through Deliver; the orphaned job must go with it, or another
// worker would run the handler again (a second approval request).
func TestDeliverOfADeferredStepLeavesNoJob(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [
	  {"name": "approve", "agent": "human", "timeout": "1h", "next": ["ship"]},
	  {"name": "ship", "task": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "orphan", Definition: def})
	require.NoError(t, err)

	dead := &Worker{Store: s, ID: "dead", Lease: 100 * time.Millisecond, Handlers: map[string]Handler{"agent": nil}}
	dead.defaults()
	claimed, err := dead.claim(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	res, err := s.Deliver(ctx, "orphan", workflow.Event{Kind: workflow.EventStepCompleted,
		StepID: "main/approve#1", Output: "approved"})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusRunning, res.State.Status)
	var left []string
	rows, err := s.pool.Query(ctx, s.sql(`SELECT step_id || ' ' || kind FROM {{schema}}.jobs WHERE run_id = 'orphan' ORDER BY 1`))
	require.NoError(t, err)
	left, err = pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	require.Equal(t, []string{"main/ship#1 step"}, left)
}

// TestTimedOutAttemptThatRetriesIsRecordedFailed checks the step record of
// an attempt that timed out and was retried.
func TestTimedOutAttemptThatRetriesIsRecordedFailed(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "w", "agent": "x", "timeout": "150ms",
	  "retry": {"max_attempts": 2, "backoff": "10ms"}}]}`)
	startWorker(t, &Worker{Store: s, Handlers: map[string]Handler{
		"agent": func(ctx context.Context, job Job) (any, error) { return nil, ErrDeferred },
	}})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "retry-timeout", Definition: def})
	require.NoError(t, err)
	waitStatus(t, s, "retry-timeout", workflow.StatusFailed)
	steps, err := s.Steps(ctx, "retry-timeout")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	for _, st := range steps {
		require.Equal(t, "failed", st.Status, st.StepID)
		require.NotNil(t, st.Error, st.StepID)
		require.Equal(t, workflow.ErrorTypeTimeout, st.Error.Type, st.StepID)
	}
	require.Equal(t, 0, countJobs(t, s, "retry-timeout"))
}

// TestOutcomeWithoutAUnit covers an event whose unit has left the state in
// the same Apply: the record still settles, from the event.
func TestOutcomeWithoutAUnit(t *testing.T) {
	def := load(t, `{"steps": [{"name": "nap", "sleep": "1s", "next": ["w"]}, {"name": "w", "agent": "x", "timeout": "1m"}]}`)
	gone := &workflow.State{}
	for _, c := range []struct {
		ev      workflow.Event
		status  string
		errType string
	}{
		{workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/w@2#1", Output: "x"}, "completed", ""},
		{workflow.Event{Kind: workflow.EventStepFailed, StepID: "main/w#1", Error: &workflow.StepError{Type: "boom"}}, "failed", "boom"},
		{workflow.Event{Kind: workflow.EventTimerFired, StepID: "main.x/nap@3#1"}, "completed", ""},
		{workflow.Event{Kind: workflow.EventTimerFired, StepID: "main/w[2]#4"}, "failed", workflow.ErrorTypeTimeout},
	} {
		status, _, stepErr := attemptOutcome(def, gone, c.ev)
		require.Equal(t, c.status, status, c.ev.StepID)
		if c.errType == "" {
			require.Nil(t, stepErr)
		} else {
			require.Equal(t, c.errType, stepErr.Type)
		}
	}
	require.Equal(t, "verify", stepName("main.tests/verify@2[3]#1"))
}
