package workflow

import (
	"fmt"
	"strings"
	"testing"
)

// soundRun validates doc and, if it passes, runs it with the given step
// outputs (keyed "step" or "step#visit") and first-attempt failures.
func soundRun(t *testing.T, doc string, inputs map[string]any, outs map[string]any, fails map[string]bool) string {
	t.Helper()
	def, err := Parse([]byte(doc))
	if err != nil {
		return "PARSE: " + err.Error()
	}
	if err := def.Validate(testKinds); err != nil {
		return "VALIDATE: " + err.Error()
	}
	visits := map[string]int{}
	h := newHost(t, def, func(c Command) result {
		visits[c.Step]++
		if fails[c.Step] && visits[c.Step] == 1 {
			return fail("boom", "x", false)
		}
		if v, found := outs[fmt.Sprintf("%s#%d", c.Step, visits[c.Step])]; found {
			return ok(v)
		}
		return ok(outs[c.Step])
	})
	h.start(inputs, nil)
	h.runToEnd(nil)
	msg := ""
	if h.final != nil && h.final.Error != nil {
		msg = h.final.Error.Type + ": " + h.final.Error.Message
	}
	return fmt.Sprintf("%s %s", h.st.Status, msg)
}

// TestValidatedRunsDoNotFailOnExpressions holds the type system's promise:
// a definition that validates, run with outputs that match their schemas,
// never fails on an expression. Each case was a counterexample in review;
// each must now be rejected by Validate or run cleanly.
func TestValidatedRunsDoNotFailOnExpressions(t *testing.T) {
	cases := []struct {
		name, doc string
		in        map[string]any
		outs      map[string]any
		fails     map[string]bool
	}{
		{"and-returns-operand-when", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"items": {"type": "array", "items": {"type": "string"}}}},
		   "next": [{"step": "b", "when": "a.items && len(a.items) > 0"}]},
		  {"name": "b", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"or-typed-bool-switch", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"mode": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": {"mode": "${a.mode || false}"}, "next": {"switch": "s.mode", "cases": {"true": "l", "false": "r"}}},
		  {"name": "l", "task": "x"}, {"name": "r", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{"mode": "manual"}}, nil},
		{"or-false-positive", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"name": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": {"name": "${a.name || \"anon\"}"}, "next": [{"step": "r", "when": "s.name == \"anon\""}]},
		  {"name": "r", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"try-one-arg", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": "${try(a.n)}"}]}`, nil, map[string]any{"a": map[string]any{"n": "x"}}, nil},
		{"if-two-arg", `{"steps": [
		  {"name": "a", "task": "x", "output": "boolean", "next": ["s"]},
		  {"name": "s", "set": "${if(a, \"yes\")}"}]}`, nil, map[string]any{"a": true}, nil},
		{"upper-optional", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"notes": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": "${upper(a.notes)}"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"enum-null-switch", `{"steps": [
		  {"name": "a", "task": "x", "output": {"enum": ["x", "y", null]}, "next": ["s"]},
		  {"name": "s", "set": "${a + \"!\"}"}]}`, nil, map[string]any{"a": nil}, nil},
		{"coalesce-settled-nil", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "lookup", "when": "s.has"}, {"step": "create"}]},
		  {"name": "lookup", "task": "x", "output": {"type": ["object", "null"], "required": ["id"], "properties": {"id": {"type": "string"}}}, "next": ["read"]},
		  {"name": "create", "task": "x", "output": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}}, "next": ["read"]},
		  {"name": "read", "set": "${coalesce(lookup, create).id}"}]}`, nil, map[string]any{"s": map[string]any{"has": true}, "lookup": nil}, nil},
		{"irreducible-loop", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "x", "when": "s.go"}, {"step": "b"}]},
		  {"name": "x", "set": {"v": 1}, "next": ["a"]},
		  {"name": "a", "task": "x", "max_visits": 3, "with": {"v": "${x.v}"}, "next": ["b"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["again"], "properties": {"again": {"type": "boolean"}}},
		   "next": [{"step": "a", "when": "b.again"}]}]}`, nil, map[string]any{"s": map[string]any{"go": false}, "b": map[string]any{"again": true}, "b#2": map[string]any{"again": false}}, nil},
		{"input-default-not-completed", `{"inputs": {"opts": {"type": "object", "properties": {"verbose": {"type": "boolean"}}, "default": {}}},
		  "steps": [{"name": "s", "set": "${coalesce(inputs.opts.verbose, false)}"}]}`, nil, nil, nil},
		{"input-default-wrong-type", `{"inputs": {"n": {"type": "string", "default": 5}},
		  "steps": [{"name": "s", "set": "${upper(inputs.n)}"}]}`, nil, nil, nil},
		{"outputs-ignore-catch", `{"steps": [
		  {"name": "a", "task": "x", "output": "number", "catch": [{"next": "b"}]},
		  {"name": "b", "task": "x"}], "outputs": {"n": "${coalesce(a, 0) + 1}"}}`, nil, nil, map[string]bool{"a": true}},
		{"additional-props-missing-key", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "additionalProperties": {"type": "number"}}, "next": ["s"]},
		  {"name": "s", "set": "${a.alice + 1}"}]}`, nil, map[string]any{"a": map[string]any{"bob": 1}}, nil},
		{"join-elem", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "a", "when": "s.l"}, {"step": "b"}]},
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["k"], "properties": {"k": {"type": "string"}}}, "next": ["read"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "additionalProperties": {"type": "number"}}, "next": ["read"]},
		  {"name": "read", "set": "${coalesce(a, b).z + 1}"}]}`, nil, map[string]any{"s": map[string]any{"l": true}, "a": map[string]any{"k": "s", "z": "str"}}, nil},
		{"list-eq", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["t", "u"], "properties": {"t": {"type": "array", "items": {"type": "string"}}, "u": {"type": "array", "items": {"type": "string"}}}},
		   "next": [{"step": "b", "when": "a.t == a.u"}]},
		  {"name": "b", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{"t": []any{"x"}, "u": []any{"x"}}}, nil},
		{"round-nil", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "number"}}}, "next": ["s"]},
		  {"name": "s", "set": "${round(a.n) + sum(map(a?.xs, 1))}"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"addl-complete", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "additionalProperties": {"type": "object", "properties": {"score": {"type": "number"}}}}, "next": ["s"]},
		  {"name": "s", "set": "${coalesce(a.alice.score, 0)}"}]}`, nil, map[string]any{"a": map[string]any{"alice": map[string]any{}}}, nil},
		{"alias", `{"inputs": {"note": {"type": "string"}}, "steps": [
		  {"name": "b", "set": "${inputs.note}", "next": ["s"]},
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "a", "when": "s.l"}, {"step": "c"}]},
		  {"name": "a", "set": "${inputs.note}", "next": ["read"]},
		  {"name": "c", "set": "${inputs.note}", "next": ["read"]},
		  {"name": "read", "set": "${coalesce(a, c) + \"!\"}"}]}`, nil, map[string]any{"s": map[string]any{"l": true}}, nil},
	}
	for _, c := range cases {
		got := soundRun(t, c.doc, c.in, c.outs, c.fails)
		if !strings.HasPrefix(got, "VALIDATE") && !strings.HasPrefix(got, "completed") {
			t.Errorf("%s: validated but %s", c.name, got)
		}
	}
}

