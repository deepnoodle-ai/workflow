package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

const outputsDoc = `{
  "schemas": {
    "review": {"type": "object", "required": ["verdict", "body"],
               "properties": {"verdict": {"enum": ["APPROVE", "REQUEST_CHANGES"]}, "body": {"type": "string"},
                              "notes": {"type": "string"}}}
  },
  "steps": [{"name": "review", "action": "x", "output": "review"}],
  "outputs": {
    "verdict": "${{ review.verdict }}",
    "summary": {"value": "${{ review.body }}", "schema": {"type": "string"},
                "description": "The review's summary."},
    "review": {"value": "${{ review }}", "schema": "review"},
    "literal": {"value": {"value": 1}}
  }
}`

func TestOutputsLongForm(t *testing.T) {
	def := mustLoad(t, outputsDoc)
	require.Len(t, def.Outputs, 4)
	require.Equal(t, "${{ review.verdict }}", def.Outputs["verdict"].Value)
	require.Nil(t, def.Outputs["verdict"].Schema)
	require.Equal(t, "${{ review.body }}", def.Outputs["summary"].Value)
	require.Equal(t, []string{"string"}, def.Outputs["summary"].Schema.Type)
	require.Equal(t, "The review's summary.", def.Outputs["summary"].Description)
	require.Equal(t, map[string]any{"value": float64(1)}, def.Outputs["literal"].Value)

	h := newHost(t, def, func(Command) result {
		return ok(map[string]any{"verdict": "APPROVE", "body": "LGTM"})
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{
		"verdict": "APPROVE",
		"summary": "LGTM",
		// A schema'd output is completed like a step output: a declared
		// property that is absent reads as nil.
		"review":  map[string]any{"verdict": "APPROVE", "body": "LGTM", "notes": nil},
		"literal": map[string]any{"value": float64(1)},
	}, h.final.Outputs)
}

func TestOutputSchema(t *testing.T) {
	def := mustLoad(t, outputsDoc)
	data, err := json.Marshal(def.OutputSchema())
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, map[string]any{
		"type":                 "object",
		"required":             []any{"literal", "review", "summary", "verdict"},
		"additionalProperties": false,
		"properties": map[string]any{
			"literal": map[string]any{},
			"review": map[string]any{
				"type": "object", "required": []any{"verdict", "body"},
				"properties": map[string]any{
					"verdict": map[string]any{"enum": []any{"APPROVE", "REQUEST_CHANGES"}},
					"body":    map[string]any{"type": "string"},
					"notes":   map[string]any{"type": "string"},
				},
			},
			"summary": map[string]any{"type": "string", "description": "The review's summary."},
			"verdict": map[string]any{},
		},
	}, got)
	// The schema a host publishes is valid JSON Schema the library reads
	// back.
	_, err = ParseSchema(data)
	require.NoError(t, err)

	// Without outputs there is no contract: the outputs are whatever steps
	// the run's paths end at.
	require.Nil(t, mustLoad(t, `{"steps": [{"name": "a", "action": "x"}]}`).OutputSchema())
}

// A description or default beside a $ref describes that output, and the
// published schema keeps it.
func TestOutputSchemaKeepsRefAnnotations(t *testing.T) {
	def := mustLoad(t, `{"schemas": {"label": {"type": ["string", "null"]}},
	  "steps": [{"name": "a", "action": "x", "output": "string"}],
	  "outputs": {"l": {"value": "${{ a }}",
	    "schema": {"$ref": "label", "description": "the label", "default": "none"}}}}`)
	l := def.OutputSchema().Properties["l"]
	require.Equal(t, "the label", l.Description)
	require.Equal(t, any("none"), l.Default)
	require.Equal(t, []string{"string", "null"}, l.Type)
}

