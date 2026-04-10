package script

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/stretchr/testify/require"
)

func TestRisorValue_Value(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		v := &RisorValue{obj: object.NewString("hello")}
		require.Equal(t, "hello", v.Value())
	})
	t.Run("int", func(t *testing.T) {
		v := &RisorValue{obj: object.NewInt(42)}
		require.Equal(t, int64(42), v.Value())
	})
	t.Run("float", func(t *testing.T) {
		v := &RisorValue{obj: object.NewFloat(3.14)}
		require.Equal(t, 3.14, v.Value())
	})
	t.Run("bool", func(t *testing.T) {
		v := &RisorValue{obj: object.True}
		require.Equal(t, true, v.Value())
	})
	t.Run("time", func(t *testing.T) {
		fixedTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
		v := &RisorValue{obj: object.NewTime(fixedTime)}
		require.Equal(t, fixedTime, v.Value())
	})
	t.Run("list", func(t *testing.T) {
		v := &RisorValue{obj: object.NewList([]object.Object{object.NewString("a"), object.NewInt(1)})}
		result := v.Value()
		arr, ok := result.([]interface{})
		require.True(t, ok)
		require.Equal(t, "a", arr[0])
		require.Equal(t, int64(1), arr[1])
	})
	t.Run("map", func(t *testing.T) {
		v := &RisorValue{obj: object.NewMap(map[string]object.Object{"k": object.NewString("v")})}
		result := v.Value()
		m, ok := result.(map[string]interface{})
		require.True(t, ok)
		require.Equal(t, "v", m["k"])
	})
	t.Run("nil", func(t *testing.T) {
		v := &RisorValue{obj: object.Nil}
		require.Nil(t, v.Value())
	})
}

func TestRisorValue_IsTruthy(t *testing.T) {
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
			v := &RisorValue{obj: tt.obj}
			require.Equal(t, tt.expected, v.IsTruthy())
		})
	}
}

func TestRisorValue_Items(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		v := &RisorValue{obj: object.NewString("hello")}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{"hello"}, items)
	})
	t.Run("int", func(t *testing.T) {
		v := &RisorValue{obj: object.NewInt(42)}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{int64(42)}, items)
	})
	t.Run("float", func(t *testing.T) {
		v := &RisorValue{obj: object.NewFloat(3.14)}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{3.14}, items)
	})
	t.Run("bool", func(t *testing.T) {
		v := &RisorValue{obj: object.True}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{true}, items)
	})
	t.Run("time", func(t *testing.T) {
		fixedTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
		v := &RisorValue{obj: object.NewTime(fixedTime)}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{fixedTime}, items)
	})
	t.Run("list", func(t *testing.T) {
		v := &RisorValue{obj: object.NewList([]object.Object{
			object.NewString("a"),
			object.NewString("b"),
		})}
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b"}, items)
	})
	t.Run("map", func(t *testing.T) {
		v := &RisorValue{obj: object.NewMap(map[string]object.Object{
			"key": object.NewString("value"),
		})}
		items, err := v.Items()
		require.NoError(t, err)
		require.Len(t, items, 1)
		require.Equal(t, "value", items[0])
	})
	t.Run("unsupported", func(t *testing.T) {
		v := &RisorValue{obj: object.Nil}
		_, err := v.Items()
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported risor result type for 'each'")
	})
}

func TestRisorValue_String(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		v := &RisorValue{obj: object.NewString("hello")}
		require.Equal(t, "hello", v.String())
	})
	t.Run("int", func(t *testing.T) {
		v := &RisorValue{obj: object.NewInt(42)}
		require.Equal(t, "42", v.String())
	})
	t.Run("float", func(t *testing.T) {
		v := &RisorValue{obj: object.NewFloat(3.14)}
		require.Equal(t, "3.14", v.String())
	})
	t.Run("bool", func(t *testing.T) {
		v := &RisorValue{obj: object.True}
		require.Equal(t, "true", v.String())
	})
	t.Run("time", func(t *testing.T) {
		ts := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		v := &RisorValue{obj: object.NewTime(ts)}
		require.Equal(t, "2025-01-15T12:00:00Z", v.String())
	})
	t.Run("nil", func(t *testing.T) {
		v := &RisorValue{obj: object.Nil}
		require.Equal(t, "", v.String())
	})
	t.Run("list", func(t *testing.T) {
		v := &RisorValue{obj: object.NewList([]object.Object{
			object.NewString("a"),
			object.NewString("b"),
		})}
		s := v.String()
		require.Contains(t, s, "a")
		require.Contains(t, s, "b")
	})
	t.Run("map", func(t *testing.T) {
		v := &RisorValue{obj: object.NewMap(map[string]object.Object{
			"key": object.NewString("value"),
		})}
		s := v.String()
		require.Contains(t, s, "key")
		require.Contains(t, s, "value")
	})
}

func TestRisorScriptingEngine_CompileAndEvaluate(t *testing.T) {
	globals := DefaultRisorGlobals()
	engine := NewRisorScriptingEngine(globals)

	t.Run("simple expression", func(t *testing.T) {
		script, err := engine.Compile(context.Background(), "1 + 2")
		require.NoError(t, err)
		result, err := script.Evaluate(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, int64(3), result.Value())
	})

	t.Run("string expression", func(t *testing.T) {
		script, err := engine.Compile(context.Background(), `"hello" + " world"`)
		require.NoError(t, err)
		result, err := script.Evaluate(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, "hello world", result.Value())
	})

	t.Run("with globals override", func(t *testing.T) {
		// The compiler needs to know about variable names at compile time,
		// so we create an engine that includes x and y as globals
		customGlobals := DefaultRisorGlobals()
		customGlobals["x"] = int64(0)
		customGlobals["y"] = int64(0)
		customEngine := NewRisorScriptingEngine(customGlobals)

		script, err := customEngine.Compile(context.Background(), "x + y")
		require.NoError(t, err)
		result, err := script.Evaluate(context.Background(), map[string]any{
			"x": int64(10),
			"y": int64(20),
		})
		require.NoError(t, err)
		require.Equal(t, int64(30), result.Value())
	})

	t.Run("compile error", func(t *testing.T) {
		_, err := engine.Compile(context.Background(), "{{invalid")
		require.Error(t, err)
	})
}

func TestDefaultRisorGlobals(t *testing.T) {
	globals := DefaultRisorGlobals()
	require.NotNil(t, globals)
	require.NotNil(t, globals["inputs"])
	require.NotNil(t, globals["state"])
	// Should have built-in functions
	require.NotNil(t, globals["len"])
	t.Logf("globals count: %d", len(globals))
}
