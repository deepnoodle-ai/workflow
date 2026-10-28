package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestTwoVersionsSideBySide starts run A on v1 and run B on v2 of the
// same-named workflow, each paused at a deferred step. A second Store with
// an empty cache, and a worker on it, advance both: each run follows the
// definition it was started with, and a v1 StepID delivered to run B is
// rejected without changing it.
func TestTwoVersionsSideBySide(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	v1 := load(t, `{"name": "release", "steps": [
	  {"name": "ask", "agent": "human", "next": ["ship"]},
	  {"name": "ship", "action": "x"}]}`)
	v2 := load(t, `{"name": "release", "steps": [
	  {"name": "triage", "agent": "human", "next": ["approve"]},
	  {"name": "approve", "agent": "human", "next": ["canary"]},
	  {"name": "canary", "action": "x"}]}`)
	require.NotEqual(t, v1.Hash(), v2.Hash())
	_, err := s.Start(ctx, NewRun{ID: "a", Definition: v1})
	require.NoError(t, err)
	_, err = s.Start(ctx, NewRun{ID: "b", Definition: v2})
	require.NoError(t, err)

	// Another process: it has loaded neither version.
	cold := storeOn(t, s, kinds)
	var (
		mu    sync.Mutex
		calls = map[string][]workflow.StepID{}
	)
	record := func(job workflow.Job) {
		mu.Lock()
		calls[job.RunID] = append(calls[job.RunID], job.StepID)
		mu.Unlock()
	}
	startWorker(t, &Worker{
		Store: cold,
		Handlers: map[string]workflow.Handler{
			"agent": func(_ context.Context, job workflow.Job) (any, error) {
				record(job)
				return nil, workflow.ErrDeferred
			},
			"action": func(_ context.Context, job workflow.Job) (any, error) {
				record(job)
				return job.Step, nil
			},
		},
	})
	handedOff := func(runID string) {
		t.Helper()
		waitFor(t, runID+" handed off", func() bool { return countJobs(t, s, runID) == 0 })
	}
	handedOff("a")
	handedOff("b")

	// A v1 StepID means nothing to run B.
	before, err := cold.GetRun(ctx, "b")
	require.NoError(t, err)
	_, err = cold.Deliver(ctx, "b",
		workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/ask#1",
			Output: "yes",
		})
	require.ErrorIs(t, err, workflow.ErrUnknownStep)
	after, err := cold.GetRun(ctx, "b")
	require.NoError(t, err)
	require.Equal(t, before.State.InFlight(), after.State.InFlight())
	require.Equal(t, before.UpdatedAt, after.UpdatedAt)
	events, err := cold.Events(ctx, "b")
	require.NoError(t, err)
	require.Len(t, events, 1, "the rejected event was not recorded")

	// Each run advances on its own version.
	_, err = cold.Deliver(ctx, "a",
		workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/ask#1",
			Output: "yes",
		})
	require.NoError(t, err)
	_, err = cold.Deliver(ctx, "b",
		workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/triage#1",
			Output: "p1",
		})
	require.NoError(t, err)
	handedOff("b")
	_, err = cold.Deliver(ctx, "b",
		workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/approve#1",
			Output: "yes",
		})
	require.NoError(t, err)

	ra := waitStatus(t, cold, "a", workflow.StatusCompleted, workflow.StatusFailed)
	rb := waitStatus(t, cold, "b", workflow.StatusCompleted, workflow.StatusFailed)
	require.Equal(t, workflow.StatusCompleted, ra.Status)
	require.Equal(t, workflow.StatusCompleted, rb.Status)
	require.Equal(t, v1.Hash(), ra.DefinitionHash)
	require.Equal(t, v2.Hash(), rb.DefinitionHash)
	require.Equal(t, "release", ra.Workflow)
	require.Equal(t, "release", rb.Workflow)
	require.Equal(t, map[string]any{"ship": "ship"}, ra.State.Outputs)
	require.Equal(t, map[string]any{"canary": "canary"}, rb.State.Outputs)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []workflow.StepID{"main/ask#1", "main/ship#1"}, calls["a"])
	require.Equal(t, []workflow.StepID{"main/triage#1", "main/approve#1", "main/canary#1"}, calls["b"])
	for runID, want := range map[string][]string{
		"a": {"main/ask#1", "main/ship#1"},
		"b": {"main/triage#1", "main/approve#1", "main/canary#1"},
	} {
		steps, err := cold.Steps(ctx, runID)
		require.NoError(t, err)
		var got []string
		for _, st := range steps {
			got = append(got, string(st.StepID))
		}
		require.Equal(t, want, got, runID)
	}
}