func TestOutputsLoadChecksSchemas(t *testing.T) {
	const steps = `"steps": [
	  {"name": "a", "action": "x", "output": {"type": "object", "required": ["n", "kind"],
	   "properties": {"n": {"type": "number"}, "kind": {"enum": ["x", "y"]}, "note": {"type": "string"}}},
	   "next": [{"step": "b", "when": "a.n > 1"}]},
	  {"name": "b", "action": "x", "output": "string"}]`
	cases := []struct{ outputs, want string }{
		{`{"n": {"value": "${{ a.n }}", "schema": "number"}}`, ""},
		{`{"n": {"value": "${{ a.n }}", "schema": "string"}}`, "outputs.n: number is not string"},
		{`{"n": {"value": "${{ a.note }}", "schema": "string"}}`,
			"outputs.n: may be nil, but the schema requires string"},
		{`{"n": {"value": "${{ a.note }}", "schema": {"type": ["string", "null"]}}}`, ""},
		{`{"n": {"value": "${{ b }}", "schema": "string"}}`, "outputs.n: may be nil"},
		{`{"n": {"value": "${{ coalesce(b, \"none\") }}", "schema": "string"}}`, ""},
		{`{"n": {"value": "${{ a.kind }}", "schema": {"enum": ["x", "y", "z"]}}}`, ""},
		{`{"n": {"value": "${{ a.kind }}", "schema": {"enum": ["x"]}}}`,
			`outputs.n: "y" is not one of "x"`},
		{`{"n": {"value": {"k": "${{ a.kind }}"}, "schema": {"type": "object", "required": ["k", "m"], "properties": {"k": {"type": "string"}, "m": {"type": "string"}}}}}`, `outputs.n: missing required "m"`},
		{`{"n": {"value": {"k": 1}, "schema": {"type": "object", "properties": {"k": {"type": "number"}}, "additionalProperties": false}}}`, ""},
		{`{"n": {"value": {"k": 1, "j": 2}, "schema": {"type": "object", "properties": {"k": {"type": "number"}}, "additionalProperties": false}}}`, `outputs.n: unexpected "j"`},
		{`{"n": {"value": "${{ a.n > 1 && a.kind }}", "schema": "string"}}`,
			"outputs.n: bool is not string"},
		{`{"n": {"value": "${{ a }}", "schema": "nope"}}`, `unknown schema "nope"`},
		{`{"n": {"schema": "string"}}`, "outputs.n: the long form needs a value"},
		{`{"n": {"value": 1, "type": "string"}}`, `outputs.n: unknown key "type"`},
		{`{"n": {"value": 1, "description": 2}}`, "outputs.n.description must be a string"},
		{`{"n": {"value": "${{ a.nn }}", "schema": "number"}}`, `no field "nn"`},
	}
	for _, c := range cases {
		_, err := Load([]byte(`{`+steps+`, "outputs": `+c.outputs+`}`), testKinds)
		if c.want == "" {
			require.NoError(t, err, c.outputs)
			continue
		}
		require.Error(t, err, c.outputs)
		require.Contains(t, err.Error(), c.want, c.outputs)
	}
}

