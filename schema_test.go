package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/expr"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// A host returns the same *Schema to every definition it loads. Load must
// never write into it: concurrent Loads would race, and one definition's
// named schemas would leak into another's.
func TestHostSchemasAreNotMutated(t *testing.T) {
	shared := mustSchema(t, `{"type": "object", "required": ["v"], "properties": {"v": "item", "list": {"type": "array", "items": "item"}}}`)
	kinds := schemaKinds{"action": {nil, shared}}
	before, err := json.Marshal(shared)
	require.NoError(t, err)

	doc := func(item string) string {
		return `{"schemas": {"item": "` + item + `"}, "steps": [{"name": "a", "action": "x"}]}`
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
			got := problemsFor(t, `{"schemas": `+tc.schemas+`, "steps": [{"name": "a", "action": "x", "output": "`+
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
	b.WriteString(`"s40": "string"}, "steps": [{"name": "a", "action": "x", "output": "s0"}]}`)
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
	  "steps": [{"name": "a", "action": "x", "output": "pair"}]}`)
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

// TestSchemaTypes pins the schema → expr type mapping documented in
// documentation/definitions.md.
func TestSchemaTypes(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{`{}`, `any`},
		{`{"description": "anything"}`, `any`},
		{`{"properties": {"a": "string"}}`, `any`},
		{`"string"`, `string`},
		{`"integer"`, `number`},
		{`{"type": "null"}`, `nil`},
		{`{"type": ["string", "null"]}`, `string?`},
		{`{"type": ["string", "number"]}`, `number | string`},
		{`{"type": ["string", "integer", "number", "null"]}`, `(number | string)?`},
		{`{"type": ["object", "array"], "items": "string"}`, `list<string> | {...}`},
		{`{"enum": ["x", "y"]}`, `"x" | "y"`},
		{`{"enum": ["x", 1]}`, `1 | "x"`},
		{`{"enum": ["a", null]}`, `"a"?`},
		{`{"type": "string", "enum": ["a", null, 1]}`, `"a"`},
		{`{"type": ["string", "null"], "enum": ["a", null]}`, `"a"?`},
		{`{"type": "integer", "enum": [1, 1.5]}`, `1`},
		{`{"type": "object"}`, `{...}`},
		{`{"type": ["object", "null"]}`, `{...}?`},
		{`{"type": "object", "additionalProperties": false}`, `{}`},
		{`{"type": "object", "required": ["a"], "properties": {"a": "string", "b": "number", "c": {"type": "object"}}}`,
			`{a: string, b: number?, c: {...}?}`},
		{`{"type": "object", "additionalProperties": "number"}`, `{...}`},
		{`{"type": "array"}`, `list<any>`},
		{`{"type": "array", "items": {"enum": ["a", "b"]}}`, `list<"a" | "b">`},
	} {
		s := mustSchema(t, tc.schema)
		require.Equal(t, tc.want, s.exprType().String(), tc.schema)
	}
	// An open object's other fields have the additionalProperties type.
	require.Equal(t, "number", mustSchema(t, `{"type": "object", "additionalProperties": "number"}`).exprType().Elem.String())
}

// nullable and validate agree with the type about nil.
func TestNullability(t *testing.T) {
	for schema, want := range map[string]bool{
		`{}`: true, `"string"`: false, `{"type": ["string", "null"]}`: true,
		`{"enum": ["a", null]}`: true, `{"enum": ["a"]}`: false,
		`{"type": ["string", "null"], "enum": ["a"]}`: false,
		`{"type": "string", "enum": ["a", null]}`:     false,
	} {
		s := mustSchema(t, schema)
		require.Equal(t, want, s.nullable(), schema)
		require.Equal(t, want, len(s.validate(nil)) == 0, schema)
		typ := s.exprType()
		require.Equal(t, want, typ.Kind == expr.KindAny && !typ.IsUnion() || typ.Optional || typ.Kind == expr.KindNull, schema)
	}
}

func TestCompleteFillsAdditionalValues(t *testing.T) {
	s := mustSchema(t, `{"type": "object", "properties": {"n": {"type": "object", "properties": {"x": "string"}}},
	  "additionalProperties": {"type": "object", "properties": {"score": "number"}}}`)
	got := s.complete(map[string]any{"alice": map[string]any{}, "n": map[string]any{}})
	require.Equal(t, map[string]any{"alice": map[string]any{"score": nil}, "n": map[string]any{"x": nil}}, got)
}

func TestIntegerIsExact(t *testing.T) {
	s := mustSchema(t, `"integer"`)
	for _, v := range []float64{0, -3, 1e19, 1 << 63, -(1 << 63), 1e300, 9007199254740993} {
		require.Len(t, s.validate(v), 0, v)
	}
	for _, v := range []float64{0.5, -1.25, 1e15 + 0.5} {
		require.Len(t, s.validate(v), 1, v)
	}
}
