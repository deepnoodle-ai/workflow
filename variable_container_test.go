package workflow

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

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
		patches := GeneratePatches(original, modified)
		require.Len(t, patches, 0)
	})

	t.Run("add new variable", func(t *testing.T) {
		original := map[string]any{"a": 1}
		modified := map[string]any{
			"a": 1,
			"b": "new",
		}
		patches := GeneratePatches(original, modified)
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

		patches := GeneratePatches(original, modified)
		require.Len(t, patches, 2)

		var aPatch, bPatch *Patch
		for i := range patches {
			switch patches[i].Variable() {
			case "a":
				aPatch = &patches[i]
			case "b":
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
		modified := map[string]any{"a": 1}
		patches := GeneratePatches(original, modified)
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

		patches := GeneratePatches(original, modified)
		require.Len(t, patches, 3)

		var modifyPatch, addPatch, deletePatch *Patch
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
