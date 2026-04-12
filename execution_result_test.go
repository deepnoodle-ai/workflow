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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("do_work", func(ctx Context, params map[string]any) (any, error) {
		return "hello", nil
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
	)
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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("fail", func(ctx Context, params map[string]any) (any, error) {
		return nil, errors.New("something broke")
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
	)
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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("do_work", func(ctx Context, params map[string]any) (any, error) {
		return "hello", nil
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
	)
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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("block", func(ctx Context, params map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
	)
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

func TestExecutionResultOutputHelpers(t *testing.T) {
	r := &ExecutionResult{
		Outputs: map[string]any{
			"name":    "alice",
			"count":   42,
			"score":   3.14,
			"i64":     int64(99),
			"enabled": true,
		},
	}

	t.Run("Output", func(t *testing.T) {
		v, ok := r.Output("name")
		require.True(t, ok)
		require.Equal(t, "alice", v)

		_, ok = r.Output("missing")
		require.False(t, ok)
	})

	t.Run("OutputString", func(t *testing.T) {
		s, ok := r.OutputString("name")
		require.True(t, ok)
		require.Equal(t, "alice", s)

		_, ok = r.OutputString("count")
		require.False(t, ok, "non-string should return false")

		_, ok = r.OutputString("missing")
		require.False(t, ok)
	})

	t.Run("OutputInt", func(t *testing.T) {
		n, ok := r.OutputInt("count")
		require.True(t, ok)
		require.Equal(t, 42, n)

		n, ok = r.OutputInt("i64")
		require.True(t, ok)
		require.Equal(t, 99, n)

		// float64 (JSON-decoded numbers) is supported and truncated.
		n, ok = r.OutputInt("score")
		require.True(t, ok)
		require.Equal(t, 3, n)

		_, ok = r.OutputInt("name")
		require.False(t, ok)

		_, ok = r.OutputInt("missing")
		require.False(t, ok)
	})

	t.Run("OutputBool", func(t *testing.T) {
		b, ok := r.OutputBool("enabled")
		require.True(t, ok)
		require.True(t, b)

		_, ok = r.OutputBool("count")
		require.False(t, ok)
	})

	t.Run("OutputAs generic", func(t *testing.T) {
		s, ok := OutputAs[string](r, "name")
		require.True(t, ok)
		require.Equal(t, "alice", s)

		// type mismatch returns zero value + false
		_, ok = OutputAs[int](r, "name")
		require.False(t, ok)

		_, ok = OutputAs[string](r, "missing")
		require.False(t, ok)
	})

	t.Run("nil-safe", func(t *testing.T) {
		var nilResult *ExecutionResult
		_, ok := nilResult.Output("any")
		require.False(t, ok)
		_, ok = nilResult.OutputString("any")
		require.False(t, ok)
		_, ok = nilResult.OutputInt("any")
		require.False(t, ok)
		_, ok = nilResult.OutputBool("any")
		require.False(t, ok)
		_, ok = OutputAs[string](nilResult, "any")
		require.False(t, ok)
	})
}

func TestExecutionResultSuspensionHelpers(t *testing.T) {
	t.Run("not suspended", func(t *testing.T) {
		r := &ExecutionResult{Status: ExecutionStatusCompleted}
		require.Equal(t, SuspensionReason(""), r.WaitReason())
		require.Nil(t, r.Topics())
		_, ok := r.NextWakeAt()
		require.False(t, ok)
	})

	t.Run("waiting on signals", func(t *testing.T) {
		wakeAt := time.Now().Add(2 * time.Hour)
		r := &ExecutionResult{
			Status: ExecutionStatusSuspended,
			Suspension: &SuspensionInfo{
				Reason: SuspensionReasonWaitingSignal,
				Topics: []string{"approval", "callback"},
				WakeAt: wakeAt,
			},
		}
		require.Equal(t, SuspensionReasonWaitingSignal, r.WaitReason())
		require.Equal(t, []string{"approval", "callback"}, r.Topics())
		got, ok := r.NextWakeAt()
		require.True(t, ok)
		require.Equal(t, wakeAt, got)
	})

	t.Run("nil-safe", func(t *testing.T) {
		var r *ExecutionResult
		require.Equal(t, SuspensionReason(""), r.WaitReason())
		require.Nil(t, r.Topics())
		_, ok := r.NextWakeAt()
		require.False(t, ok)
	})
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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("do_work", func(ctx Context, params map[string]any) (any, error) {
		return 42, nil
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
		WithCheckpointer(NewNullCheckpointer()),
	)
	require.NoError(t, err)

	result, err := exec.Execute(context.Background(), ResumeFrom("nonexistent-id"))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Completed())
	require.Equal(t, 42, result.Outputs["result"])
}
