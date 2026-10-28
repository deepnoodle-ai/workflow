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
  "steps": [{"name": "review", "task": "x", "output": "review"}],
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
	h.start(nil, nil)
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
	require.Nil(t, mustLoad(t, `{"steps": [{"name": "a", "task": "x"}]}`).OutputSchema())
}

func TestOutputsLoadChecksSchemas(t *testing.T) {
	const steps = `"steps": [
	  {"name": "a", "task": "x", "output": {"type": "object", "required": ["n", "kind"],
	   "properties": {"n": {"type": "number"}, "kind": {"enum": ["x", "y"]}, "note": {"type": "string"}}},
	   "next": [{"step": "b", "when": "a.n > 1"}]},
	  {"name": "b", "task": "x", "output": "string"}]`
	cases := []struct{ outputs, want string }{
		{`{"n": {"value": "${{ a.n }}", "schema": "number"}}`, ""},
		{`{"n": {"value": "${{ a.n }}", "schema": "string"}}`, "outputs.n: number is not string"},
		{`{"n": {"value": "${{ a.note }}", "schema": "string"}}`, "outputs.n: may be nil, but the schema requires string"},
		{`{"n": {"value": "${{ a.note }}", "schema": {"type": ["string", "null"]}}}`, ""},
		{`{"n": {"value": "${{ b }}", "schema": "string"}}`, "outputs.n: may be nil"},
		{`{"n": {"value": "${{ coalesce(b, \"none\") }}", "schema": "string"}}`, ""},
		{`{"n": {"value": "${{ a.kind }}", "schema": {"enum": ["x", "y", "z"]}}}`, ""},
		{`{"n": {"value": "${{ a.kind }}", "schema": {"enum": ["x"]}}}`, `outputs.n: "y" is not one of "x"`},
		{`{"n": {"value": {"k": "${{ a.kind }}"}, "schema": {"type": "object", "required": ["k", "m"], "properties": {"k": {"type": "string"}, "m": {"type": "string"}}}}}`, `outputs.n: missing required "m"`},
		{`{"n": {"value": {"k": 1}, "schema": {"type": "object", "properties": {"k": {"type": "number"}}, "additionalProperties": false}}}`, ""},
		{`{"n": {"value": {"k": 1, "j": 2}, "schema": {"type": "object", "properties": {"k": {"type": "number"}}, "additionalProperties": false}}}`, `outputs.n: unexpected "j"`},
		{`{"n": {"value": "${{ a.n > 1 && a.kind }}", "schema": "string"}}`, "outputs.n: bool is not string"},
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
	  "steps": [{"name": "a", "task": "x"}],
	  "on_failure": [{"name": "report", "task": "x", "with": {"why": "${{ error.type }}"}}],
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
	h.start(nil, nil)
	cmds := h.deliver("main/a#1")
	// The run fails like any other failure: on_failure runs.
	require.Equal(t, []string{"failure/report#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, ErrorTypeInvalidOutput, cmds[0].Params["why"])
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeInvalidOutput, h.final.Error.Type)
	require.Equal(t, "", h.final.Error.Step)
	require.Equal(t, `outputs do not match their schemas: outputs.n: expected integer, got number; `+
		`outputs.s: missing required property "k"`, h.final.Error.Message)
	require.Nil(t, h.st.Outputs)

	// A value that matches completes the run.
	h = newHost(t, def, func(Command) result { return ok(map[string]any{"n": 2, "s": map[string]any{"k": true}}) })
	h.start(nil, nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{"n": float64(2), "s": map[string]any{"k": true}, "ok": float64(2)}, h.st.Outputs)
}

func TestOutputsShortFormObjectsStayTemplates(t *testing.T) {
	// An object without value or schema is a short-form template object.
	def := mustLoad(t, `{"steps": [{"name": "a", "set": 3}],
	  "outputs": {"o": {"n": "${{ a }}", "description": "text"}}}`)
	require.Nil(t, def.Outputs["o"].Schema)
	require.Equal(t, "", def.Outputs["o"].Description)
	h := newHost(t, def, nil)
	h.start(nil, nil)
	require.Equal(t, map[string]any{"o": map[string]any{"n": float64(3), "description": "text"}}, h.st.Outputs)
	require.True(t, strings.Contains(mustJSON(t, def), `"description":"text"`))
}
