package activities

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/script"
	"github.com/deepnoodle-ai/wonton/assert"
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
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the state was updated
	state := ctx.PathLocalState

	value, exists := state.GetVariable("existing_var")
	assert.True(t, exists)
	assert.Equal(t, value, "initial_value")

	value, exists = state.GetVariable("new_variable")
	assert.True(t, exists)
	assert.Equal(t, value, "hello world")

	// Verify the original map is unchanged
	assert.Equal(t, variables, map[string]any{"existing_var": "initial_value"})
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
	assert.NoError(t, err)
	assert.NotNil(t, result)

	// Verify the state contains the expected values derived from inputs
	state := ctx.PathLocalState

	value, exists := state.GetVariable("processed_user_id")
	assert.True(t, exists)
	assert.Equal(t, value, int64(246))

	value, exists = state.GetVariable("action_type")
	assert.True(t, exists)
	assert.Equal(t, value, "create_processed")

	assert.Equal(t, variables, map[string]any{})
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
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing 'code' parameter")
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
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing 'code' parameter")
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
		assert.Len(t, patches, 0)
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
		assert.Len(t, patches, 1)
		assert.Equal(t, patches[0].Variable(), "b")
		assert.Equal(t, patches[0].Value(), "new")
		assert.False(t, patches[0].Delete())
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
		assert.Len(t, patches, 2)

		// Check both patches exist
		var aPatch, bPatch *workflow.Patch
		for i := range patches {
			if patches[i].Variable() == "a" {
				aPatch = &patches[i]
			} else if patches[i].Variable() == "b" {
				bPatch = &patches[i]
			}
		}

		assert.NotNil(t, aPatch)
		assert.NotNil(t, bPatch)

		assert.Equal(t, aPatch.Value(), 2)
		assert.False(t, aPatch.Delete())

		assert.Equal(t, bPatch.Value(), "new")
		assert.False(t, bPatch.Delete())
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
		assert.Len(t, patches, 1)
		assert.Equal(t, patches[0].Variable(), "b")
		assert.Nil(t, patches[0].Value())
		assert.True(t, patches[0].Delete())
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
		assert.Len(t, patches, 3)

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

		assert.NotNil(t, modifyPatch)
		assert.Equal(t, modifyPatch.Value(), "new_value")
		assert.False(t, modifyPatch.Delete())

		assert.NotNil(t, addPatch)
		assert.Equal(t, addPatch.Value(), "brand_new")
		assert.False(t, addPatch.Delete())

		assert.NotNil(t, deletePatch)
		assert.Nil(t, deletePatch.Value())
		assert.True(t, deletePatch.Delete())
	})
}
