package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestRunOrResumeFallsBackOnMissingCheckpoint(t *testing.T) {
	callCount := 0
	wf, err := New(Options{
		Name:  "fallback-test",
		Steps: []*Step{{Name: "count", Activity: "counter"}},
	})
	require.NoError(t, err)

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("counter", func(ctx Context, params map[string]any) (any, error) {
		callCount++
		return nil, nil
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
		WithCheckpointer(NewNullCheckpointer()),
	)
	require.NoError(t, err)

	_, err = exec.Execute(context.Background(), ResumeFrom("no-such-checkpoint"))
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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("noop", func(ctx Context, params map[string]any) (any, error) {
		return nil, nil
	}))
	exec, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
		WithCheckpointer(brokenCheckpointer),
	)
	require.NoError(t, err)

	_, err = exec.Execute(context.Background(), ResumeFrom("some-id"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "database connection refused")
	// Should NOT have fallen back to Run — the error is infrastructure, not "no checkpoint"
	require.False(t, errors.Is(err, ErrNoCheckpoint))
}

// errorCheckpointer is a test helper that always returns an error on Load.
type errorCheckpointer struct {
	NullCheckpointer
	err error
}

func (e *errorCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return nil, e.err
}
