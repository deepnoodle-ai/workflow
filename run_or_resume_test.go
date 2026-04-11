package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestErrNoCheckpointSentinel(t *testing.T) {
	wf, err := New(Options{
		Name:  "sentinel-test",
		Steps: []*Step{{Name: "start", Activity: "noop"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Checkpointer:   NewNullCheckpointer(), // always returns nil, nil
		Activities: []Activity{
			NewActivityFunction("noop", func(ctx Context, params map[string]any) (any, error) {
				return nil, nil
			}),
		},
	})
	require.NoError(t, err)

	err = exec.Resume(context.Background(), "does-not-exist")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoCheckpoint), "should wrap ErrNoCheckpoint")
	require.Contains(t, err.Error(), "does-not-exist")
}

func TestRunOrResumeFallsBackOnMissingCheckpoint(t *testing.T) {
	callCount := 0
	wf, err := New(Options{
		Name:  "fallback-test",
		Steps: []*Step{{Name: "count", Activity: "counter"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Checkpointer:   NewNullCheckpointer(),
		Activities: []Activity{
			NewActivityFunction("counter", func(ctx Context, params map[string]any) (any, error) {
				callCount++
				return nil, nil
			}),
		},
	})
	require.NoError(t, err)

	err = exec.RunOrResume(context.Background(), "no-such-checkpoint")
	require.NoError(t, err)
	require.Equal(t, 1, callCount, "activity should have run once via fresh Run")
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
}

func TestRunOrResumePropagatesRealErrors(t *testing.T) {
	// A checkpointer that returns an infrastructure error (not "not found")
	brokenCheckpointer := &errorCheckpointer{
		err: fmt.Errorf("database connection refused"),
	}

	wf, err := New(Options{
		Name:  "propagate-test",
		Steps: []*Step{{Name: "start", Activity: "noop"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Checkpointer:   brokenCheckpointer,
		Activities: []Activity{
			NewActivityFunction("noop", func(ctx Context, params map[string]any) (any, error) {
				return nil, nil
			}),
		},
	})
	require.NoError(t, err)

	err = exec.RunOrResume(context.Background(), "some-id")
	require.Error(t, err)
	require.Contains(t, err.Error(), "database connection refused")
	// Should NOT have fallen back to Run — the error is infrastructure, not "no checkpoint"
	require.False(t, errors.Is(err, ErrNoCheckpoint))
}

func TestResumeFailureLeaveExecutionReusable(t *testing.T) {
	// This tests the bug fix: Resume loads checkpoint BEFORE start(),
	// so a failed Resume (no checkpoint) doesn't taint the execution.
	wf, err := New(Options{
		Name:  "reuse-test",
		Steps: []*Step{{Name: "work", Activity: "noop"}},
	})
	require.NoError(t, err)

	exec, err := NewExecution(ExecutionOptions{
		ScriptCompiler: newTestCompiler(),
		Workflow:       wf,
		Checkpointer:   NewNullCheckpointer(),
		Activities: []Activity{
			NewActivityFunction("noop", func(ctx Context, params map[string]any) (any, error) {
				return nil, nil
			}),
		},
	})
	require.NoError(t, err)

	// First: Resume fails because there's no checkpoint
	err = exec.Resume(context.Background(), "nonexistent")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoCheckpoint))

	// Second: Run should still work because the execution wasn't tainted
	err = exec.Run(context.Background())
	require.NoError(t, err)
	require.Equal(t, ExecutionStatusCompleted, exec.Status())
}

// errorCheckpointer is a test helper that always returns an error on Load.
type errorCheckpointer struct {
	NullCheckpointer
	err error
}

func (e *errorCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return nil, e.err
}
