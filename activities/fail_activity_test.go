package activities

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailActivity(t *testing.T) {
	activity := NewFailActivity()
	require.Equal(t, "fail", activity.Name())

	t.Run("custom message", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{
			"message": "something broke",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "fail activity: something broke")
	})

	t.Run("default message", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "intentional failure for testing")
	})
}
