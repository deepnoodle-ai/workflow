package activities

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestWaitActivity(t *testing.T) {
	activity := NewWaitActivity()
	require.Equal(t, "wait", activity.Name())

	t.Run("zero duration returns immediately", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"seconds": 0.0})
		require.NoError(t, err)
		require.Equal(t, "done", result)
	})

	t.Run("negative duration returns immediately", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"seconds": -1.0})
		require.NoError(t, err)
		require.Equal(t, "done", result)
	})

	t.Run("short wait completes", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"seconds": 0.01})
		require.NoError(t, err)
		require.Equal(t, "done", result)
	})

	t.Run("context cancellation", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()
		ctx := workflow.NewContext(cancelCtx, workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{}),
		})
		_, err := activity.Execute(ctx, map[string]any{"seconds": 10.0})
		require.Error(t, err)
	})
}
