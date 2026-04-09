package activities

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/script"
	"github.com/stretchr/testify/require"
)

func TestScriptActivity_AddNewVariable(t *testing.T) {
	activity := NewScriptActivity()

	// Setup initial state
	variables := map[string]any{"existing_var": "initial_value"}
	inputs := map[string]any{"user_id": 123}

	ctx := workflow.NewContext(context.Background(),
		workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(inputs, variables),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		})

	// Script that sets a new variable
	params := map[string]any{
		"code": `state["new_variable"] = "hello world"`,
	}

	_, err := activity.Execute(ctx, params)
	require.NoError(t, err)

	// Verify the state was updated
	state := ctx.PathLocalState

	value, exists := state.GetVariable("existing_var")
	require.True(t, exists)
	require.Equal(t, "initial_value", value)

	value, exists = state.GetVariable("new_variable")
	require.True(t, exists)
	require.Equal(t, "hello world", value)

	// Verify the original map is unchanged
	require.Equal(t, map[string]any{"existing_var": "initial_value"}, variables)
}

func TestScriptActivity_DotAssignNewKeyFails(t *testing.T) {
	activity := NewScriptActivity()

	variables := map[string]any{"existing_var": "initial_value"}
	inputs := map[string]any{}

	ctx := workflow.NewContext(context.Background(),
		workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(inputs, variables),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		})

	// Dot assignment for a key that doesn't exist should fail in Risor v2
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

	ctx := workflow.NewContext(context.Background(),
		workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(inputs, variables),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		})

	// Script that uses inputs to create state
	params := map[string]any{
		"code": `
			state["processed_user_id"] = inputs.user_id * 2
			state["action_type"] = inputs.action + "_processed"
		`,
	}

	_, err := activity.Execute(ctx, params)
	require.NoError(t, err)

	// Verify the state contains the expected values derived from inputs
	state := ctx.PathLocalState

	value, exists := state.GetVariable("processed_user_id")
	require.True(t, exists)
	require.Equal(t, int64(246), value)

	value, exists = state.GetVariable("action_type")
	require.True(t, exists)
	require.Equal(t, "create_processed", value)

	require.Equal(t, map[string]any{}, variables)
}

func TestScriptActivity_ErrorCases(t *testing.T) {
	activity := NewScriptActivity()

	t.Run("missing code parameter", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{}),
			Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
			PathID:         "test",
			StepName:       "test",
		})
		params := map[string]any{}

		_, err := activity.Execute(ctx, params)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing 'code' parameter")
	})

	t.Run("empty code parameter", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{}),
			Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
			PathID:         "test",
			StepName:       "test",
		})
		params := map[string]any{
			"code": "",
		}

		_, err := activity.Execute(ctx, params)
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing 'code' parameter")
	})
}

// Test the generatePatches function directly
func TestGeneratePatches(t *testing.T) {
	t.Run("no changes", func(t *testing.T) {
		original := map[string]any{
			"a": 1,
			"b": "hello",
		}
		modified := map[string]any{
			"a": 1,
			"b": "hello",
		}
		patches := workflow.GeneratePatches(original, modified)
		require.Len(t, patches, 0)
	})

	t.Run("add new variable", func(t *testing.T) {
		original := map[string]any{
			"a": 1,
		}
		modified := map[string]any{
			"a": 1,
			"b": "new",
		}
		patches := workflow.GeneratePatches(original, modified)
		require.Len(t, patches, 1)
		require.Equal(t, "b", patches[0].Variable())
		require.Equal(t, "new", patches[0].Value())
		require.False(t, patches[0].Delete())
	})

	t.Run("modify existing variable", func(t *testing.T) {
		original := map[string]any{
			"a": 1,
			"b": "old",
		}
		modified := map[string]any{
			"a": 2,
			"b": "new",
		}

		patches := workflow.GeneratePatches(original, modified)
		require.Len(t, patches, 2)

		// Check both patches exist
		var aPatch, bPatch *workflow.Patch
		for i := range patches {
			if patches[i].Variable() == "a" {
				aPatch = &patches[i]
			} else if patches[i].Variable() == "b" {
				bPatch = &patches[i]
			}
		}

		require.NotNil(t, aPatch)
		require.NotNil(t, bPatch)

		require.Equal(t, 2, aPatch.Value())
		require.False(t, aPatch.Delete())

		require.Equal(t, "new", bPatch.Value())
		require.False(t, bPatch.Delete())
	})

	t.Run("delete variable", func(t *testing.T) {
		original := map[string]any{
			"a": 1,
			"b": "delete_me",
		}
		modified := map[string]any{
			"a": 1,
		}
		patches := workflow.GeneratePatches(original, modified)
		require.Len(t, patches, 1)
		require.Equal(t, "b", patches[0].Variable())
		require.Nil(t, patches[0].Value())
		require.True(t, patches[0].Delete())
	})

	t.Run("mixed operations", func(t *testing.T) {
		original := map[string]any{
			"keep":   "unchanged",
			"modify": "old_value",
			"delete": "remove_me",
		}
		modified := map[string]any{
			"keep":   "unchanged",
			"modify": "new_value",
			"add":    "brand_new",
		}

		patches := workflow.GeneratePatches(original, modified)
		require.Len(t, patches, 3)

		// Organize patches by type
		var modifyPatch, addPatch, deletePatch *workflow.Patch
		for i := range patches {
			switch patches[i].Variable() {
			case "modify":
				modifyPatch = &patches[i]
			case "add":
				addPatch = &patches[i]
			case "delete":
				deletePatch = &patches[i]
			}
		}

		require.NotNil(t, modifyPatch)
		require.Equal(t, "new_value", modifyPatch.Value())
		require.False(t, modifyPatch.Delete())

		require.NotNil(t, addPatch)
		require.Equal(t, "brand_new", addPatch.Value())
		require.False(t, addPatch.Delete())

		require.NotNil(t, deletePatch)
		require.Nil(t, deletePatch.Value())
		require.True(t, deletePatch.Delete())
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
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, variables),
		})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.str`})
		require.NoError(t, err)
		require.Equal(t, "hello", result)
	})

	t.Run("handles int32 and float32 in state", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{"i32": int32(7), "f32": float32(2.5)}),
		})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.i32 + state.f32`})
		require.NoError(t, err)
		require.NotNil(t, result)
	})

	t.Run("handles nil in state", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{"nothing": nil, "keep": "yes"}),
		})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.keep`})
		require.NoError(t, err)
		require.Equal(t, "yes", result)
	})

	t.Run("handles unknown type in state", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{"custom": struct{ X int }{X: 5}}),
		})
		result, err := activity.Execute(ctx, map[string]any{"code": `state.custom`})
		require.NoError(t, err)
		require.NotNil(t, result)
	})

	t.Run("script compile error", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{}),
			Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		})
		_, err := activity.Execute(ctx, map[string]any{"code": "{{bad syntax"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to compile")
	})

	t.Run("script runtime error", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{}),
			Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
			Compiler:       script.NewRisorScriptingEngine(script.DefaultRisorGlobals()),
		})
		_, err := activity.Execute(ctx, map[string]any{"code": "state.nonexistent_var.method()"})
		require.Error(t, err)
	})

	t.Run("modify variable via script", func(t *testing.T) {
		ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
			PathLocalState: workflow.NewPathLocalState(map[string]any{}, map[string]any{"counter": 10}),
		})
		_, err := activity.Execute(ctx, map[string]any{"code": `state.counter = state.counter + 5`})
		require.NoError(t, err)
		v, exists := ctx.GetVariable("counter")
		require.True(t, exists)
		require.Equal(t, int64(15), v)
	})
}
