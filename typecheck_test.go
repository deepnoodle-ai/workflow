package workflow

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// problemsFor validates doc and returns its problems as "step: message".
func problemsFor(t *testing.T, doc string, kinds Kinds) string {
	t.Helper()
	_, err := Load([]byte(doc), kinds)
	if err == nil {
		return ""
	}
	var ve *ValidationError
	require.True(t, errors.As(err, &ve), err)
	lines := make([]string, len(ve.Problems))
	for i, p := range ve.Problems {
		lines[i] = p.Step + ": " + p.Message
	}
	return strings.Join(lines, "\n")
}

func TestTypesCatchMistakesInPRReview(t *testing.T) {
	for name, tc := range map[string]struct{ from, to, want string }{
		"misspelled output field": {
			`"each": "concat(correctness.items,`, `"each": "concat(correctness.itmes,`,
			`verify: each: ` + "`correctness.itmes`" + `: no field "itmes" in {items: list<string>} (did you mean "items"?)`},
		"misspelled item field": {
			`any(verify, it.confirmed)`, `any(verify, it.confirmd)`,
			`no field "confirmd"`},
		"value outside an enum": {
			`"state": "${{ coalesce(report, approve).status }}"`, `"state": "${{ coalesce(report, approve).status == \"sucess\" }}"`,
			`never "sucess"`},
		"reading an alternative that may not have run": {
			`"event": "${{ coalesce(report, approve).verdict }}"`, `"event": "${{ report.verdict }}"`,
			"post: with: `report`: may be nil here"},
		"when that is not a boolean": {
			`"when": "any(verify, it.confirmed)"`, `"when": "filter(verify, it.confirmed)"`,
			`when must be a boolean, got list<{confirmed: bool, finding: string}>`},
		"input field that is not in its schema": {
			`${{ input.event.pull_request.head.sha }}`, `${{ input.event.pull_request.sha }}`,
			`no field "sha" in {head: {sha: string}, number: number}`},
	} {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, prReview, tc.from)
			got := problemsFor(t, strings.Replace(prReview, tc.from, tc.to, 1), testKinds)
			require.Contains(t, got, tc.want)
		})
	}
}

const classifyDoc = `{
  "schemas": {"triage": {"type": "object", "required": ["kind"],
     "properties": {"kind": {"enum": ["bug_fix", "feature", "other"]}, "notes": {"type": "string"}}}},
  "steps": [
    {"name": "classify", "agent": "triage", "output": "triage",
     "next": {"switch": "classify.kind", "cases": %s %s}},
    {"name": "fix", "task": "x", "next": ["done"]},
    {"name": "feature", "task": "x", "next": ["done"]},
    {"name": "other", "task": "x", "next": ["done"]},
    {"name": "done", "set": {"handled_by": "${{ coalesce(fix, feature, other) }}", "notes": "${{ classify.notes }}"}}
  ]
}`

func classify(cases, def string) string {
	return strings.Replace(strings.Replace(classifyDoc, "%s", cases, 1), "%s", def, 1)
}

func TestSwitchIsCheckedAgainstTheEnum(t *testing.T) {
	all := `{"bug_fix": "fix", "feature": "feature", "other": "other"}`
	require.Equal(t, "", problemsFor(t, classify(all, ""), testKinds))

	got := problemsFor(t, classify(`{"bug_fix": "fix", "feature": "feature"}`, ""), testKinds)
	require.Contains(t, got, `classify: next: switch on classify.kind does not handle other; add cases or a default`)

	got = problemsFor(t, classify(`{"bugfix": "fix", "feature": "feature", "other": "other"}`, `, "default": "fix"`), testKinds)
	require.Contains(t, got, `next: case "bugfix": classify.kind is never "bugfix" (it is one of bug_fix, feature, other)`)

	// A default covers what the cases do not.
	require.Equal(t, "", problemsFor(t, classify(`{"bug_fix": "fix", "feature": "feature"}`, `, "default": "other"`), testKinds))

	// Reading an optional property needs ?. .
	require.Contains(t, problemsFor(t, strings.Replace(classify(all, ""), `${{ classify.notes }}`, `${{ classify.notes + \"!\" }}`, 1), testKinds),
		"`classify.notes`: may be nil here")
}

