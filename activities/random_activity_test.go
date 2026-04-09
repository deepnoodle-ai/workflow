package activities

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRandomActivity(t *testing.T) {
	activity := NewRandomActivity()
	require.Equal(t, "random", activity.Name())

	t.Run("uuid", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"type": "uuid"})
		require.NoError(t, err)
		uuid, ok := result.(string)
		require.True(t, ok)
		require.Len(t, uuid, 36)
	})

	t.Run("default type is uuid", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{})
		require.NoError(t, err)
		uuid, ok := result.(string)
		require.True(t, ok)
		require.Len(t, uuid, 36)
	})

	t.Run("number with seed", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"type": "number", "min": 0.0, "max": 100.0, "seed": int64(42),
		})
		require.NoError(t, err)
		num, ok := result.(int)
		require.True(t, ok)
		require.GreaterOrEqual(t, num, 0)
		require.LessOrEqual(t, num, 100)
	})

	t.Run("number default range", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"type": "number", "seed": int64(42)})
		require.NoError(t, err)
		_, ok := result.(int)
		require.True(t, ok)
	})

	t.Run("float", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"type": "float", "min": 1.0, "max": 5.0, "seed": int64(42),
		})
		require.NoError(t, err)
		f, ok := result.(float64)
		require.True(t, ok)
		require.GreaterOrEqual(t, f, 1.0)
		require.LessOrEqual(t, f, 5.0)
	})

	t.Run("string with length", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"type": "string", "length": 20, "seed": int64(42),
		})
		require.NoError(t, err)
		s, ok := result.(string)
		require.True(t, ok)
		require.Len(t, s, 20)
	})

	t.Run("string default length", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"type": "string", "seed": int64(42)})
		require.NoError(t, err)
		s, ok := result.(string)
		require.True(t, ok)
		require.Len(t, s, 10)
	})

	t.Run("string custom charset", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"type": "string", "length": 5, "charset": "abc", "seed": int64(42),
		})
		require.NoError(t, err)
		s, ok := result.(string)
		require.True(t, ok)
		require.Len(t, s, 5)
		for _, c := range s {
			require.Contains(t, "abc", string(c))
		}
	})

	t.Run("choice", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"type": "choice", "choices": []string{"a", "b", "c"}, "seed": int64(42),
		})
		require.NoError(t, err)
		s, ok := result.(string)
		require.True(t, ok)
		require.Contains(t, []string{"a", "b", "c"}, s)
	})

	t.Run("choice empty", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{"type": "choice", "choices": []string{}})
		require.Error(t, err)
		require.Contains(t, err.Error(), "choices cannot be empty")
	})

	t.Run("boolean", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"type": "boolean", "seed": int64(42)})
		require.NoError(t, err)
		_, ok := result.(bool)
		require.True(t, ok)
	})

	t.Run("alphanumeric", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"type": "alphanumeric", "seed": int64(42)})
		require.NoError(t, err)
		s, ok := result.(string)
		require.True(t, ok)
		require.Len(t, s, 8)
	})

	t.Run("hex", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"type": "hex", "seed": int64(42)})
		require.NoError(t, err)
		s, ok := result.(string)
		require.True(t, ok)
		require.Len(t, s, 8)
		for _, c := range s {
			require.Contains(t, "0123456789abcdef", string(c))
		}
	})

	t.Run("unsupported type", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{"type": "unknown"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported type")
	})

	t.Run("multiple count", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"type": "number", "min": 0.0, "max": 100.0, "count": 3, "seed": int64(42),
		})
		require.NoError(t, err)
		arr, ok := result.([]any)
		require.True(t, ok)
		require.Len(t, arr, 3)
	})
}
