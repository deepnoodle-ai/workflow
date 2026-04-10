package expr

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/stretchr/testify/require"
)

func TestEngine_CompileAndEvaluate(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	t.Run("simple arithmetic", func(t *testing.T) {
		s, err := engine.Compile(ctx, "1 + 2")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.EqualValues(t, 3, result.Value())
	})

	t.Run("state lookup", func(t *testing.T) {
		s, err := engine.Compile(ctx, "state.count + 10")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, map[string]any{
			"state": map[string]any{"count": 5},
		})
		require.NoError(t, err)
		require.EqualValues(t, 15, result.Value())
	})

	t.Run("inputs lookup", func(t *testing.T) {
		s, err := engine.Compile(ctx, "inputs.name")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, map[string]any{
			"inputs": map[string]any{"name": "Alice"},
		})
		require.NoError(t, err)
		require.Equal(t, "Alice", result.Value())
	})

	t.Run("boolean condition", func(t *testing.T) {
		s, err := engine.Compile(ctx, "state.count > 3")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, map[string]any{
			"state": map[string]any{"count": 5},
		})
		require.NoError(t, err)
		require.True(t, result.IsTruthy())
	})

	t.Run("logical and", func(t *testing.T) {
		s, err := engine.Compile(ctx, "state.a && state.b")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, map[string]any{
			"state": map[string]any{"a": true, "b": true},
		})
		require.NoError(t, err)
		require.True(t, result.IsTruthy())
	})

	t.Run("list literal iteration", func(t *testing.T) {
		s, err := engine.Compile(ctx, "[1, 2, 3]")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		items, err := result.Items()
		require.NoError(t, err)
		require.Len(t, items, 3)
	})

	t.Run("undefined variable at compile time is allowed", func(t *testing.T) {
		_, err := engine.Compile(ctx, "state.not_set")
		require.NoError(t, err, "undefined keys should be deferred to evaluation")
	})

	t.Run("invalid syntax fails to compile", func(t *testing.T) {
		_, err := engine.Compile(ctx, "1 + + +")
		require.Error(t, err)
	})

	t.Run("reuse compiled script", func(t *testing.T) {
		s, err := engine.Compile(ctx, "state.x * 2")
		require.NoError(t, err)

		r1, err := s.Evaluate(ctx, map[string]any{"state": map[string]any{"x": 5}})
		require.NoError(t, err)
		require.EqualValues(t, 10, r1.Value())

		r2, err := s.Evaluate(ctx, map[string]any{"state": map[string]any{"x": 20}})
		require.NoError(t, err)
		require.EqualValues(t, 40, r2.Value())
	})
}

func TestEngine_CustomFunctions(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(WithFunctions(map[string]any{
		"double": func(n int) int { return n * 2 },
	}))

	s, err := engine.Compile(ctx, "double(state.x)")
	require.NoError(t, err)
	result, err := s.Evaluate(ctx, map[string]any{
		"state": map[string]any{"x": 7},
	})
	require.NoError(t, err)
	require.EqualValues(t, 14, result.Value())
}

func TestEngine_Template(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()

	tests := []struct {
		name    string
		input   string
		globals map[string]any
		want    string
	}{
		{
			name:  "single variable",
			input: "Hello ${state.name}",
			globals: map[string]any{
				"state": map[string]any{"name": "Alice"},
			},
			want: "Hello Alice",
		},
		{
			name:  "multiple variables and arithmetic",
			input: "${state.greeting} ${state.name}! The answer is ${40 + 2}",
			globals: map[string]any{
				"state": map[string]any{
					"greeting": "Hello",
					"name":     "Bob",
				},
			},
			want: "Hello Bob! The answer is 42",
		},
		{
			name:  "nested expression",
			input: "Result: ${1 + (2 * 3)}",
			want:  "Result: 7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := script.NewTemplate(engine, tt.input)
			require.NoError(t, err)
			got, err := tmpl.Eval(ctx, tt.globals)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestExprValue_Items(t *testing.T) {
	t.Run("any slice", func(t *testing.T) {
		v := &exprValue{v: []any{1, 2, 3}}
		items, err := v.Items()
		require.NoError(t, err)
		require.Len(t, items, 3)
	})
	t.Run("scalar wraps to single item", func(t *testing.T) {
		v := &exprValue{v: 42}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{42}, items)
	})
}

func TestExprValue_IsTruthy(t *testing.T) {
	tests := []struct {
		name   string
		v      any
		expect bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"nonzero", 1, true},
		{"zero", 0, false},
		{"nonempty string", "hi", true},
		{"empty string", "", false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &exprValue{v: tt.v}
			require.Equal(t, tt.expect, v.IsTruthy())
		})
	}
}
