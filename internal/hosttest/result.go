package hosttest

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestDeliverReportsApplied lets an HTTP handler tell a new approval from a
// retried one.
func testDeliverReportsApplied(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{ID: "applied", Definition: def})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Len(t, res.Commands, 1)
	res, err = s.Start(ctx, NewRun{ID: "applied", Definition: def})
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Len(t, res.Commands, 0)

	ev := workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/approve#1",
		Output: "yes",
	}
	res, err = s.Deliver(ctx, "applied", ev)
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusCompleted, res.State.Status)
	res, err = s.Deliver(ctx, "applied", ev)
	require.NoError(t, err)
	require.False(t, res.Applied)
	require.Equal(t, workflow.StatusCompleted, res.State.Status)

	res, err = s.Cancel(ctx, "applied", "too late")
	require.NoError(t, err)
	require.False(t, res.Applied)
}

// TestFinallyAndTheFinishHook runs a cancelled run's finally steps through the
// outbox and workers: the run is cancelling until they are done, and the
// hook sees the run finish once, with the cleanup that failed.
func testFinallyAndTheFinishHook(t *testing.T, h Harness) {
	base := open(t, h)
	var (
		mu       sync.Mutex
		finished []Finished
	)
	s, err := newOn(t, base, kinds,
		WithOnFinished(func(ctx context.Context, f Finished) error {
			mu.Lock()
			finished = append(finished, f)
			mu.Unlock()
			return nil
		}))
	require.NoError(t, err)
	ctx := context.Background()
	def := load(t, `{
	  "steps": [{"name": "wait", "agent": "x"}],
	  "finally": [
	    {"name": "release", "action": "x", "with": {"status": "${{ run.status }}"}},
	    {"name": "notify", "action": "x", "with": {"released": "${{ release?.error == nil }}"}}]}`)
	_, err = s.Start(ctx, NewRun{ID: "fin", Definition: def})
	require.NoError(t, err)
	res, err := s.Cancel(ctx, "fin", "superseded")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelling, res.State.Status)
	run, err := s.GetRun(ctx, "fin")
	require.NoError(t, err)
	require.Equal(t, workflow.StatusCancelling, run.Status)

	var params []map[string]any
	startWorker(t, &Worker{
		Store: s,
		Handlers: map[string]workflow.Handler{
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				mu.Lock()
				params = append(params, job.Command.Params)
				mu.Unlock()
				if job.Command.Step == "release" {
					return nil, &workflow.StepError{Type: "locked", Message: "still held"}
				}
				return "ok", nil
			},
		},
	})
	run = waitStatus(t, s, "fin", workflow.StatusCancelled)
	require.Len(t, run.State.FinallyErrors, 1)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []map[string]any{{"status": "cancelled"}, {"released": false}}, params)
	require.Len(t, finished, 1)
	require.Equal(t, workflow.StatusCancelled, finished[0].Status)
	require.Equal(t, "superseded", finished[0].Reason)
	require.Len(t, finished[0].FinallyErrors, 1)
	require.Equal(t, "release", finished[0].FinallyErrors[0].Step)
	require.Equal(t, "locked", finished[0].FinallyErrors[0].Type)
}

// A run started without an ID gets one, and the result names it.
func testResultNamesTheRun(t *testing.T, h Harness) {
	s := open(t, h)
	def := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{Definition: def})
	require.NoError(t, err)
	require.NotEqual(t, "", res.RunID)
	r, err := s.GetRun(ctx, res.RunID)
	require.NoError(t, err)
	require.Equal(t, def.Hash(), r.DefinitionHash)
	res, err = s.Deliver(ctx, res.RunID,
		workflow.Event{Kind: workflow.EventStepCompleted, StepID: "main/approve#1"})
	require.NoError(t, err)
	require.Equal(t, r.ID, res.RunID)
}

// WithRunIDs replaces the generated run IDs with the application's own.
func testWithRunIDs(t *testing.T, h Harness) {
	base := open(t, h)
	n := 0
	s, err := newOn(t, base, kinds, WithRunIDs(func() string {
		n++
		return fmt.Sprintf("order-%d", n)
	}))
	require.NoError(t, err)
	def := load(t, `{"steps": [{"name": "approve", "agent": "human"}]}`)
	ctx := context.Background()
	res, err := s.Start(ctx, NewRun{Definition: def})
	require.NoError(t, err)
	require.Equal(t, "order-1", res.RunID)
	res, err = s.Start(ctx, NewRun{ID: "mine", Definition: def})
	require.NoError(t, err)
	require.Equal(t, "mine", res.RunID, "an explicit ID wins")

	empty, err := newOn(t, base, kinds,
		WithRunIDs(func() string { return "" }))
	require.NoError(t, err)
	_, err = empty.Start(ctx, NewRun{Definition: def})
	require.Error(t, err)
}
