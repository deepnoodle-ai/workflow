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
	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("do_work", activityFn))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
	)
	require.NoError(t, err)
	return exec
}

func TestRunnerBasicRun(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "hello", nil
	})

	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec)
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

	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec,
		WithRunTimeout(100*time.Millisecond),
	)
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

	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec,
		WithHeartbeat(&HeartbeatConfig{
			Interval: 50 * time.Millisecond,
			Func: func(ctx context.Context) error {
				return fmt.Errorf("lease lost")
			},
		}),
	)
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
	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec,
		WithCompletionHook(func(ctx context.Context, r *ExecutionResult) ([]FollowUpSpec, error) {
			hookCalled = true
			return []FollowUpSpec{
				{WorkflowName: "follow-up-wf", Inputs: map[string]any{"source": r.Outputs["result"]}},
			}, nil
		}),
	)
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
	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec,
		WithCompletionHook(func(ctx context.Context, r *ExecutionResult) ([]FollowUpSpec, error) {
			hookCalled = true
			return nil, nil
		}),
	)
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

	runner := NewRunner(
		WithDefaultTimeout(100 * time.Millisecond),
	)
	result, err := runner.Run(context.Background(), exec)
	require.NoError(t, err)
	require.True(t, result.Failed())
}

func TestRunnerNilExecutionReturnsError(t *testing.T) {
	runner := NewRunner()
	result, err := runner.Run(context.Background(), nil)
	require.ErrorIs(t, err, ErrNilExecution)
	require.Nil(t, result)
}

func TestRunnerHeartbeatZeroIntervalReturnsError(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "ok", nil
	})

	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec,
		WithHeartbeat(&HeartbeatConfig{
			Interval: 0,
			Func:     func(ctx context.Context) error { return nil },
		}),
	)
	require.ErrorIs(t, err, ErrInvalidHeartbeatInterval)
	require.Nil(t, result)
}

func TestRunnerHeartbeatNilFuncReturnsError(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "ok", nil
	})

	runner := NewRunner()
	result, err := runner.Run(context.Background(), exec,
		WithHeartbeat(&HeartbeatConfig{
			Interval: time.Second,
			Func:     nil,
		}),
	)
	require.ErrorIs(t, err, ErrNilHeartbeatFunc)
	require.Nil(t, result)
}

func TestRunnerNegativeTimeoutDisablesDefault(t *testing.T) {
	wf := newSimpleWorkflow(t)
	exec := newSimpleExecution(t, wf, func(ctx Context, params map[string]any) (any, error) {
		return "fast", nil
	})

	runner := NewRunner(
		WithDefaultTimeout(1 * time.Second),
	)
	result, err := runner.Run(context.Background(), exec,
		WithRunTimeout(-1),
	)
	require.NoError(t, err)
	require.True(t, result.Completed())
}
