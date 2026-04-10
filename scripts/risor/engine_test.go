package risor

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/deepnoodle-ai/workflow/script"
	"github.com/stretchr/testify/require"
)

func TestEngine_CompileAndEvaluate(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(DefaultGlobals())

	t.Run("simple expression", func(t *testing.T) {
		s, err := engine.Compile(ctx, "1 + 2")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, int64(3), result.Value())
	})

	t.Run("string expression", func(t *testing.T) {
		s, err := engine.Compile(ctx, `"hello" + " " + "world"`)
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, "hello world", result.Value())
	})

	t.Run("compile and evaluate boolean", func(t *testing.T) {
		s, err := engine.Compile(ctx, "10 > 5")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, true, result.Value())
		require.True(t, result.IsTruthy())
	})

	t.Run("evaluate with globals", func(t *testing.T) {
		custom := DefaultGlobals()
		custom["x"] = int64(0)
		custom["y"] = int64(0)
		e := NewEngine(custom)

		s, err := e.Compile(ctx, "x + y")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, map[string]any{
			"x": int64(10),
			"y": int64(20),
		})
		require.NoError(t, err)
		require.Equal(t, int64(30), result.Value())
	})

	t.Run("evaluate with builtins", func(t *testing.T) {
		s, err := engine.Compile(ctx, "len([1, 2, 3, 4, 5])")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, int64(5), result.Value())
	})

	t.Run("evaluate with math module", func(t *testing.T) {
		s, err := engine.Compile(ctx, "math.sqrt(16)")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, 4.0, result.Value())
	})

	t.Run("compile error", func(t *testing.T) {
		_, err := engine.Compile(ctx, "{{invalid")
		require.Error(t, err)
	})

	t.Run("reuse compiled script", func(t *testing.T) {
		globals := DefaultGlobals()
		globals["state"] = map[string]any{"x": int64(0)}
		e := NewEngine(globals)

		s, err := e.Compile(ctx, "state.x * 2")
		require.NoError(t, err)

		r1, err := s.Evaluate(ctx, map[string]any{"state": map[string]any{"x": 5}})
		require.NoError(t, err)
		require.Equal(t, int64(10), r1.Value())

		r2, err := s.Evaluate(ctx, map[string]any{"state": map[string]any{"x": 20}})
		require.NoError(t, err)
		require.Equal(t, int64(40), r2.Value())
	})
}

func TestEngine_Template(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(DefaultGlobals())

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
			name:  "nested expressions",
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

func TestScriptValue_Value(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		v := &scriptValue{obj: object.NewString("hello")}
		require.Equal(t, "hello", v.Value())
	})
	t.Run("int", func(t *testing.T) {
		v := &scriptValue{obj: object.NewInt(42)}
		require.Equal(t, int64(42), v.Value())
	})
	t.Run("float", func(t *testing.T) {
		v := &scriptValue{obj: object.NewFloat(3.14)}
		require.Equal(t, 3.14, v.Value())
	})
	t.Run("bool", func(t *testing.T) {
		v := &scriptValue{obj: object.True}
		require.Equal(t, true, v.Value())
	})
	t.Run("time", func(t *testing.T) {
		fixedTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
		v := &scriptValue{obj: object.NewTime(fixedTime)}
		require.Equal(t, fixedTime, v.Value())
	})
	t.Run("list", func(t *testing.T) {
		v := &scriptValue{obj: object.NewList([]object.Object{object.NewString("a"), object.NewInt(1)})}
		result := v.Value()
		arr, ok := result.([]any)
		require.True(t, ok)
		require.Equal(t, "a", arr[0])
		require.Equal(t, int64(1), arr[1])
	})
	t.Run("map", func(t *testing.T) {
		v := &scriptValue{obj: object.NewMap(map[string]object.Object{"k": object.NewString("v")})}
		result := v.Value()
		m, ok := result.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "v", m["k"])
	})
	t.Run("nil", func(t *testing.T) {
		v := &scriptValue{obj: object.Nil}
		require.Nil(t, v.Value())
	})
}

func TestScriptValue_IsTruthy(t *testing.T) {
	tests := []struct {
		name     string
		obj      object.Object
		expected bool
	}{
		{"true", object.True, true},
		{"false", object.False, false},
		{"int nonzero", object.NewInt(5), true},
		{"int zero", object.NewInt(0), false},
		{"float nonzero", object.NewFloat(1.0), true},
		{"float zero", object.NewFloat(0.0), false},
		{"list nonempty", object.NewList([]object.Object{object.NewInt(1)}), true},
		{"list empty", object.NewList([]object.Object{}), false},
		{"map nonempty", object.NewMap(map[string]object.Object{"a": object.NewInt(1)}), true},
		{"map empty", object.NewMap(map[string]object.Object{}), false},
		{"string nonempty", object.NewString("hello"), true},
		{"string empty", object.NewString(""), false},
		{"string false", object.NewString("false"), false},
		{"nil", object.Nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &scriptValue{obj: tt.obj}
			require.Equal(t, tt.expected, v.IsTruthy())
		})
	}
}

func TestScriptValue_Items(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		v := &scriptValue{obj: object.NewString("hello")}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{"hello"}, items)
	})
	t.Run("list", func(t *testing.T) {
		v := &scriptValue{obj: object.NewList([]object.Object{
			object.NewString("a"),
			object.NewString("b"),
		})}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b"}, items)
	})
	t.Run("unsupported", func(t *testing.T) {
		v := &scriptValue{obj: object.Nil}
		_, err := v.Items()
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported risor result type for 'each'")
	})
}

func TestScriptValue_String(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		v := &scriptValue{obj: object.NewString("hello")}
		require.Equal(t, "hello", v.String())
	})
	t.Run("int", func(t *testing.T) {
		v := &scriptValue{obj: object.NewInt(42)}
		require.Equal(t, "42", v.String())
	})
	t.Run("float", func(t *testing.T) {
		v := &scriptValue{obj: object.NewFloat(3.14)}
		require.Equal(t, "3.14", v.String())
	})
	t.Run("bool", func(t *testing.T) {
		v := &scriptValue{obj: object.True}
		require.Equal(t, "true", v.String())
	})
	t.Run("time", func(t *testing.T) {
		ts := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		v := &scriptValue{obj: object.NewTime(ts)}
		require.Equal(t, "2025-01-15T12:00:00Z", v.String())
	})
	t.Run("nil", func(t *testing.T) {
		v := &scriptValue{obj: object.Nil}
		require.Equal(t, "", v.String())
	})
}

func TestDefaultGlobals(t *testing.T) {
	globals := DefaultGlobals()
	require.NotNil(t, globals)
	require.NotNil(t, globals["inputs"])
	require.NotNil(t, globals["state"])
	require.NotNil(t, globals["len"])
}