// TestNoFalsePositives are definitions that are safe and must validate.
func TestNoFalsePositives(t *testing.T) {
	for name, doc := range map[string]string{
		"narrowing": `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "number"}}},
		   "next": [{"step": "b", "when": "a.n != nil && a.n > 3"}]},
		  {"name": "b", "task": "x"}]}`,
		"or default": `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"name": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": {"name": "${a.name || \"anon\"}"}, "next": [{"step": "r", "when": "s.name == \"anon\""}]},
		  {"name": "r", "task": "x"}]}`,
		"loop reads its own last pass": `{"steps": [
		  {"name": "draft", "task": "x", "max_visits": 3, "with": {"prev": "${draft?.text}"},
		   "output": {"type": "object", "required": ["text"], "properties": {"text": {"type": "string"}}}, "next": ["review"]},
		  {"name": "review", "task": "x", "output": {"type": "object", "required": ["ok"], "properties": {"ok": {"type": "boolean"}}},
		   "match": "first", "next": [{"step": "draft", "when": "!review.ok"}, {"step": "publish"}]},
		  {"name": "publish", "task": "x", "with": {"text": "${draft.text}"}}]}`,
	} {
		if got := problemsFor(t, doc, testKinds); got != "" {
			t.Errorf("%s: %s", name, got)
		}
	}
}
