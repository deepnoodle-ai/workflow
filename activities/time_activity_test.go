package activities

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestTimeActivity(t *testing.T) {
	activity := NewTimeActivity()
	require.Equal(t, "time", activity.Name())

	t.Run("utc", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"utc": true})
		require.NoError(t, err)
		require.NotNil(t, result)
	})

	t.Run("local", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"utc": false})
		require.NoError(t, err)
		require.NotNil(t, result)
	})
}
