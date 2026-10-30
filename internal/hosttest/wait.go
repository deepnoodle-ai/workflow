package hosttest

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestWaitStepThroughDeliverByName covers an approval gate: the wait has
// no step job, only its timeout timer; the application delivers to it by
// step name; a second delivery is a duplicate; and a wait nobody answers
// fails with a timeout.
func testWaitStepThroughDeliverByName(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [
	  {"name": "approve", "wait": true, "timeout": "1h",
	   "output": {"type": "object", "required": ["by"], "properties": {"by": {"type": "string"}}},
	   "next": ["ship"]},
	  {"name": "ship", "action": "x", "with": {"by": "${{ approve.by }}"}, "next": ["confirm"]},
	  {"name": "confirm", "wait": true, "timeout": "150ms"}]}`)
	shipped := make(chan string, 1)
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				shipped <- job.Params["by"].(string)
				return nil, nil
			},
		},
	})
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "gate", Definition: def})
	require.NoError(t, err)

	// The wait is only a timer: no worker claims a step job for it.
	var jobs []string
	for _, j := range s.jobs(t, "gate") {
		jobs = append(jobs, string(j.StepID)+" "+j.Kind+" "+j.StepKind)
	}
	require.Equal(t, []string{"main/approve#1 timer wait"}, jobs)

	// A name the run has not reached, and one that is not a step.
	_, err = s.Deliver(ctx, "gate", workflow.Event{Kind: workflow.EventStepCompleted, StepID: "confirm"})
	require.ErrorIs(t, err, workflow.ErrNotWaiting)
	_, err = s.Deliver(ctx, "gate", workflow.Event{Kind: workflow.EventStepCompleted, StepID: "aprove"})
	require.ErrorIs(t, err, workflow.ErrUnknownStep)

	approval := workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "approve",
		Output: map[string]any{"by": "curtis"},
	}
	res, err := s.Deliver(ctx, "gate", approval)
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, "curtis", await(t, "the ship step runs", shipped))

	// The webhook delivers again: a duplicate, not an error.
	res, err = s.Deliver(ctx, "gate", approval)
	require.NoError(t, err)
	require.False(t, res.Applied)

	// Nobody confirms: the wait times out and fails the run.
	r := waitStatus(t, s, "gate", workflow.StatusFailed)
	require.Equal(t, workflow.ErrorTypeTimeout, r.State.Error.Type)
	require.Equal(t, "confirm", r.State.Error.Step)

	// The records hold full StepIDs.
	records, err := s.Steps(ctx, "gate")
	require.NoError(t, err)
	got := map[workflow.StepID]durable.StepStatus{}
	for _, rec := range records {
		if rec.StepKind == workflow.KindWait {
			got[rec.StepID] = rec.Status
		}
	}
	require.Equal(t, map[workflow.StepID]durable.StepStatus{
		"main/approve#1": durable.StepCompleted,
		"main/confirm#1": durable.StepFailed,
	}, got)
}