func TestOutputsSchemaValidatedAtCompletion(t *testing.T) {
	// a has no schema, so Load cannot prove anything about its value;
	// Apply checks it when the run completes.
	def := mustLoad(t, `{
	  "steps": [{"name": "a", "action": "x"}],
	  "on_failure": [{"name": "report", "action": "x", "with": {"why": "${{ error.type }}"}}],
	  "outputs": {
	    "n": {"value": "${{ a.n }}", "schema": "integer"},
	    "s": {"value": "${{ a.s }}", "schema": {"type": "object", "required": ["k"]}},
	    "ok": "${{ a.n }}"
	  }}`)
	h := newHost(t, def, func(c Command) result {
		if c.Step == "report" {
			return ok(nil)
		}
		return ok(map[string]any{"n": 1.5, "s": map[string]any{}})
	})
	h.start(nil)
	cmds := h.deliver("main/a#1")
	// The run fails like any other failure: on_failure runs.
	require.Equal(t, []string{"failure/report#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, ErrorTypeInvalidOutput, cmds[0].Params["why"])
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeInvalidOutput, h.final.Error.Type)
	require.Equal(t, "", h.final.Error.Step)
	msg := `outputs do not match their schemas: outputs.n: expected integer, got number; ` +
		`outputs.s: missing required property "k"`
	require.Equal(t, msg, h.final.Error.Message)
	require.Nil(t, h.st.Outputs)

	// A value that matches completes the run.
	h = newHost(t, def, func(Command) result {
		return ok(map[string]any{"n": 2, "s": map[string]any{"k": true}})
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	want := map[string]any{
		"n":  float64(2),
		"s":  map[string]any{"k": true},
		"ok": float64(2),
	}
	require.Equal(t, want, h.st.Outputs)
}

func TestOutputsShortFormObjectsStayTemplates(t *testing.T) {
	// An object without value or schema is a short-form template object.
	def := mustLoad(t, `{"steps": [{"name": "a", "set": 3}],
	  "outputs": {"o": {"n": "${{ a }}", "description": "text"}}}`)
	require.Nil(t, def.Outputs["o"].Schema)
	require.Equal(t, "", def.Outputs["o"].Description)
	h := newHost(t, def, nil)
	h.start(nil)
	require.Equal(t, map[string]any{"o": map[string]any{"n": float64(3), "description": "text"}},
		h.st.Outputs)
	require.True(t, strings.Contains(mustJSON(t, def), `"description":"text"`))
}

// TestRequireOutputs loads a definition against the output schema of the
// step that will run it, as a host does for a generated child workflow.
func TestRequireOutputs(t *testing.T) {
	slot, err := ParseSchema([]byte(`{"type": "object", "required": ["done", "summary"],
	  "additionalProperties": false,
	  "properties": {"done": {"type": "boolean"}, "summary": {"type": "string"}}}`))
	require.NoError(t, err)

	// work has a typed output; raw's is unchecked.
	const steps = `"steps": [
	  {"name": "work", "action": "x", "next": "raw",
	   "output": {"type": "object", "required": ["finished", "note"],
	              "properties": {"finished": {"type": "boolean"}, "note": {"type": "string"}}}},
	  {"name": "raw", "action": "x"}]`
	load := func(outputs string) (*Definition, error) {
		return Load([]byte(`{`+steps+outputs+`}`), testKinds, RequireOutputs(slot))
	}

	for name, tc := range map[string]struct{ outputs, want string }{
		"fits": {
			outputs: `, "outputs": {"done": "${{ work.finished }}", "summary": "${{ work.note }}"}`,
		},
		"an output's schema types an unchecked value": {
			outputs: `, "outputs": {
			  "done": {"value": "${{ raw.ok }}", "schema": {"type": "boolean"}},
			  "summary": "${{ work.note }}"}`,
		},
		"missing": {
			outputs: `, "outputs": {"done": "${{ work.finished }}"}`,
			want:    `outputs: missing required "summary" (required by the caller)`,
		},
		"wrong type": {
			outputs: `, "outputs": {"done": "${{ work.note }}", "summary": "${{ work.note }}"}`,
			want:    `outputs.done: string is not boolean (required by the caller)`,
		},
		"unexpected": {
			outputs: `, "outputs": {
			  "done": "${{ work.finished }}",
			  "summary": "${{ work.note }}",
			  "extra": "${{ work.note }}"}`,
			want: `outputs: unexpected "extra" (required by the caller)`,
		},
		"no outputs": {
			want: `outputs: the caller requires done, summary; declare outputs`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(tc.outputs)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}

	// A definition that loads fills the slot when it completes.
	def, err := load(`, "outputs": {"done": "${{ work.finished }}", "summary": "${{ work.note }}"}`)
	require.NoError(t, err)

	h := newHost(t, def, func(Command) result {
		return ok(map[string]any{"finished": true, "note": "fixed"})
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Len(t, slot.validate(h.final.Outputs), 0)

	// A nil schema checks nothing.
	doc := []byte(`{"steps": [{"name": "a", "action": "x"}]}`)
	_, err = Load(doc, testKinds, RequireOutputs(nil))
	require.NoError(t, err)
}

// A caller schema with nothing required still needs declared outputs:
// without them, the outputs are whichever steps the paths end at.
func TestRequireOutputsNeedsDeclaredOutputs(t *testing.T) {
	slot := mustSchema(t, `{"type": "object", "additionalProperties": false,
	  "properties": {"summary": {"type": "string"}}}`)

	_, err := Load([]byte(`{"steps": [{"name": "summary", "action": "x"}]}`),
		testKinds, RequireOutputs(slot))
	require.Error(t, err)
	require.Contains(t, err.Error(),
		"outputs: the caller requires outputs that fit its schema; declare outputs")
}

// A slot taken from a loaded definition keeps its refs bound to that
// definition, whether the host passes it to RequireOutputs or returns it
// from KindSchemas.
func TestCallerSchemaRefsStayBound(t *testing.T) {
	parent := mustLoad(t, `{
	  "schemas": {"result": {"type": "object", "required": ["done"],
	                         "properties": {"done": {"type": "boolean"}}}},
	  "steps": [{"name": "execute", "action": "x", "output": "result"}]}`)
	slot := parent.byName["execute"].Output
	require.Equal(t, "result", slot.Ref)

	// The child has no schema named result.
	_, err := Load([]byte(`{"steps": [{"name": "a", "action": "x"}], "outputs": {"done": true}}`),
		testKinds, RequireOutputs(slot))
	require.NoError(t, err)

	// The child's own result is a different schema.
	_, err = Load([]byte(`{"schemas": {"result": {"type": "string"}},
	  "steps": [{"name": "a", "action": "x"}], "outputs": {"done": true}}`),
		testKinds, RequireOutputs(slot))
	require.NoError(t, err)

	// The same holds for a host kind whose output is the slot.
	kinds := schemaKinds{"child": {nil, slot}}
	_, err = Load([]byte(`{"schemas": {"result": {"type": "string"}},
	  "steps": [{"name": "c", "child": "x", "next": "after"},
	            {"name": "after", "set": "${{ c.done }}"}]}`), kinds)
	require.NoError(t, err)
}
