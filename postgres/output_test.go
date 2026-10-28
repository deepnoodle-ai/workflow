package postgres

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestOutputsPostgresCannotStore returns outputs that encode to no valid
// jsonb. The step fails once with invalid_output; the handler is not run
// again because the result could not be stored.
func TestOutputsPostgresCannotStore(t *testing.T) {
	for name, output := range map[string]any{
		"nul": map[string]any{"text": "before\x00after"},
		"nan": map[string]any{"score": math.NaN()},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			def := load(t, `{"steps": [{"name": "a", "action": "x"}]}`)
			var calls atomic.Int32
			startWorker(t, &Worker{
				Store: s,
				Lease: 200 * time.Millisecond,
				Handlers: map[string]workflow.Handler{
					"action": func(ctx context.Context, job workflow.Job) (any, error) {
						calls.Add(1)
						return output, nil
					},
				},
			})
			ctx := context.Background()
			_, err := s.Start(ctx, NewRun{ID: name, Definition: def})
			require.NoError(t, err)
			r := waitStatus(t, s, name, workflow.StatusFailed, workflow.StatusCompleted)
			require.Equal(t, workflow.StatusFailed, r.Status)
			require.Equal(t, workflow.ErrorTypeInvalidOutput, r.State.Error.Type)
			time.Sleep(500 * time.Millisecond) // past the lease
			require.Equal(t, int32(1), calls.Load())
			steps, err := s.Steps(ctx, name)
			require.NoError(t, err)
			require.Equal(t, StepFailed, steps[0].Status)
			require.Equal(t, workflow.ErrorTypeInvalidOutput, steps[0].Error.Type)
		})
	}
}

// TestDeliverCleansWhatPostgresCannotStore delivers NUL characters through
// the application's own path.
func TestDeliverCleansWhatPostgresCannotStore(t *testing.T) {
	s := newStore(t)
	def := load(t, `{"steps": [{"name": "a", "agent": "x", "next": ["b"]}, {"name": "b", "agent": "x"}]}`)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "clean", Definition: def})
	require.NoError(t, err)
	res, err := s.Deliver(ctx, "clean", workflow.Event{
		Kind:   workflow.EventStepCompleted,
		StepID: "main/a#1",
		Output: "nul\x00",
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.Equal(t, workflow.StatusFailed, res.State.Status)
	require.Equal(t, workflow.ErrorTypeInvalidOutput, res.State.Error.Type)
	require.True(t, strings.Contains(res.State.Error.Message, "NUL"), res.State.Error.Message)

	_, err = s.Start(ctx, NewRun{ID: "clean-2", Definition: def})
	require.NoError(t, err)
	res, err = s.Deliver(ctx, "clean-2", workflow.Event{
		Kind:   workflow.EventStepFailed,
		StepID: "main/a#1",
		Error:  &workflow.StepError{Type: "boom", Message: "bad\x00byte"},
	})
	require.NoError(t, err)
	require.True(t, res.Applied)
	r, err := s.GetRun(ctx, "clean-2")
	require.NoError(t, err)
	require.Equal(t, "bad�byte", r.State.Error.Message)

	_, err = s.Start(ctx, NewRun{
		ID:         "clean-3",
		Definition: def,
		Input:      map[string]any{"x": "\x00"},
	})
	require.ErrorIs(t, err, ErrUnstorable)
	_, err = s.GetRun(ctx, "clean-3")
	require.ErrorIs(t, err, ErrRunNotFound)
}

func TestHasNUL(t *testing.T) {
	for in, want := range map[string]bool{
		`"a\u0000b"`:     true,
		`"a\\u0000b"`:    false, // an escaped backslash followed by text
		`"a\\\u0000b"`:   true,
		`{"\u0000": 1}`:  true,
		`"\u00001"`:      true,
		`"plain"`:        false,
		`"a\"\u0000"`:    true,
		`"\u0001\u000a"`: false,
	} {
		require.Equal(t, want, hasNUL([]byte(in)), in)
	}
}

// TestUnstorableErrorsAreRecognized checks the worker's fallback: an error
// Postgres raises for a value it cannot store fails the step instead of
// leaving the job to be claimed again.
func TestUnstorableErrorsAreRecognized(t *testing.T) {
	s := newStore(t)
	_, err := s.pool.Exec(context.Background(), `SELECT $1::jsonb`, `"\u0000"`)
	require.Error(t, err)
	require.True(t, isUnstorable(err), err)
	_, err = s.pool.Exec(context.Background(), `SELECT 1/0`)
	require.True(t, isUnstorable(err)) // division by zero is class 22 too; never transient
	require.False(t, isUnstorable(errLeaseLost))
}
