package workflow

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// A host returns the same *Schema to every definition it loads. Load must
// never write into it: concurrent Loads would race, and one definition's
// named schemas would leak into another's.
func TestHostSchemasAreNotMutated(t *testing.T) {
	shared := mustSchema(t, `{"type": "object", "required": ["v"], "properties": {"v": "item", "list": {"type": "array", "items": "item"}}}`)
	kinds := schemaKinds{"task": {nil, shared}}
	before, err := json.Marshal(shared)
	require.NoError(t, err)

	doc := func(item string) string {
		return `{"schemas": {"item": "` + item + `"}, "steps": [{"name": "a", "task": "x"}]}`
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Load([]byte(doc("string")), kinds)
			require.NoError(t, err)
		}()
	}
	wg.Wait()

	def1, err := Load([]byte(doc("string")), kinds)
	require.NoError(t, err)
	_, err = Load([]byte(doc("number")), kinds)
	require.NoError(t, err)
	require.Len(t, def1.Step("a").Output.validate(map[string]any{"v": "hello"}), 0)

	after, err := json.Marshal(shared)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
}
