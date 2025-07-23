package activities

import (
	"context"
	"log/slog"
	"os"
	"testing"

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
		})

	// Script that sets a new variable
	params := map[string]any{
		"code": `state.new_variable = "hello world"`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

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
		})

	// Script that uses inputs to create state
	params := map[string]any{
		"code": `
			state.processed_user_id = inputs.user_id * 2
			state.action_type = inputs.action + "_processed"
		`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result)

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
