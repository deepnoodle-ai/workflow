package hosttest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// approvalDoc holds a blocked deploy for a person to approve.
const approvalDoc = `{"steps": [
  {"name": "deploy", "action": "deploy", "with": {"target": "prod"},
   "approval": {"errors": ["guard_blocked"], "timeout": "%s"}}]}`

// guardedWorker starts a worker whose deploy is blocked unless approved,
// and whose approval handler hands each job off and sends it on asked.
func guardedWorker(t *testing.T, s *Env, deploys *atomic.Int32) <-chan workflow.Job {
	asked := make(chan workflow.Job, 4)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if !job.Approved {
					return nil, &workflow.StepError{Type: "guard_blocked", Message: "flagged"}
				}
				deploys.Add(1)
				return map[string]any{"deployed": job.Params["target"]}, nil
			},
			workflow.KindApproval: func(ctx context.Context, job workflow.Job) (any, error) {
				asked <- job
				return nil, workflow.ErrDeferred
			},
		},
	})
	return asked
}

// stepRecords returns a run's step records by StepID.
func stepRecords(t *testing.T, s *Env, runID string) map[workflow.StepID]durable.StepRecord {
	t.Helper()
	records, err := s.Steps(context.Background(), runID)
	require.NoError(t, err)
	out := map[workflow.StepID]durable.StepRecord{}
	for _, rec := range records {
		out[rec.StepID] = rec
	}
	return out
}

// testApprovalHoldApproveRun holds a blocked job, takes a person's yes by
// step name, runs the same job approved, and records all three attempts.
func testApprovalHoldApproveRun(t *testing.T, h Harness) {
	s := open(t, h)
	var deploys atomic.Int32
	asked := guardedWorker(t, s, &deploys)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "held", Definition: load(t, fmt.Sprintf(approvalDoc, "1h"))})
	require.NoError(t, err)

	job := await(t, "the approval job is dispatched", asked)
	require.Equal(t, workflow.StepID("main/deploy.approval#1"), job.StepID)
	require.Equal(t, map[string]any{"target": "prod"}, job.Params)
	require.Equal(t, "guard_blocked", job.Hold.Error.Type)

	answer := workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "deploy",
		Output: map[string]any{"approved": true, "reason": "release 4.2"},
	}
	_, err = s.Deliver(ctx, "held", answer)
	require.NoError(t, err)
	r := waitStatus(t, s, "held", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	require.Equal(t, int32(1), deploys.Load())

	// A repeated answer by name is refused: it never reaches the deploy.
	_, err = s.Deliver(ctx, "held", answer)
	require.ErrorIs(t, err, workflow.ErrNotWaiting)

	recs := stepRecords(t, s, "held")
	require.Len(t, recs, 3)
	held := recs["main/deploy#1"]
	require.Equal(t, StepFailed, held.Status)
	require.Equal(t, "guard_blocked", held.Error.Type)
	approval := recs["main/deploy.approval#1"]
	require.Equal(t, workflow.KindApproval, approval.StepKind)
	require.Equal(t, "deploy", approval.Step)
	require.Equal(t, StepCompleted, approval.Status)
	require.Equal(t, map[string]any{"target": "prod"}, approval.Params)
	require.Equal(t, map[string]any{"approved": true, "reason": "release 4.2"}, approval.Output)
	approved := recs["main/deploy#2"]
	require.Equal(t, StepCompleted, approved.Status)
	require.Equal(t, map[string]any{"deployed": "prod"}, approved.Output)
	require.Empty(t, s.jobs(t, "held"))
}

// testApprovalTimeoutEndsTheHold fails a hold nobody answers with the
// blocking error, and refuses a late answer.
func testApprovalTimeoutEndsTheHold(t *testing.T, h Harness) {
	s := open(t, h)
	var deploys atomic.Int32
	asked := guardedWorker(t, s, &deploys)
	ctx := context.Background()
	def := load(t, fmt.Sprintf(approvalDoc, "100ms"))
	_, err := s.Start(ctx, NewRun{ID: "unanswered", Definition: def})
	require.NoError(t, err)
	await(t, "the approval job is dispatched", asked)

	r := waitStatus(t, s, "unanswered", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, "guard_blocked", r.State.Error.Type)
	require.Equal(t, "flagged; not approved within 100ms", r.State.Error.Message)
	require.Equal(t, int32(0), deploys.Load())

	_, err = s.Deliver(ctx, "unanswered", workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "deploy",
		Output: map[string]any{"approved": true},
	})
	require.ErrorIs(t, err, workflow.ErrNotWaiting)
	recs := stepRecords(t, s, "unanswered")
	require.Equal(t, StepFailed, recs["main/deploy#1"].Status)
	require.Equal(t, StepFailed, recs["main/deploy.approval#1"].Status)
	require.Equal(t, workflow.ErrorTypeTimeout, recs["main/deploy.approval#1"].Error.Type)
}

// testApprovalDuringAPause takes a yes while the run is paused: the
// approved job is held until Resume, then runs once and the run completes.
func testApprovalDuringAPause(t *testing.T, h Harness) {
	s := open(t, h)
	var deploys atomic.Int32
	asked := guardedWorker(t, s, &deploys)
	ctx := context.Background()
	def := load(t, fmt.Sprintf(approvalDoc, "1h"))
	_, err := s.Start(ctx, NewRun{ID: "paused", Definition: def})
	require.NoError(t, err)
	await(t, "the approval job is dispatched", asked)

	require.True(t, pause(t, s, "paused", "freeze"))
	_, err = s.Deliver(ctx, "paused", workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "deploy",
		Output: map[string]any{"approved": true},
	})
	require.NoError(t, err)
	r, err := s.GetRun(ctx, "paused")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusPaused, r.Status)
	require.Equal(t, []workflow.StepID{"main/deploy#2"}, r.State.InFlight())
	time.Sleep(100 * time.Millisecond) // a few polls: the held job stays put
	require.Equal(t, int32(0), deploys.Load())

	require.True(t, resume(t, s, "paused"))
	r = waitStatus(t, s, "paused", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, r.Status)
	require.Equal(t, int32(1), deploys.Load())
	recs := stepRecords(t, s, "paused")
	require.Len(t, recs, 3)
	require.Equal(t, StepCompleted, recs["main/deploy#2"].Status)
}
