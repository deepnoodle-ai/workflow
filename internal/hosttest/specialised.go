package hosttest

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// runWorker runs a worker until the returned stop is called (or the test
// ends) and returns once it has stopped.
func runWorker(t *testing.T, w *Worker) (stop func()) {
	t.Helper()
	if w.Poll == 0 {
		w.Poll = 20 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}

// TestSpecialisedWorkersShareOneOutbox extends
// TestWorkersWithDifferentHandlers, which splits agent and action steps
// across workers, to timers and to a kind no worker handles. Every worker
// claims timers, so a sleep completes with only the agent worker running,
// and again with only the action worker running. A step whose kind no
// worker handles is never claimed; its timeout fails it.
func testSpecialisedWorkersShareOneOutbox(t *testing.T, h Harness) {
	base := open(t, h)
	k := workflow.KindMap{"agent": nil, "action": nil, "approval": nil}
	s := storeOn(t, base, k)
	ctx := context.Background()
	agentWorker := func() *Worker {
		return &Worker{
			Store: s,
			ID:    "agents",
			Handlers: map[string]workflow.Handler{
				"agent": func(context.Context, workflow.Job) (any, error) { return "thought", nil },
			},
		}
	}
	actionWorker := func() *Worker {
		return &Worker{
			Store: s,
			ID:    "actions",
			Handlers: map[string]workflow.Handler{
				"action": func(context.Context, workflow.Job) (any, error) { return "acted", nil },
			},
		}
	}
	sleepThen := func(runID, kind string) {
		t.Helper()
		def := loadKinds(t, `{"steps": [{"name": "nap", "sleep": "5ms", "next": ["work"]}, {"name": "work", "`+kind+`": "x"}]}`, k)
		_, err := s.Start(ctx, NewRun{ID: runID, Definition: def})
		require.NoError(t, err)
		r := waitStatus(t, s, runID, workflow.StatusCompleted, workflow.StatusFailed)
		require.Equal(t, workflow.StatusCompleted, r.Status, "%v", r.State.Error)
		steps, err := s.Steps(ctx, runID)
		require.NoError(t, err)
		require.Len(t, steps, 2)
		require.Equal(t, workflow.StepID("main/nap#1"), steps[0].StepID)
		require.Equal(t, StepCompleted, steps[0].Status)
	}

	// Only the agent worker: it fires the timer and runs the agent step.
	stopAgents := runWorker(t, agentWorker())
	sleepThen("agents-only", "agent")
	stopAgents()

	// Only the action worker: it fires the timer too.
	stopActions := runWorker(t, actionWorker())
	sleepThen("actions-only", "action")

	// Both: an approval step no worker handles waits in the outbox until
	// its timeout timer, which either worker fires, fails it.
	runWorker(t, agentWorker())
	def := loadKinds(t, `{"steps": [
	  {"name": "think", "agent": "x", "next": ["sign"]},
	  {"name": "sign", "approval": "x", "timeout": "150ms", "next": ["act"]},
	  {"name": "act", "action": "x"}]}`, k)
	_, err := s.Start(ctx, NewRun{ID: "unhandled", Definition: def})
	require.NoError(t, err)
	r := waitStatus(t, s, "unhandled", workflow.StatusFailed, workflow.StatusCompleted)
	require.Equal(t, workflow.StatusFailed, r.Status)
	require.Equal(t, workflow.ErrorTypeTimeout, r.State.Error.Type)
	require.Equal(t, workflow.StepID("main/sign#1"), r.State.Error.StepID)
	steps, err := s.Steps(ctx, "unhandled")
	require.NoError(t, err)
	require.Len(t, steps, 2)
	require.Equal(t, StepCompleted, steps[0].Status)
	require.Equal(t, workflow.StepID("main/sign#1"), steps[1].StepID)
	require.Equal(t, "approval", steps[1].StepKind)
	require.Equal(t, StepFailed, steps[1].Status)
	require.Equal(t, workflow.ErrorTypeTimeout, steps[1].Error.Type)
	require.Equal(t, 0, countJobs(t, s, "unhandled"), "the unclaimed step job went with the run")
	stopActions()
}
