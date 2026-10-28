package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestRecursiveSchemasAreRejected(t *testing.T) {
	for name, tc := range map[string]struct{ schemas, want string }{
		"tree": {`{"node": {"type": "object", "properties": {"left": "node", "right": "node"}}}`,
			`schemas.node: refers to itself (node → node); recursive schemas are not supported`},
		"alias cycle": {`{"a": "b", "b": "a"}`,
			`schemas.a: refers to itself (a → b → a); recursive schemas are not supported`},
		"self alias": {`{"a": "a"}`, `schemas.a: refers to itself (a → a)`},
		"through a list": {`{"node": {"type": "object", "properties": {"kids": {"type": "array", "items": "kid"}}}, "kid": "node"}`,
			`refers to itself (kid → node → kid)`},
	} {
		t.Run(name, func(t *testing.T) {
			got := problemsFor(t, `{"schemas": `+tc.schemas+`, "steps": [{"name": "a", "task": "x", "output": "`+
				strings.SplitN(strings.Trim(tc.schemas, "{ "), `"`, 3)[1]+`"}]}`, testKinds)
			require.Contains(t, got, tc.want)
		})
	}
}

// Refs shared down a chain of named schemas double the inlined size at
// every level. Load rejects such a schema quickly rather than letting
// every dispatch marshal it.
func TestSchemaSizeIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"schemas": {`)
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, `"s%d": {"type": "object", "properties": {"l": "s%d", "r": "s%d"}}, `, i, i+1, i+1)
	}
	b.WriteString(`"s40": "string"}, "steps": [{"name": "a", "task": "x", "output": "s0"}]}`)
	start := time.Now()
	got := problemsFor(t, b.String(), testKinds)
	require.Contains(t, got, "schemas.s0: has more than 10000 nodes with its refs inlined")
	require.Contains(t, got, "a: schema has more than 10000 nodes")
	require.True(t, time.Since(start) < 2*time.Second, time.Since(start))
}

// A shared named schema marshals inline, compactly, and round-trips.
func TestSchemaMarshalRoundTrips(t *testing.T) {
	def := mustLoad(t, `{"schemas": {"id": {"type": "string", "description": "an id"},
	  "pair": {"type": "object", "required": ["a"], "properties": {"a": "id", "b": "id", "tags": {"type": "array", "items": "id"}}}},
	  "steps": [{"name": "a", "task": "x", "output": "pair"}]}`)
	data, err := json.Marshal(def.Step("a").Output)
	require.NoError(t, err)
	id := `{"description":"an id","type":"string"}`
	require.Equal(t, `{"properties":{"a":`+id+`,"b":`+id+`,"tags":{"items":`+id+`,"type":"array"}},"required":["a"],"type":"object"}`, string(data))
	var back Schema
	require.NoError(t, json.Unmarshal(data, &back))
	again, err := json.Marshal(&back)
	require.NoError(t, err)
	require.Equal(t, string(data), string(again))
}
