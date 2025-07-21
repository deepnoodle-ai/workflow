package activities

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStateReader implements workflow state reader for testing
type mockStateReader struct {
	inputs    map[string]any
	variables map[string]any
	patches   []state.Patch
}

func newMockStateReader(inputs, variables map[string]any) *mockStateReader {
	return &mockStateReader{
		inputs:    copyMap(inputs),
		variables: copyMap(variables),
		patches:   []state.Patch{},
	}
}

func (m *mockStateReader) GetInputs() map[string]any {
	return copyMap(m.inputs)
}

func (m *mockStateReader) GetVariables() map[string]any {
	return copyMap(m.variables)
}

func (m *mockStateReader) ApplyPatches(patches []state.Patch) {
	m.patches = append(m.patches, patches...)

	// Apply patches to the mock state for verification
	for _, patch := range patches {
		if patch.Delete {
			delete(m.variables, patch.Variable)
		} else {
			m.variables[patch.Variable] = patch.Value
		}
	}
}

func (m *mockStateReader) GetAppliedPatches() []state.Patch {
	return m.patches
}

// Helper function to copy map (already exists in execution_state.go but needed for tests)
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	copy := make(map[string]any, len(m))
	for k, v := range m {
		copy[k] = v
	}
	return copy
}

func TestScriptActivity_AddNewVariable(t *testing.T) {
	activity := &ScriptActivity{}

	// Setup initial state
	initialVars := map[string]any{
		"existing_var": "initial_value",
	}
	inputs := map[string]any{
		"user_id": 123,
	}

	stateReader := newMockStateReader(inputs, initialVars)

	ctx := context.Background()
	ctx = workflow.WithState(ctx, stateReader)

	// Script that adds a new variable
	params := map[string]any{
		"code": `state.new_variable = "hello world"`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify patches were applied
	patches := stateReader.GetAppliedPatches()
	require.Len(t, patches, 1)

	patch := patches[0]
	assert.Equal(t, "new_variable", patch.Variable)
	assert.Equal(t, "hello world", patch.Value)
	assert.False(t, patch.Delete)

	// Verify the state was updated
	finalVars := stateReader.GetVariables()
	assert.Equal(t, "initial_value", finalVars["existing_var"])
	assert.Equal(t, "hello world", finalVars["new_variable"])
}

func TestScriptActivity_ModifyExistingVariable(t *testing.T) {
	activity := &ScriptActivity{}

	// Setup initial state
	initialVars := map[string]any{
		"counter": 5,
		"name":    "Alice",
	}
	inputs := map[string]any{}

	stateReader := newMockStateReader(inputs, initialVars)

	ctx := context.Background()
	ctx = workflow.WithState(ctx, stateReader)

	// Script that modifies existing variables
	params := map[string]any{
		"code": `
			state.counter = state.counter + 10
			state.name = "Bob"
		`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify patches were applied
	patches := stateReader.GetAppliedPatches()
	require.Len(t, patches, 2)

	// Find patches by variable name
	var counterPatch, namePatch *state.Patch
	for i := range patches {
		if patches[i].Variable == "counter" {
			counterPatch = &patches[i]
		} else if patches[i].Variable == "name" {
			namePatch = &patches[i]
		}
	}

	require.NotNil(t, counterPatch)
	require.NotNil(t, namePatch)

	assert.Equal(t, int64(15), counterPatch.Value)
	assert.False(t, counterPatch.Delete)

	assert.Equal(t, "Bob", namePatch.Value)
	assert.False(t, namePatch.Delete)

	// Verify the state was updated
	finalVars := stateReader.GetVariables()
	assert.Equal(t, int64(15), finalVars["counter"])
	assert.Equal(t, "Bob", finalVars["name"])
}

func TestScriptActivity_DeleteVariable(t *testing.T) {
	activity := &ScriptActivity{}

	// Setup initial state
	initialVars := map[string]any{
		"temp_var": "delete_me",
		"keep_var": "keep_me",
	}
	inputs := map[string]any{}

	stateReader := newMockStateReader(inputs, initialVars)

	ctx := context.Background()
	ctx = workflow.WithState(ctx, stateReader)

	// Script that deletes a variable by setting it to nil
	params := map[string]any{
		"code": `
			// Set the key to nil to mark it for deletion
			state.temp_var = nil
		`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify patches were applied
	patches := stateReader.GetAppliedPatches()
	require.Len(t, patches, 1)

	patch := patches[0]
	assert.Equal(t, "temp_var", patch.Variable)
	assert.Nil(t, patch.Value)
	assert.True(t, patch.Delete)

	// Verify the state was updated
	finalVars := stateReader.GetVariables()
	assert.Equal(t, "keep_me", finalVars["keep_var"])
	_, exists := finalVars["temp_var"]
	assert.False(t, exists)
}

func TestScriptActivity_NoChanges(t *testing.T) {
	activity := &ScriptActivity{}

	// Setup initial state
	initialVars := map[string]any{
		"static_var": "unchanged",
	}
	inputs := map[string]any{
		"input_val": 42,
	}

	stateReader := newMockStateReader(inputs, initialVars)

	ctx := context.Background()
	ctx = workflow.WithState(ctx, stateReader)

	// Script that reads but doesn't modify state
	params := map[string]any{
		"code": `state.static_var + " processed"`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, "unchanged processed", result)

	// Verify no patches were applied
	patches := stateReader.GetAppliedPatches()
	assert.Len(t, patches, 0)

	// Verify the state is unchanged
	finalVars := stateReader.GetVariables()
	assert.Equal(t, "unchanged", finalVars["static_var"])
}

func TestScriptActivity_ComplexDataTypes(t *testing.T) {
	activity := &ScriptActivity{}

	// Setup initial state with complex data types
	initialVars := map[string]any{
		"user": map[string]any{
			"id":   1,
			"name": "Alice",
		},
		"tags": []string{"go", "workflow"},
	}
	inputs := map[string]any{}

	stateReader := newMockStateReader(inputs, initialVars)

	ctx := context.Background()
	ctx = workflow.WithState(ctx, stateReader)

	// Script that modifies complex data
	params := map[string]any{
		"code": `
			state.user.name = "Bob"
			state.user.email = "bob@example.com"
			state.tags = state.tags + ["risor"]
			state.metadata = {"created": "2024-01-01"}
		`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify patches were applied (should have patches for user, tags, and metadata)
	patches := stateReader.GetAppliedPatches()
	require.True(t, len(patches) >= 3, "Expected at least 3 patches, got %d", len(patches))

	// Verify the state contains the expected changes
	finalVars := stateReader.GetVariables()

	// Check user object was updated
	user, ok := finalVars["user"].(map[string]any)
	require.True(t, ok, "user should be a map")
	assert.Equal(t, "Bob", user["name"])
	assert.Equal(t, "bob@example.com", user["email"])

	// Check tags were updated (Risor may convert to []any)
	tagsInterface := finalVars["tags"]
	require.NotNil(t, tagsInterface)

	// Convert to []any to check contents
	tags, ok := tagsInterface.([]any)
	if ok {
		// Check if "risor" is in the list
		found := false
		for _, tag := range tags {
			if tag == "risor" {
				found = true
				break
			}
		}
		assert.True(t, found, "Should contain 'risor' tag")
	} else {
		// Fallback check in case it's still []string
		stringTags, ok := tagsInterface.([]string)
		require.True(t, ok, "tags should be either []any or []string")
		assert.Contains(t, stringTags, "risor")
	}

	// Check new metadata was added
	metadata, exists := finalVars["metadata"]
	assert.True(t, exists)
	assert.NotNil(t, metadata)
}

func TestScriptActivity_AccessInputs(t *testing.T) {
	activity := &ScriptActivity{}

	// Setup initial state
	initialVars := map[string]any{}
	inputs := map[string]any{
		"user_id": 123,
		"action":  "create",
	}

	stateReader := newMockStateReader(inputs, initialVars)

	ctx := context.Background()
	ctx = workflow.WithState(ctx, stateReader)

	// Script that uses inputs to create state
	params := map[string]any{
		"code": `
			state.processed_user_id = inputs.user_id * 2
			state.action_type = inputs.action + "_processed"
		`,
	}

	result, err := activity.Execute(ctx, params)
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Verify patches were applied
	patches := stateReader.GetAppliedPatches()
	require.Len(t, patches, 2)

	// Verify the state contains the expected values derived from inputs
	finalVars := stateReader.GetVariables()
	assert.Equal(t, int64(246), finalVars["processed_user_id"]) // 123 * 2
	assert.Equal(t, "create_processed", finalVars["action_type"])
}

func TestScriptActivity_ErrorCases(t *testing.T) {
	activity := &ScriptActivity{}

	t.Run("missing code parameter", func(t *testing.T) {
		ctx := context.Background()
		params := map[string]any{}

		_, err := activity.Execute(ctx, params)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing 'code' parameter")
	})

	t.Run("empty code parameter", func(t *testing.T) {
		ctx := context.Background()
		params := map[string]any{
			"code": "",
		}

		_, err := activity.Execute(ctx, params)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing 'code' parameter")
	})

	t.Run("missing state in context", func(t *testing.T) {
		ctx := context.Background()
		params := map[string]any{
			"code": "state.test = 1",
		}

		_, err := activity.Execute(ctx, params)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "missing state reader in context")
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

		patches := generatePatches(original, modified)
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

		patches := generatePatches(original, modified)
		require.Len(t, patches, 1)

		assert.Equal(t, "b", patches[0].Variable)
		assert.Equal(t, "new", patches[0].Value)
		assert.False(t, patches[0].Delete)
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

		patches := generatePatches(original, modified)
		require.Len(t, patches, 2)

		// Check both patches exist
		var aPatch, bPatch *state.Patch
		for i := range patches {
			if patches[i].Variable == "a" {
				aPatch = &patches[i]
			} else if patches[i].Variable == "b" {
				bPatch = &patches[i]
			}
		}

		require.NotNil(t, aPatch)
		require.NotNil(t, bPatch)

		assert.Equal(t, 2, aPatch.Value)
		assert.False(t, aPatch.Delete)

		assert.Equal(t, "new", bPatch.Value)
		assert.False(t, bPatch.Delete)
	})

	t.Run("delete variable", func(t *testing.T) {
		original := map[string]any{
			"a": 1,
			"b": "delete_me",
		}
		modified := map[string]any{
			"a": 1,
		}

		patches := generatePatches(original, modified)
		require.Len(t, patches, 1)

		assert.Equal(t, "b", patches[0].Variable)
		assert.Nil(t, patches[0].Value)
		assert.True(t, patches[0].Delete)
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

		patches := generatePatches(original, modified)
		require.Len(t, patches, 3)

		// Organize patches by type
		var modifyPatch, addPatch, deletePatch *state.Patch
		for i := range patches {
			switch patches[i].Variable {
			case "modify":
				modifyPatch = &patches[i]
			case "add":
				addPatch = &patches[i]
			case "delete":
				deletePatch = &patches[i]
			}
		}

		require.NotNil(t, modifyPatch)
		assert.Equal(t, "new_value", modifyPatch.Value)
		assert.False(t, modifyPatch.Delete)

		require.NotNil(t, addPatch)
		assert.Equal(t, "brand_new", addPatch.Value)
		assert.False(t, addPatch.Delete)

		require.NotNil(t, deletePatch)
		assert.Nil(t, deletePatch.Value)
		assert.True(t, deletePatch.Delete)
	})
}
