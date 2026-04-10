package risor

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/stretchr/testify/require"
)

func newActivityContext(t *testing.T, variables, inputs map[string]any) workflow.Context {
	t.Helper()
	return workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
		PathLocalState: workflow.NewPathLocalState(inputs, variables),
		Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
		Compiler:       NewEngine(DefaultGlobals()),
		PathID:         "test",
		StepName:       "test",
	})
}

func TestScriptActivity_AddNewVariable(t *testing.T) {
	activity := NewScriptActivity()

	variables := map[string]any{"existing_var": "initial_value"}
	inputs := map[string]any{"user_id": 123}

	ctx := newActivityContext(t, variables, inputs)

	params := map[string]any{
		"code": `state["new_variable"] = "hello world"`,
	}

	_, err := activity.Execute(ctx, params)
	require.NoError(t, err)

	value, exists := ctx.GetVariable("existing_var")
	require.True(t, exists)
	require.Equal(t, "initial_value", value)

	value, exists = ctx.GetVariable("new_variable")
	require.True(t, exists)
	require.Equal(t, "hello world", value)

	require.Equal(t, map[string]any{"existing_var": "initial_value"}, variables)
}

func TestScriptActivity_DotAssignNewKeyFails(t *testing.T) {
	activity := NewScriptActivity()

	ctx := newActivityContext(t,
		map[string]any{"existing_var": "initial_value"},
		map[string]any{})

	params := map[string]any{
		"code": `state.new_variable = "hello world"`,
	}

	_, err := activity.Execute(ctx, params)
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exist")
}

func TestScriptActivity_AccessInputs(t *testing.T) {
	activity := NewScriptActivity()

	variables := map[string]any{}
	inputs := map[string]any{
		"user_id": 123,
		"action":  "create",
	}

	ctx := newActivityContext(t, variables, inputs)

	params := map[string]any{
		"code": `
			state["processed_user_id"] = inputs.user_id * 2
			state["action_type"] = inputs.action + "_processed"
		`,
	}

	_, err := activity.Execute(ctx, params)
	require.NoError(t, err)

	value, exists := ctx.GetVariable("processed_user_id")
	require.True(t, exists)
	require.Equal(t, int64(246), value)

	value, exists = ctx.GetVariable("action_type")
	require.True(t, exists)
	require.Equal(t, "create_processed", value)

	require.Equal(t, map[string]any{}, variables)
}

func TestScriptActivity_ErrorCases(t *testing.T) {
	activity := NewScriptActivity()

	t.Run("missing code parameter", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{}, map[string]any{})
		_, err := activity.Execute(ctx, map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing 'code' parameter")
	})

	t.Run("empty code parameter", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{}, map[string]any{})
		_, err := activity.Execute(ctx, map[string]any{"code": ""})
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing 'code' parameter")
	})
}

func TestScriptActivity_Name(t *testing.T) {
	activity := NewScriptActivity()
	require.Equal(t, "script", activity.Name())
}

func TestScriptActivity_VariousTypes(t *testing.T) {
	activity := NewScriptActivity()

	t.Run("handles various Go types in state", func(t *testing.T) {
		variables := map[string]any{
			"str": "hello", "num": 42, "num64": int64(99),
			"flt": 3.14, "flag": true,
			"items": []any{"a", "b"}, "strings": []string{"x", "y"},
			"ints": []int{1, 2}, "nested": map[string]any{"key": "val"},
			"when": time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		ctx := newActivityContext(t, variables, map[string]any{})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.str`})
		require.NoError(t, err)
		require.Equal(t, "hello", result)
	})

	t.Run("handles int32 and float32 in state", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{"i32": int32(7), "f32": float32(2.5)}, map[string]any{})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.i32 + state.f32`})
		require.NoError(t, err)
		require.NotNil(t, result)
	})

	t.Run("handles nil in state", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{"nothing": nil, "keep": "yes"}, map[string]any{})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.keep`})
		require.NoError(t, err)
		require.Equal(t, "yes", result)
	})

	t.Run("handles unknown type in state", func(t *testing.T) {
		original := struct{ X int }{X: 5}
		ctx := newActivityContext(t, map[string]any{"custom": original}, map[string]any{})
		_, err := activity.Execute(ctx, map[string]any{"code": `state.custom`})
		require.NoError(t, err)
		// The original Go value should survive the round-trip through Risor
		// instead of being rewritten as its stringified form.
		v, exists := ctx.GetVariable("custom")
		require.True(t, exists)
		require.Equal(t, original, v)
	})

	t.Run("script compile error", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{}, map[string]any{})
		_, err := activity.Execute(ctx, map[string]any{"code": "{{bad syntax"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to compile")
	})

	t.Run("script runtime error", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{}, map[string]any{})
		_, err := activity.Execute(ctx, map[string]any{"code": "state.nonexistent_var.method()"})
		require.Error(t, err)
	})

	t.Run("modify variable via script", func(t *testing.T) {
		ctx := newActivityContext(t, map[string]any{"counter": 10}, map[string]any{})
		_, err := activity.Execute(ctx, map[string]any{"code": `state.counter = state.counter + 5`})
		require.NoError(t, err)
		v, exists := ctx.GetVariable("counter")
		require.True(t, exists)
		require.Equal(t, int64(15), v)
	})
}