func TestSwitchRoutes(t *testing.T) {
	def := mustLoad(t, classify(`{"bug_fix": "fix", "feature": ["feature", "other"]}`, `, "default": []`))
	for kind, want := range map[string][]StepID{
		"bug_fix": {"main/classify#1", "main/fix#1"},
		"feature": {"main.feature/feature#1", "main.other/other#1", "main/classify#1"},
		"other":   {"main/classify#1"},
	} {
		h := newHost(t, def, func(c Command) result { return ok(map[string]any{"kind": kind}) })
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, StatusCompleted, h.st.Status, kind)
		require.Equal(t, want, h.dispatched(), kind)
	}
}

func TestSwitchOnUntypedValueNeedsDefault(t *testing.T) {
	doc := `{"steps": [
	  {"name": "a", "task": "x", "next": {"switch": "a.route", "cases": {"left": "l"} %s}},
	  {"name": "l", "task": "x"}, {"name": "r", "task": "x"}]}`
	require.Contains(t, problemsFor(t, strings.Replace(doc, "%s", "", 1), testKinds),
		"needs a default: its type is unknown")
	def := mustLoad(t, strings.Replace(doc, "%s", `, "default": "r"`, 1))
	h := newHost(t, def, func(c Command) result { return ok(map[string]any{"route": "up"}) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, []StepID{"main/a#1", "main/r#1"}, h.dispatched())

	// A boolean switch must handle both values.
	got := problemsFor(t, `{"steps": [
	  {"name": "a", "task": "x", "output": "boolean", "next": {"switch": "a", "cases": {"true": "l"}}},
	  {"name": "l", "task": "x"}]}`, testKinds)
	require.Contains(t, got, "does not handle false")
}

func TestWhenMustBeBooleanAtRunTime(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "a", "task": "x", "next": [{"step": "b", "when": "a.items"}]},
	  {"name": "b", "task": "x"}]}`)
	h := newHost(t, def, func(c Command) result { return ok(map[string]any{"items": []any{1}}) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Contains(t, h.final.Error.Message, "when must be a boolean")
}

func TestSettledAnalysis(t *testing.T) {
	doc := `{"steps": [
	  {"name": "start", "task": "x", "next": ["a", {"step": "b", "when": "start.go"}]},
	  {"name": "a", "task": "x", "output": {"type": "object", "required": ["v"], "properties": {"v": {"type": "number"}}}, "next": ["j"]},
	  {"name": "b", "task": "x", "output": {"type": "object", "required": ["v"], "properties": {"v": {"type": "number"}}}, "next": ["j"]},
	  {"name": "j", "join": %s, "next": ["read"]},
	  {"name": "read", "set": "%s"}]}`
	build := func(join, read string) string {
		return strings.Replace(strings.Replace(doc, "%s", join, 1), "%s", read, 1)
	}
	// An unnamed join only guarantees what every arrival saw.
	require.Contains(t, problemsFor(t, build(`[]`, `${{ a.v + b.v }}`), testKinds), "`b`: may be nil here")
	require.Contains(t, problemsFor(t, build(`[]`, `${{ a.v + b.v }}`), testKinds), "`a`: may be nil here")
	require.Equal(t, "", problemsFor(t, build(`[]`, `${{ a?.v }}`), testKinds))
	// A named join guarantees what it names.
	require.Equal(t, "", problemsFor(t, build(`["a", "b"]`, `${{ a.v + b.v }}`), testKinds))
}

func TestCoalesceCoversAlternatives(t *testing.T) {
	doc := `{"steps": [
	  {"name": "s", "task": "x", "match": "first", "next": [{"step": "a", "when": "s.left"}, {"step": "b"}]},
	  {"name": "a", "set": {"n": 1}, "next": ["read"]},
	  {"name": "b", "set": {"n": 2}, "next": ["read"]},
	  {"name": "read", "set": "%s"}]}`
	require.Equal(t, "", problemsFor(t, strings.Replace(doc, "%s", `${{ coalesce(a, b).n + 1 }}`, 1), testKinds))
	require.Contains(t, problemsFor(t, strings.Replace(doc, "%s", `${{ coalesce(a).n }}`, 1), testKinds), "may be nil")

	def := mustLoad(t, strings.Replace(doc, "%s", `${{ coalesce(a, b).n }}`, 1))
	for left, want := range map[bool]float64{true: 1, false: 2} {
		h := newHost(t, def, func(c Command) result { return ok(map[string]any{"left": left}) })
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, map[string]any{"read": want}, h.st.Outputs)
	}
}

// schemaKinds is a host that knows its kinds' schemas.
type schemaKinds map[string][2]*Schema

func (k schemaKinds) Known(kind string) bool                      { _, ok := k[kind]; return ok }
func (k schemaKinds) ValidateConfig(string, map[string]any) error { return nil }
func (k schemaKinds) Schemas(kind string, _ map[string]any) (*Schema, *Schema, error) {
	return k[kind][0], k[kind][1], nil
}

func mustSchema(t *testing.T, doc string) *Schema {
	t.Helper()
	s, err := ParseSchema([]byte(doc))
	require.NoError(t, err)
	return s
}

func statusKinds(t *testing.T) schemaKinds {
	return schemaKinds{
		"set_status": {
			mustSchema(t, `{"type": "object", "required": ["sha", "state"], "additionalProperties": false,
			  "properties": {"sha": {"type": "string"}, "state": {"enum": ["pending", "success", "failure", "error"]},
			                 "description": {"type": "string"}}}`),
			mustSchema(t, `{"type": "object", "required": ["id"], "properties": {"id": {"type": "integer"}}}`),
		},
		"task": {nil, nil},
	}
}

func TestParamsAreCheckedAgainstKindSchemas(t *testing.T) {
	kinds := statusKinds(t)
	got := problemsFor(t, `{"input": {"type": "object", "required": ["sha"], "properties": {"sha": {"type": "string"}, "note": {"type": "string"}}},
	  "steps": [
	    {"name": "a", "set_status": "github", "with": {"sha": "${{ input.sha }}", "state": "pendng"}, "next": ["b"]},
	    {"name": "b", "set_status": "github", "with": {"state": "success", "color": "red"}, "next": ["c"]},
	    {"name": "c", "set_status": "github", "with": {"sha": "${{ a.id }}", "state": "error", "description": "${{ input.note }}"}, "next": ["d"]},
	    {"name": "d", "task": "x", "with": {"id": "${{ c.idd }}"}}]}`, kinds)
	for _, want := range []string{
		`a: with.state: "pendng" is not one of "error", "failure", "pending", "success"`,
		`b: with: missing required "sha"`,
		`b: with: unexpected "color"`,
		`c: with.sha: number is not string`,
		`d: with: ` + "`c.idd`" + `: no field "idd" in {id: number}`,
	} {
		require.Contains(t, got, want)
	}
	// An optional value fits an optional property.
	require.NotContains(t, got, "description")
}

func TestParamsAndOutputsAreValidatedAtRunTime(t *testing.T) {
	kinds := statusKinds(t)
	def, err := Load([]byte(`{"steps": [
	  {"name": "a", "task": "x", "next": ["b"]},
	  {"name": "b", "set_status": "github", "with": {"sha": "${{ a.sha }}", "state": "${{ a.state }}"}}]}`), kinds)
	require.NoError(t, err)

	// a is untyped, so b's params are checked when b is dispatched.
	h := newHost(t, def, func(c Command) result { return ok(map[string]any{"sha": "abc", "state": "bogus"}) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeInvalidParams, h.final.Error.Type)
	require.Contains(t, h.final.Error.Message, `state: "bogus" is not one of`)
	require.Len(t, h.dispatched(), 1, "b never dispatched")

	// b's output must match the kind's output schema.
	h = newHost(t, def, func(c Command) result {
		if c.Step == "a" {
			return ok(map[string]any{"sha": "abc", "state": "success"})
		}
		require.NotNil(t, c.OutputSchema)
		return ok(map[string]any{"id": "not a number"})
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeInvalidOutput, h.final.Error.Type)
	require.Contains(t, h.final.Error.Message, "id: expected integer, got string")
}

func TestOutputSchemaInCommand(t *testing.T) {
	def := mustLoad(t, classify(`{"bug_fix": "fix", "feature": "feature", "other": "other"}`, ""))
	h := newHost(t, def, nil)
	cmds := h.apply(Event{Kind: EventStart})
	data, err := json.Marshal(cmds[0].OutputSchema)
	require.NoError(t, err)
	require.Equal(t, `{"properties":{"kind":{"enum":["bug_fix","feature","other"]},"notes":{"type":"string"}},"required":["kind"],"type":"object"}`, string(data))

	// A command survives a round trip through a host's outbox.
	cmdJSON, err := json.Marshal(cmds[0])
	require.NoError(t, err)
	var back Command
	require.NoError(t, json.Unmarshal(cmdJSON, &back))
	again, err := json.Marshal(back)
	require.NoError(t, err)
	require.Equal(t, string(cmdJSON), string(again))
}

func TestInputSchemaValidatesInput(t *testing.T) {
	def := mustLoad(t, prReview)
	_, _, err := Apply(def, nil, Event{At: t0, Kind: EventStart, Input: map[string]any{"event": map[string]any{
		"repository": map[string]any{"full_name": "x"}, "pull_request": map[string]any{"number": "42"},
	}}})
	require.ErrorIs(t, err, ErrInvalidInput)
	require.Contains(t, err.Error(), "input.event.pull_request.number: expected integer, got string")
	require.Contains(t, err.Error(), `input.event.pull_request: missing required property "head"`)
}

func TestErrorTypeReadsInOnFailure(t *testing.T) {
	got := problemsFor(t, `{"steps": [{"name": "a", "task": "x"}],
	  "on_failure": [{"task": "x", "with": {"t": "${{ error.type }}", "m": "${{ error.mesage }}"}}]}`, testKinds)
	require.NotContains(t, got, "error.type")
	require.Contains(t, got, `no field "mesage"`)
}

func TestOutputsSeeWhatSettledOnEveryEnd(t *testing.T) {
	obj := `{"type": "object", "required": ["v"], "properties": {"v": {"type": "number"}}}`
	for name, tc := range map[string]struct{ doc, want string }{
		"the start step has always run": {`{"steps": [
		  {"name": "a", "task": "x", "output": ` + obj + `, "next": ["b"]},
		  {"name": "b", "task": "x"}], "outputs": {"v": "${{ a.v }}"}}`, ""},
		"a path can end before a later step": {`{"steps": [
		  {"name": "a", "task": "x", "output": ` + obj + `, "next": [{"step": "b", "when": "a.v > 1"}]},
		  {"name": "b", "task": "x", "output": ` + obj + `}], "outputs": {"v": "${{ b.v }}"}}`, "outputs: `b`: may be nil"},
		"a switch case that ends the path": {`{"steps": [
		  {"name": "a", "task": "x", "output": {"enum": ["go", "stop"]}, "next": {"switch": "a", "cases": {"go": "b", "stop": []}}},
		  {"name": "b", "task": "x", "output": ` + obj + `}], "outputs": {"v": "${{ b.v }}"}}`, "outputs: `b`: may be nil"},
		"alternatives that converge": {`{"steps": [
		  {"name": "a", "task": "x", "output": ` + obj + `, "match": "first", "next": [{"step": "b", "when": "a.v > 1"}, {"step": "c"}]},
		  {"name": "b", "task": "x", "output": ` + obj + `},
		  {"name": "c", "task": "x", "output": ` + obj + `}], "outputs": {"v": "${{ coalesce(b, c).v }}"}}`, ""},
		"branches that end separately": {`{"steps": [
		  {"name": "a", "task": "x", "output": ` + obj + `, "next": ["b", "c"]},
		  {"name": "b", "task": "x", "output": ` + obj + `},
		  {"name": "c", "task": "x", "output": ` + obj + `}], "outputs": {"v": "${{ b.v + c.v }}"}}`, ""},
		"a branch that may end early": {`{"steps": [
		  {"name": "a", "task": "x", "output": ` + obj + `, "next": ["b", "c"]},
		  {"name": "b", "task": "x", "output": ` + obj + `},
		  {"name": "c", "task": "x", "output": ` + obj + `, "next": [{"step": "d", "when": "c.v > 1"}]},
		  {"name": "d", "task": "x", "output": ` + obj + `}], "outputs": {"v": "${{ b.v + d.v }}"}}`, "outputs: `d`: may be nil"},
		"a caught failure": {`{"steps": [
		  {"name": "a", "task": "x", "output": ` + obj + `, "catch": "b", "next": ["c"]},
		  {"name": "b", "task": "x"},
		  {"name": "c", "task": "x", "output": ` + obj + `}], "outputs": {"v": "${{ c.v }}"}}`, "outputs: `c`: may be nil"},
	} {
		t.Run(name, func(t *testing.T) {
			got := problemsFor(t, tc.doc, testKinds)
			if tc.want == "" {
				require.Equal(t, "", got)
			} else {
				require.Contains(t, got, tc.want)
			}
		})
	}
}

func TestEachItemIsTyped(t *testing.T) {
	got := problemsFor(t, `{"steps": [
	  {"name": "list", "task": "x", "output": {"type": "array", "items": {"type": "object", "required": ["title"],
	    "properties": {"title": {"type": "string"}}}}, "next": ["each"]},
	  {"name": "each", "task": "x", "each": "list", "with": {"t": "${{ item.titel }}", "i": "${{ index + 1 }}"}}]}`, testKinds)
	require.Contains(t, got, `each: with: `+"`item.titel`"+`: no field "titel" in {title: string} (did you mean "title"?)`)
}

// Switch keys for numbers are written as Switch.route encodes them.
func TestSwitchOnNumbers(t *testing.T) {
	doc := `{"steps": [
	  {"name": "a", "task": "x", "output": {"enum": [1, 1000000, 2.5]}, "next": {"switch": "a", "cases": %s}},
	  {"name": "l", "task": "x"}, {"name": "r", "task": "x"}, {"name": "h", "task": "x"}]}`
	require.Contains(t, problemsFor(t, strings.Replace(doc, "%s", `{"1": "l", "1e+06": "r", "2.5": "h"}`, 1), testKinds),
		`next: case "1e+06": a is never "1e+06" (it is one of 1, 1000000, 2.5)`)
	def := mustLoad(t, strings.Replace(doc, "%s", `{"1": "l", "1000000": "r", "2.5": "h"}`, 1))
	for v, want := range map[float64]StepID{1: "main/l#1", 1000000: "main/r#1", 2.5: "main/h#1"} {
		h := newHost(t, def, func(c Command) result { return ok(v) })
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, StatusCompleted, h.st.Status)
		require.Equal(t, []StepID{"main/a#1", want}, h.dispatched())
	}
}

// Set steps are typed to a fixpoint, so a step that reads one around a
// loop sees its type.
func TestSetTypesAcrossLoops(t *testing.T) {
	loop := `{"steps": [
	  {"name": "a", "task": "x", "max_visits": 3, "with": {"n": "%s"}, "next": ["b"]},
	  {"name": "b", "task": "x", "output": {"type": "object", "required": ["again"], "properties": {"again": {"type": "boolean"}}}, "next": ["s"]},
	  {"name": "s", "set": %s, "next": [{"step": "a", "when": "b.again"}]}]}`
	build := func(with, set string) string {
		return strings.Replace(strings.Replace(loop, "%s", with, 1), "%s", set, 1)
	}
	// A counter that reads its own last pass.
	require.Equal(t, "", problemsFor(t, build(`${{ coalesce(s?.n, 0) + 1 }}`, `{"n": "${{ coalesce(s?.n, 0) + 1 }}"}`), testKinds))
	require.Contains(t, problemsFor(t, build(`${{ s?.v + 1 }}`, `{"v": "str"}`), testKinds), "a: with: ")
	// A type that grows on every pass is reported.
	require.Contains(t, problemsFor(t, build(`x`, `{"v": ["${{ s }}"]}`), testKinds),
		"s: set: its type does not settle: it depends on its own value through a loop")
}

func TestParamsCheckUnionsAndAbsentWith(t *testing.T) {
	kinds := schemaKinds{
		"task": {nil, nil},
		"num":  {mustSchema(t, `{"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}, "m": {"enum": [1, 2]}}}`), nil},
	}
	got := problemsFor(t, `{"steps": [
	  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"name": {"type": "string"}}}, "next": ["b", "c", "d"]},
	  {"name": "b", "num": "x", "with": {"n": "${{ a.name || 0 }}"}},
	  {"name": "c", "num": "x"},
	  {"name": "d", "num": "x", "with": {"n": 1, "m": "${{ a?.name }}"}}],
	  "on_failure": [{"name": "f", "num": "x"}]}`, kinds)
	for _, want := range []string{
		`b: with.n: string is not number`,
		`c: with: missing required "n"`,
		`d: with.m: string is not one of 1, 2`,
		`f: with: missing required "n"`,
	} {
		require.Contains(t, got, want)
	}
	require.NotContains(t, got, "b: with.n: number")
}
