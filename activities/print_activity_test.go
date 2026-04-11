package activities

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestPrintActivity(t *testing.T) {
	activity := NewPrintActivity()
	require.Equal(t, "print", activity.Name())

	t.Run("simple message", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"message": "hello world",
		})
		require.NoError(t, err)
		require.Equal(t, "hello world", result)
	})

	t.Run("formatted message", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"message": "hello %s, count is %v",
			"args":    []any{"curtis", 30},
		})
		require.NoError(t, err)
		require.Equal(t, "hello curtis, count is 30", result)
	})
}
