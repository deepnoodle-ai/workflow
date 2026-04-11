package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func newSimpleWorkflow(t *testing.T) *Workflow {
	t.Helper()
	wf, err := New(Options{
		Name: "runner-test",
		Steps: []*Step{
			{Name: "work", Activity: "do_work", Store: "result"},
		},
		Outputs: []*Output{{Name: "result", Variable: "result"}},
	})
	require.NoError(t, err)
	return wf
}

func newSimpleExecution(t *testing.T, wf *Workflow, activityFn func(Context, map[string]any) (any, error)) *Execution {
	t.Helper()
	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("do_work", activityFn),
		},
	})
	require.NoError(t, err)
	return exec
}

func TestRunnerBasicRun(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "hello", nil
	})

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{})
	require.NoError(t, err)
	require.True(t, result.Completed())
	require.Equal(t, "hello", result.Outputs["result"])
}

func TestRunnerTimeoutCancelsExecution(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		// Block until cancelled
		<-ctx.Done()
		return nil, ctx.Err()
	})

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		Timeout: 100 * time.Millisecond,
	})
	// Context cancellation during execution means the execution did start
	// but was interrupted — should return a result with failed status
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Failed())
}

func TestRunnerHeartbeatFailureCancelsExecution(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		// Block until cancelled by heartbeat
		<-ctx.Done()
		return nil, ctx.Err()
	})

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		Heartbeat: &HeartbeatConfig{
			Interval: 50 * time.Millisecond,
			Func: func(ctx context.Context) error {
				return fmt.Errorf("lease lost")
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Failed())
}

func TestRunnerCompletionHookProducesFollowUps(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "done", nil
	})

	hookCalled := false
	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		CompletionHook: func(ctx context.Context, r *ExecutionResult) ([]FollowUpSpec, error) {
			hookCalled = true
			return []FollowUpSpec{
				{WorkflowName: "follow-up-wf", Inputs: map[string]any{"source": r.Outputs["result"]}},
			}, nil
		},
	})
	require.NoError(t, err)
	require.True(t, hookCalled)
	require.Len(t, result.FollowUps, 1)
	require.Equal(t, "follow-up-wf", result.FollowUps[0].WorkflowName)
	require.Equal(t, "done", result.FollowUps[0].Inputs["source"])
}

func TestRunnerCompletionHookNotCalledOnFailure(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return nil, errors.New("boom")
	})

	hookCalled := false
	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		CompletionHook: func(ctx context.Context, r *ExecutionResult) ([]FollowUpSpec, error) {
			hookCalled = true
			return nil, nil
		},
	})
	require.NoError(t, err)
	require.True(t, result.Failed())
	require.False(t, hookCalled, "hook should not fire on failure")
}

func TestRunnerDefaultTimeoutFromConfig(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	runner := NewRunner(RunnerConfig{
		DefaultTimeout: 100 * time.Millisecond,
	})
	result, err := runner.Run(context.Background(), exec, RunOptions{})
	require.NoError(t, err)
	require.True(t, result.Failed())
}

func TestRunnerNilExecutionReturnsError(t *testing.T) {
	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), nil, RunOptions{})
	require.ErrorIs(t, err, ErrNilExecution)
	require.Nil(t, result)
}

func TestRunnerHeartbeatZeroIntervalReturnsError(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "ok", nil
	})

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		Heartbeat: &HeartbeatConfig{
			Interval: 0,
			Func:     func(ctx context.Context) error { return nil },
		},
	})
	require.ErrorIs(t, err, ErrInvalidHeartbeatInterval)
	require.Nil(t, result)
}

func TestRunnerHeartbeatNilFuncReturnsError(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "ok", nil
	})

	runner := NewRunner(RunnerConfig{})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		Heartbeat: &HeartbeatConfig{
			Interval: time.Second,
			Func:     nil,
		},
	})
	require.ErrorIs(t, err, ErrNilHeartbeatFunc)
	require.Nil(t, result)
}

func TestRunnerNegativeTimeoutDisablesDefault(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "fast", nil
	})

	runner := NewRunner(RunnerConfig{
		DefaultTimeout: 1 * time.Second, // short so regressions surface quickly
	})
	result, err := runner.Run(context.Background(), exec, RunOptions{
		Timeout: -1, // explicit no-timeout override
	})
	require.NoError(t, err)
	require.True(t, result.Completed())
}
