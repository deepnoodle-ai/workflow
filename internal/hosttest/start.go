package hosttest

import (
	"context"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestLimitsDoNotDependOnTheCache gives the same output to a run whose
// definition is cached (the process that started it) and to one whose
// definition is loaded from the database (another process). Both apply the
// store's limits.
func testLimitsDoNotDependOnTheCache(t *testing.T, h Harness) {
	base := open(t, h)
	limits := WithLimits(workflow.Limits{MaxOutputBytes: 10})
	warm, err := newOn(t, base, kinds, limits)
	require.NoError(t, err)
	def := load(t, `{"steps": [{"name": "a", "agent": "x"}]}`)
	ctx := context.Background()
	big := strings.Repeat("x", 100)
	for _, id := range []string{"warm", "cold"} {
		_, err := warm.Start(ctx, NewRun{ID: id, Definition: def})
		require.NoError(t, err)
	}
	require.Equal(t, workflow.Limits{}, def.Limits(), "the caller's definition is not modified")

	cold, err := newOn(t, base, kinds, limits)
	require.NoError(t, err)
	for id, s := range map[string]*Env{"warm": warm, "cold": cold} {
		res, err := s.Deliver(ctx, id, workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/a#1",
			Output: big,
		})
		require.NoError(t, err)
		require.Equal(t, workflow.StatusFailed, res.State.Status, id)
		require.Equal(t, workflow.ErrorTypeOutputTooLarge, res.State.Error.Type, id)
	}
}

func testStartRejectsUnloadedDefinitions(t *testing.T, h Harness) {
	s := open(t, h)
	ctx := context.Background()
	_, err := s.Start(ctx, NewRun{ID: "nil"})
	require.ErrorIs(t, err, workflow.ErrNotValidated)
	_, err = s.Start(ctx, NewRun{ID: "raw", Definition: &workflow.Definition{Name: "raw"}})
	require.ErrorIs(t, err, workflow.ErrNotValidated)
	require.Equal(t, 0, s.count(t, "definitions", ""))
	require.Equal(t, 0, s.Store.Cached())
}
