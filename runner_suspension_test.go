package workflow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestRunnerSurfacesSuspendedResult verifies that Runner.Run returns
// cleanly when an execution hard-suspends on a signal wait. The
// result carries Status=Suspended and a populated SuspensionInfo so
// the caller can schedule a resume.
func TestRunnerSurfacesSuspendedResult(t *testing.T) {
	const topic = "runner-wait"

	wf, err := New(Options{
		Name: "runner-suspend",
		Steps: []*Step{
			{Name: "await", Activity: "awaiter", Store: "reply"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()
	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		return Wait(ctx, topic, time.Minute)
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Suspended())
	require.True(t, result.NeedsResume())
	require.False(t, result.Completed())
	require.NotNil(t, result.Suspension)
	require.Equal(t, SuspensionReasonWaitingSignal, result.Suspension.Reason)
	require.Contains(t, result.Suspension.Topics, topic)
}

// TestRunnerSurfacesPausedResult verifies that Runner.Run returns a
// Paused result when a path hits a declarative Pause step.
func TestRunnerSurfacesPausedResult(t *testing.T) {
	wf, err := New(Options{
		Name: "runner-pause",
		Steps: []*Step{
			{
				Name:  "gate",
				Pause: &PauseConfig{Reason: "hold"},
				Next:  []*Edge{{Step: "after"}},
			},
			{Name: "after", Activity: "noop"},
		},
	})
	require.NoError(t, err)

	noop := NewActivityFunction("noop", func(ctx Context, p map[string]any) (any, error) { return "ok", nil })

	exec, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{noop},
		Checkpointer: newSpikeMemoryCheckpointer(),
	})
	require.NoError(t, err)

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{})
	require.NoError(t, err)
	require.True(t, result.Paused())
	require.True(t, result.NeedsResume())
	require.False(t, result.Completed())
	require.Equal(t, SuspensionReasonPaused, result.Suspension.Reason)
}

// TestRunnerDoesNotRunCompletionHookOnSuspension verifies the
// completion hook is only invoked for a fully-completed execution,
// not for a suspended or paused one. Consumers depend on this so
// their follow-up workflows only fire when the parent is truly done.
func TestRunnerDoesNotRunCompletionHookOnSuspension(t *testing.T) {
	const topic = "hook-test"

	wf, err := New(Options{
		Name: "runner-hook",
		Steps: []*Step{
			{Name: "await", Activity: "awaiter"},
		},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		return Wait(ctx, topic, time.Minute)
	})

	exec, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: newSpikeMemoryCheckpointer(),
		SignalStore:  signals,
	})
	require.NoError(t, err)

	var hookCalls int32
	runner := NewRunner(RunnerConfig{})
	_, err = runner.Run(context.Background(), exec, RunOptions{
		CompletionHook: func(ctx context.Context, r *ExecutionResult) ([]FollowUpSpec, error) {
			atomic.AddInt32(&hookCalls, 1)
			return nil, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), atomic.LoadInt32(&hookCalls),
		"completion hook must not run when the execution is suspended")
}

// TestRunnerResumeAfterSignalCompletes verifies the full runner
// lifecycle across a suspend/resume cycle: first run suspends, a
// signal is delivered, a second runner invocation (with
// PriorExecutionID) resumes and completes.
func TestRunnerResumeAfterSignalCompletes(t *testing.T) {
	const topic = "full-cycle"

	wf, err := New(Options{
		Name: "runner-resume",
		Steps: []*Step{
			{Name: "await", Activity: "awaiter", Store: "reply"},
		},
		Outputs: []*Output{{Name: "reply", Variable: "reply"}},
	})
	require.NoError(t, err)

	signals := NewMemorySignalStore()
	cp := newSpikeMemoryCheckpointer()
	awaiter := NewActivityFunction("awaiter", func(ctx Context, p map[string]any) (any, error) {
		return Wait(ctx, topic, time.Minute)
	})

	exec1, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
	})
	require.NoError(t, err)
	execID := exec1.ID()

	runner := NewRunner(RunnerConfig{})
	res1, err := runner.Run(context.Background(), exec1, RunOptions{})
	require.NoError(t, err)
	require.True(t, res1.Suspended())

	// Deliver the signal.
	require.NoError(t, signals.Send(context.Background(), execID, topic, "from-consumer"))

	// Second Run with PriorExecutionID → resume.
	exec2, err := NewExecution(ExecutionOptions{
		Workflow:     wf,
		Activities:   []Activity{awaiter},
		Checkpointer: cp,
		SignalStore:  signals,
		ExecutionID:  execID,
	})
	require.NoError(t, err)

	res2, err := runner.Run(context.Background(), exec2, RunOptions{
		PriorExecutionID: execID,
	})
	require.NoError(t, err)
	require.True(t, res2.Completed())
	require.Equal(t, "from-consumer", res2.Outputs["reply"])
}
