package script

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// stubCompiler is a minimal Compiler used by tests in the script package
// to exercise engine-neutral logic (Template parsing, etc.) without
// pulling in a real scripting engine. It only supports single identifier
// lookups against the globals map.
type stubCompiler struct{}

func (stubCompiler) Compile(ctx context.Context, code string) (Script, error) {
	expr := strings.TrimSpace(code)
	if expr == "" {
		return nil, fmt.Errorf("empty expression")
	}
	return &stubScript{expr: expr}, nil
}

type stubScript struct {
	expr string
}

func (s *stubScript) Evaluate(ctx context.Context, globals map[string]any) (Value, error) {
	// Resolve dot-separated identifier paths like "state.name".
	parts := strings.Split(s.expr, ".")
	var current any = globals
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("not a map at %q", part)
		}
		v, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("undefined variable %q", part)
		}
		current = v
	}
	return &stubValue{v: current}, nil
}

type stubValue struct {
	v any
}

func (s *stubValue) Value() any            { return s.v }
func (s *stubValue) Items() ([]any, error) { return EachValue(s.v) }
func (s *stubValue) String() string        { return fmt.Sprintf("%v", s.v) }
func (s *stubValue) IsTruthy() bool        { return IsTruthyValue(s.v) }

func TestTemplate(t *testing.T) {
	engine := stubCompiler{}

	t.Run("plain string without template variables", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "Hello World")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, "Hello World", got)
	})

	t.Run("interpolated template returns string", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "Hello ${state.name}")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{"name": "Alice"},
		})
		require.NoError(t, err)
		require.Equal(t, "Hello Alice", got)
	})

	t.Run("multiple template variables", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "${state.greeting} ${state.name}")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{
				"greeting": "Hello",
				"name":     "Bob",
			},
		})
		require.NoError(t, err)
		require.Equal(t, "Hello Bob", got)
	})

	t.Run("single-expression template preserves int", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "${state.count}")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{"count": 42},
		})
		require.NoError(t, err)
		require.Equal(t, 42, got)
	})

	t.Run("single-expression template preserves bool", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "${state.flag}")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{"flag": true},
		})
		require.NoError(t, err)
		require.Equal(t, true, got)
	})

	t.Run("single-expression template with surrounding whitespace preserves type", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "  ${state.count}  ")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{"count": 7},
		})
		require.NoError(t, err)
		require.Equal(t, 7, got)
	})

	t.Run("EvalString stringifies typed value", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "${state.count}")
		require.NoError(t, err)
		got, err := tmpl.EvalString(context.Background(), map[string]any{
			"state": map[string]any{"count": 42},
		})
		require.NoError(t, err)
		require.Equal(t, "42", got)
	})

	t.Run("unclosed brace is rejected", func(t *testing.T) {
		_, err := NewTemplate(engine, "Hello ${name")
		require.Error(t, err)
		require.Contains(t, err.Error(), "unclosed template expression")
	})

}

func TestIsTruthyValue(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		expect bool
	}{
		{"nil", nil, false},
		{"true bool", true, true},
		{"false bool", false, false},
		{"nonzero int", 42, true},
		{"zero int", 0, false},
		{"nonzero int64", int64(1), true},
		{"zero int64", int64(0), false},
		{"nonzero float64", 3.14, true},
		{"zero float64", 0.0, false},
		{"nonempty string", "hello", true},
		{"empty string", "", false},
		{"false string lowercase", "false", false},
		{"false string mixed case", "FaLsE", false},
		{"nonempty []any", []any{1}, true},
		{"empty []any", []any{}, false},
		{"nonempty map", map[string]any{"a": 1}, true},
		{"empty map", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, IsTruthyValue(tt.value))
		})
	}
}

func TestEachValue(t *testing.T) {
	t.Run("string slice", func(t *testing.T) {
		result, err := EachValue([]string{"a", "b", "c"})
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b", "c"}, result)
	})

	t.Run("int slice", func(t *testing.T) {
		result, err := EachValue([]int{1, 2, 3})
		require.NoError(t, err)
		require.Equal(t, []any{1, 2, 3}, result)
	})

	t.Run("any slice", func(t *testing.T) {
		input := []any{"hello", 42, true}
		result, err := EachValue(input)
		require.NoError(t, err)
		require.Equal(t, input, result)
	})

	t.Run("map converts to key-value pairs", func(t *testing.T) {
		result, err := EachValue(map[string]any{"key": "value"})
		require.NoError(t, err)
		require.Len(t, result, 1)
		item := result[0].(map[string]any)
		require.Equal(t, "key", item["key"])
		require.Equal(t, "value", item["value"])
	})

	t.Run("scalar wraps in slice", func(t *testing.T) {
		result, err := EachValue(42)
		require.NoError(t, err)
		require.Equal(t, []any{42}, result)
	})

	t.Run("unsupported type errors", func(t *testing.T) {
		_, err := EachValue(struct{ X int }{X: 1})
		require.Error(t, err)
	})
}
