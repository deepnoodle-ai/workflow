package activities

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestJSONActivity(t *testing.T) {
	activity := NewJSONActivity()
	require.Equal(t, "json", activity.Name())

	tests := []struct {
		name   string
		params map[string]any
		check  func(t *testing.T, result any, err error)
	}{
		{
			name:   "parse",
			params: map[string]any{"data": `{"name": "test", "value": 42}`},
			check: func(t *testing.T, result any, err error) {
				require.NoError(t, err)
				m := result.(map[string]any)
				require.Equal(t, "test", m["name"])
				require.Equal(t, float64(42), m["value"])
			},
		},
		{
			name:   "parse invalid json",
			params: map[string]any{"data": `not json`},
			check: func(t *testing.T, result any, err error) {
				require.Error(t, err)
			},
		},
		{
			name:   "stringify",
			params: map[string]any{"operation": "stringify", "data": `{"b":2,"a":1}`},
			check: func(t *testing.T, result any, err error) {
				require.NoError(t, err)
				s := result.(string)
				require.Contains(t, s, "\"a\": 1")
				require.Contains(t, s, "\"b\": 2")
			},
		},
		{
			name:   "validate valid",
			params: map[string]any{"operation": "validate", "data": `{"valid": true}`},
			check: func(t *testing.T, result any, err error) {
				require.NoError(t, err)
				require.Equal(t, true, result)
			},
		},
		{
			name:   "validate invalid",
			params: map[string]any{"operation": "validate", "data": `not json`},
			check: func(t *testing.T, result any, err error) {
				require.NoError(t, err)
				require.Equal(t, false, result)
			},
		},
		{
			name:   "unsupported operation",
			params: map[string]any{"operation": "unknown", "data": `{}`},
			check: func(t *testing.T, result any, err error) {
				require.Error(t, err)
				require.Contains(t, err.Error(), "unsupported operation")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			result, err := activity.Execute(ctx, tt.params)
			tt.check(t, result, err)
		})
	}
}

func TestJSONActivity_Query(t *testing.T) {
	activity := NewJSONActivity()

	tests := []struct {
		name    string
		data    string
		query   string
		want    any
		wantErr string
	}{
		{"dot notation", `{"user": {"name": "alice"}}`, "user.name", "alice", ""},
		{"array index", `{"items": ["a", "b", "c"]}`, "items.1", "b", ""},
		{"root", `{"a": 1}`, ".", map[string]any{"a": float64(1)}, ""},
		{"empty query", `{}`, "", "", "query cannot be empty"},
		{"key not found", `{"a": 1}`, "b", nil, "key 'b' not found"},
		{"array out of bounds", `[1, 2]`, "5", nil, "out of bounds"},
		{"query into non-object", `{"a": 1}`, "a.b", nil, "cannot query into non-object"},
		{"invalid array index", `["a", "b"]`, "not_a_number", nil, "invalid array index"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newTestContext()
			result, err := activity.Execute(ctx, map[string]any{
				"operation": "query",
				"data":      tt.data,
				"query":     tt.query,
			})
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				if m, ok := tt.want.(map[string]any); ok {
					rm := result.(map[string]any)
					for k, v := range m {
						require.Equal(t, v, rm[k])
					}
				} else {
					require.Equal(t, tt.want, result)
				}
			}
		})
	}
}

func TestJSONActivity_Merge(t *testing.T) {
	activity := NewJSONActivity()

	t.Run("simple merge", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"operation": "merge", "data": `{"a": 1, "b": 2}`, "merge_with": `{"b": 3, "c": 4}`,
		})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, float64(1), m["a"])
		require.Equal(t, float64(3), m["b"])
		require.Equal(t, float64(4), m["c"])
	})

	t.Run("recursive merge", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"operation":  "merge",
			"data":       `{"nested": {"a": 1, "b": 2}}`,
			"merge_with": `{"nested": {"b": 3, "c": 4}}`,
		})
		require.NoError(t, err)
		nested := result.(map[string]any)["nested"].(map[string]any)
		require.Equal(t, float64(1), nested["a"])
		require.Equal(t, float64(3), nested["b"])
		require.Equal(t, float64(4), nested["c"])
	})

	t.Run("empty merge_with", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{"operation": "merge", "data": `{"a": 1}`})
		require.Error(t, err)
		require.Contains(t, err.Error(), "merge_with cannot be empty")
	})

	t.Run("invalid main data", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{
			"operation": "merge", "data": "not json", "merge_with": `{"a": 1}`,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse main data")
	})

	t.Run("invalid merge_with data", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{
			"operation": "merge", "data": `{"a": 1}`, "merge_with": "not json",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to parse merge data")
	})
}
