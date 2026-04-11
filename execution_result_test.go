package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestExecuteSuccessReturnsStructuredResult(t *testing.T) {
	wf, err := New(Options{
		Name: "result-test",
		Steps: []*Step{
			{Name: "work", Activity: "do_work", Store: "output"},
		},
		Outputs: []*Output{{Name: "output", Variable: "output"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("do_work", func(ctx Context, params map[string]any) (any, error) {
				return "hello", nil
			}),
		},
	})
	require.NoError(t, err)

	result, err := exec.Execute(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)

	require.True(t, result.Completed())
	require.False(t, result.Failed())
	require.Nil(t, result.Error)
	require.Equal(t, "result-test", result.WorkflowName)
	require.Equal(t, ExecutionStatusCompleted, result.Status)
	require.Equal(t, "hello", result.Outputs["output"])
	require.False(t, result.Timing.StartedAt.IsZero())
	require.False(t, result.Timing.FinishedAt.IsZero())
	require.True(t, result.Timing.Duration > 0)
}

func TestExecuteFailureReturnsResultNotError(t *testing.T) {
	wf, err := New(Options{
		Name:  "fail-test",
		Steps: []*Step{{Name: "boom", Activity: "fail"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("fail", func(ctx Context, params map[string]any) (any, error) {
				return nil, errors.New("something broke")
			}),
		},
	})
	require.NoError(t, err)

	result, err := exec.Execute(context.Background())
	// Key semantic: err is nil because execution ran. Failure is in result.
	require.NoError(t, err)
	require.NotNil(t, result)

	require.True(t, result.Failed())
	require.False(t, result.Completed())
	require.NotNil(t, result.Error)
	require.Equal(t, ExecutionStatusFailed, result.Status)
	require.Contains(t, result.Error.Cause, "something broke")
}

func TestExecuteCalledTwiceReturnsError(t *testing.T) {
	wf, err := New(Options{
		Name:  "reuse-test",
		Steps: []*Step{{Name: "work", Activity: "do_work", Store: "result"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("do_work", func(ctx Context, params map[string]any) (any, error) {
				return "hello", nil
			}),
		},
	})
	require.NoError(t, err)

	// First call succeeds
	result, err := exec.Execute(context.Background())
	require.NoError(t, err)
	require.True(t, result.Completed())

	// Second call returns infrastructure error, not a stale result
	result2, err := exec.Execute(context.Background())
	require.ErrorIs(t, err, ErrAlreadyStarted)
	require.Nil(t, result2)
}

func TestExecuteInterruptedHasValidDuration(t *testing.T) {
	wf, err := New(Options{
		Name:  "interrupt-test",
		Steps: []*Step{{Name: "block", Activity: "block"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Activities: []Activity{
			NewActivityFunction("block", func(ctx Context, params map[string]any) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}),
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := exec.Execute(ctx)
	require.NoError(t, err)
	require.True(t, result.Failed())
	require.False(t, result.Timing.FinishedAt.IsZero(), "FinishedAt should be set for interrupted executions")
	require.True(t, result.Timing.Duration > 0, "Duration should be positive for interrupted executions")
}

func TestExecuteOrResumeNoCheckpointRunsFresh(t *testing.T) {
	wf, err := New(Options{
		Name: "eor-test",
		Steps: []*Step{
			{Name: "work", Activity: "do_work", Store: "result"},
		},
		Outputs: []*Output{{Name: "result", Variable: "result"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Checkpointer:   NewNullCheckpointer(),
		Activities: []Activity{
			NewActivityFunction("do_work", func(ctx Context, params map[string]any) (any, error) {
				return 42, nil
			}),
		},
	})
	require.NoError(t, err)

	result, err := exec.ExecuteOrResume(context.Background(), "nonexistent-id")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Completed())
	require.Equal(t, 42, result.Outputs["result"])
}
