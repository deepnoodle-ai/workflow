package script

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecuteScript(t *testing.T) {
	compiler := NewRisorScriptingEngine(DefaultRisorGlobals())

	t.Run("set new state variable", func(t *testing.T) {
		state := map[string]any{"existing": "hello"}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler, `state["new_var"] = "world"`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, "hello", result.State["existing"])
		require.Equal(t, "world", result.State["new_var"])
	})

	t.Run("modify existing state variable", func(t *testing.T) {
		state := map[string]any{"counter": int64(1)}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler, `state.counter += 1`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, int64(2), result.State["counter"])
	})

	t.Run("read inputs", func(t *testing.T) {
		state := map[string]any{}
		inputs := map[string]any{"user_id": 42, "action": "create"}

		result, err := ExecuteScript(context.Background(), compiler,
			`state["result"] = string(inputs.user_id) + "_" + inputs.action`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, "42_create", result.State["result"])
	})

	t.Run("return value", func(t *testing.T) {
		state := map[string]any{}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler, `1 + 2`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, int64(3), result.Value)
	})

	t.Run("use allowed builtins", func(t *testing.T) {
		state := map[string]any{"items": []any{"a", "b", "c"}}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler, `len(state.items)`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, int64(3), result.Value)
	})

	t.Run("dot assign to nonexistent key fails", func(t *testing.T) {
		state := map[string]any{}
		inputs := map[string]any{}

		_, err := ExecuteScript(context.Background(), compiler,
			`state.missing = "value"`, state, inputs)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not exist")
	})

	t.Run("compile error", func(t *testing.T) {
		state := map[string]any{}
		inputs := map[string]any{}

		_, err := ExecuteScript(context.Background(), compiler, `}{invalid`, state, inputs)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to compile")
	})

	t.Run("original state is not mutated", func(t *testing.T) {
		state := map[string]any{"x": int64(1)}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler, `state["y"] = 2`, state, inputs)
		require.NoError(t, err)

		// Original Go map should be untouched
		_, hasY := state["y"]
		require.False(t, hasY)

		// Result state should have the new key
		require.Equal(t, int64(2), result.State["y"])
	})

	t.Run("nested map state", func(t *testing.T) {
		state := map[string]any{
			"config": map[string]any{"timeout": 30},
		}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler, `state.config.timeout`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, int64(30), result.Value)
	})

	t.Run("boolean state", func(t *testing.T) {
		state := map[string]any{"enabled": true}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler,
			`state.enabled`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, true, result.Value)
	})

	t.Run("if expression", func(t *testing.T) {
		state := map[string]any{"flag": true}
		inputs := map[string]any{}

		result, err := ExecuteScript(context.Background(), compiler,
			`if (state.flag) { "yes" } else { "no" }`, state, inputs)
		require.NoError(t, err)
		require.Equal(t, "yes", result.Value)
	})
}
