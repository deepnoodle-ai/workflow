package workflow

import (
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// soundRun validates doc and, if it passes, runs it with the given step
// outputs (keyed "step" or "step#visit") and first-attempt failures.
func soundRun(t *testing.T, doc string, inputs map[string]any, outs map[string]any, fails map[string]bool) string {
	t.Helper()
	return soundRunKinds(t, testKinds, doc, inputs, outs, fails)
}

func soundRunKinds(t *testing.T, kinds Kinds, doc string, inputs map[string]any, outs map[string]any, fails map[string]bool) string {
	t.Helper()
	def, err := Load([]byte(doc), kinds)
	if err != nil {
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
		if of := h.final.Error.OnFailure; of != nil {
			msg += "; on_failure " + of.Type + ": " + of.Message
		}
	}
	return fmt.Sprintf("%s %s", h.st.Status, msg)
}

// TestValidatedRunsDoNotFailOnExpressions holds the type system's promise:
// a definition that validates, run with outputs that match their schemas,
// never fails on an expression. Each case was a counterexample in review;
// each must now be rejected by Load or run cleanly.
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
		  {"name": "s", "set": {"mode": "${{ a.mode || false }}"}, "next": {"switch": "s.mode", "cases": {"true": "l", "false": "r"}}},
		  {"name": "l", "task": "x"}, {"name": "r", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{"mode": "manual"}}, nil},
		{"or-false-positive", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"name": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": {"name": "${{ a.name || \"anon\" }}"}, "next": [{"step": "r", "when": "s.name == \"anon\""}]},
		  {"name": "r", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"try-one-arg", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ try(a.n) }}"}]}`, nil, map[string]any{"a": map[string]any{"n": "x"}}, nil},
		{"if-two-arg", `{"steps": [
		  {"name": "a", "task": "x", "output": "boolean", "next": ["s"]},
		  {"name": "s", "set": "${{ if(a, \"yes\") }}"}]}`, nil, map[string]any{"a": true}, nil},
		{"upper-optional", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"notes": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ upper(a.notes) }}"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"enum-null-switch", `{"steps": [
		  {"name": "a", "task": "x", "output": {"enum": ["x", "y", null]}, "next": ["s"]},
		  {"name": "s", "set": "${{ a + \"!\" }}"}]}`, nil, map[string]any{"a": nil}, nil},
		{"coalesce-settled-nil", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "lookup", "when": "s.has"}, {"step": "create"}]},
		  {"name": "lookup", "task": "x", "output": {"type": ["object", "null"], "required": ["id"], "properties": {"id": {"type": "string"}}}, "next": ["read"]},
		  {"name": "create", "task": "x", "output": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}}, "next": ["read"]},
		  {"name": "read", "set": "${{ coalesce(lookup, create).id }}"}]}`, nil, map[string]any{"s": map[string]any{"has": true}, "lookup": nil}, nil},
		{"irreducible-loop", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "x", "when": "s.go"}, {"step": "b"}]},
		  {"name": "x", "set": {"v": 1}, "next": ["a"]},
		  {"name": "a", "task": "x", "max_visits": 3, "with": {"v": "${{ x.v }}"}, "next": ["b"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["again"], "properties": {"again": {"type": "boolean"}}},
		   "next": [{"step": "a", "when": "b.again"}]}]}`, nil, map[string]any{"s": map[string]any{"go": false}, "b": map[string]any{"again": true}, "b#2": map[string]any{"again": false}}, nil},
		{"input-default-not-completed", `{"inputs": {"opts": {"type": "object", "properties": {"verbose": {"type": "boolean"}}, "default": {}}},
		  "steps": [{"name": "s", "set": "${{ coalesce(inputs.opts.verbose, false) }}"}]}`, nil, nil, nil},
		{"input-default-wrong-type", `{"inputs": {"n": {"type": "string", "default": 5}},
		  "steps": [{"name": "s", "set": "${{ upper(inputs.n) }}"}]}`, nil, nil, nil},
		{"outputs-ignore-catch", `{"steps": [
		  {"name": "a", "task": "x", "output": "number", "catch": [{"next": "b"}]},
		  {"name": "b", "task": "x"}], "outputs": {"n": "${{ coalesce(a, 0) + 1 }}"}}`, nil, nil, map[string]bool{"a": true}},
		{"outputs-early-end", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["go"], "properties": {"go": {"type": "boolean"}}},
		   "next": [{"step": "b", "when": "a.go"}]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}}}],
		  "outputs": {"n": "${{ b.n + 1 }}"}}`, nil, map[string]any{"a": map[string]any{"go": false}}, nil},
		{"outputs-one-branch-ends", `{"steps": [
		  {"name": "a", "task": "x", "next": ["b", "c"]},
		  {"name": "b", "task": "x", "output": "number"},
		  {"name": "c", "task": "x", "output": "number", "next": [{"step": "d", "when": "c > 1"}]},
		  {"name": "d", "task": "x", "output": "number"}],
		  "outputs": {"n": "${{ b + d }}"}}`, nil, map[string]any{"b": 1, "c": 0}, nil},
		{"outputs-schema-early-end", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["go"], "properties": {"go": {"type": "boolean"}}},
		   "next": [{"step": "b", "when": "a.go"}]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}}}],
		  "outputs": {"n": {"value": "${{ b?.n }}", "schema": "number"}}}`, nil, map[string]any{"a": map[string]any{"go": false}}, nil},
		{"outputs-schema-early-end-coalesce", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["go"], "properties": {"go": {"type": "boolean"}}},
		   "next": [{"step": "b", "when": "a.go"}]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}}}],
		  "outputs": {"n": {"value": "${{ coalesce(b?.n, 0) }}", "schema": "number"}}}`, nil, map[string]any{"a": map[string]any{"go": false}}, nil},
		{"outputs-schema-caught", `{"steps": [
		  {"name": "a", "task": "x", "output": "number", "catch": [{"next": "b"}]},
		  {"name": "b", "task": "x"}], "outputs": {"n": {"value": "${{ a }}", "schema": "number"}}}`, nil, nil, map[string]bool{"a": true}},
		{"outputs-schema-optional-field", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"note": {"type": "string"}}}}],
		  "outputs": {"n": {"value": {"note": "${{ a.note }}"}, "schema": {"type": "object", "required": ["note"], "properties": {"note": {"type": "string"}}}}}}`,
			nil, map[string]any{"a": map[string]any{}}, nil},
		{"catch-and-success-converge", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}},
		   "catch": "b", "next": ["b"]},
		  {"name": "b", "set": "${{ a.error.message }}"}]}`, nil, map[string]any{"a": map[string]any{"id": "x"}}, nil},
		{"additional-props-missing-key", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "additionalProperties": {"type": "number"}}, "next": ["s"]},
		  {"name": "s", "set": "${{ a.alice + 1 }}"}]}`, nil, map[string]any{"a": map[string]any{"bob": 1}}, nil},
		{"join-elem", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "a", "when": "s.l"}, {"step": "b"}]},
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["k"], "properties": {"k": {"type": "string"}}}, "next": ["read"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "additionalProperties": {"type": "number"}}, "next": ["read"]},
		  {"name": "read", "set": "${{ coalesce(a, b).z + 1 }}"}]}`, nil, map[string]any{"s": map[string]any{"l": true}, "a": map[string]any{"k": "s", "z": "str"}}, nil},
		{"list-eq", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["t", "u"], "properties": {"t": {"type": "array", "items": {"type": "string"}}, "u": {"type": "array", "items": {"type": "string"}}}},
		   "next": [{"step": "b", "when": "a.t == a.u"}]},
		  {"name": "b", "task": "x"}]}`, nil, map[string]any{"a": map[string]any{"t": []any{"x"}, "u": []any{"x"}}}, nil},
		{"round-nil", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "number"}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ round(a.n) + sum(map(a?.xs, 1)) }}"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"addl-complete", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "additionalProperties": {"type": "object", "properties": {"score": {"type": "number"}}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ if(a?.alice != nil, coalesce(a?.alice.score, 0), 0) }}"}]}`, nil, map[string]any{"a": map[string]any{"alice": map[string]any{}}}, nil},
		{"addl-complete-with-props", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "number"}},
		    "additionalProperties": {"type": "object", "properties": {"score": {"type": "number"}}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ if(a?.alice != nil, coalesce(a?.alice.score, 0), 0) }}"}]}`, nil, map[string]any{"a": map[string]any{"alice": map[string]any{}}}, nil},
		{"bare-object-may-not-have-run", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["go"], "properties": {"go": {"type": "boolean"}}},
		   "next": [{"step": "b", "when": "a.go"}, {"step": "c", "when": "!a.go"}]},
		  {"name": "b", "task": "x", "output": {"type": "object"}, "next": ["c"]},
		  {"name": "c", "set": "${{ b.k }}"}]}`, nil, map[string]any{"a": map[string]any{"go": false}}, nil},
		{"bare-object-optional-property", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"meta": {"type": "object"}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ a.meta.k }}"}]}`, nil, map[string]any{"a": map[string]any{}}, nil},
		{"multi-type-upper", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": ["string", "number"]}, "next": ["s"]},
		  {"name": "s", "set": "${{ upper(a) }}"}]}`, nil, map[string]any{"a": 1}, nil},
		{"multi-type-arith", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": ["string", "number"]}, "next": ["s"]},
		  {"name": "s", "set": "${{ a * 2 }}"}]}`, nil, map[string]any{"a": "x"}, nil},
		{"multi-type-object-or-list", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": ["object", "array"], "properties": {"k": {"type": "string"}}, "required": ["k"]}, "next": ["s"]},
		  {"name": "s", "set": "${{ a.k }}"}]}`, nil, map[string]any{"a": []any{}}, nil},
		{"mixed-enum", `{"steps": [
		  {"name": "a", "task": "x", "output": {"enum": ["x", 1]}, "next": ["s"]},
		  {"name": "s", "set": "${{ a * 2 }}"}]}`, nil, map[string]any{"a": "x"}, nil},
		{"switch-number-key", `{"steps": [
		  {"name": "a", "task": "x", "output": {"enum": [1, 1000000]}, "next": {"switch": "a", "cases": {"1": "l", "1e+06": "r"}}},
		  {"name": "l", "task": "x"}, {"name": "r", "task": "x"}]}`, nil, map[string]any{"a": 1000000}, nil},
		{"set-across-back-edge", `{"steps": [
		  {"name": "a", "task": "x", "max_visits": 3, "with": {"x": "${{ s?.v + 1 }}"}, "next": ["b"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["again"], "properties": {"again": {"type": "boolean"}}}, "next": ["s"]},
		  {"name": "s", "set": {"v": "str"}, "next": [{"step": "a", "when": "b.again"}]}]}`,
			nil, map[string]any{"b": map[string]any{"again": true}, "b#2": map[string]any{"again": false}}, nil},
		{"set-across-back-edge-coalesce", `{"steps": [
		  {"name": "a", "task": "x", "max_visits": 3, "with": {"x": "${{ coalesce(s?.v, 0) * 2 }}"}, "next": ["b"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["again"], "properties": {"again": {"type": "boolean"}}}, "next": ["s"]},
		  {"name": "s", "set": {"v": "str"}, "next": [{"step": "a", "when": "b.again"}]}]}`,
			nil, map[string]any{"b": map[string]any{"again": true}, "b#2": map[string]any{"again": false}}, nil},
		{"set-across-back-edge-chain", `{"steps": [
		  {"name": "a", "task": "x", "max_visits": 3, "with": {"x": "${{ coalesce(t?.v, 0) * 2 }}"}, "next": ["b"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["again"], "properties": {"again": {"type": "boolean"}}}, "next": ["s"]},
		  {"name": "s", "set": {"v": "str"}, "next": ["t"]},
		  {"name": "t", "set": {"v": "${{ s.v }}"}, "next": [{"step": "a", "when": "b.again"}]}]}`,
			nil, map[string]any{"b": map[string]any{"again": true}, "b#2": map[string]any{"again": false}}, nil},
		{"nullable-enum-required", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["p"], "properties": {"p": {"enum": ["a", null]}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ coalesce(a.p, \"z\") }}"}]}`, nil, map[string]any{"a": map[string]any{"p": nil}}, nil},
		{"big-integer", `{"steps": [
		  {"name": "a", "task": "x", "output": "integer", "next": ["s"]},
		  {"name": "s", "set": "${{ a + 1 }}"}]}`, nil, map[string]any{"a": 1e19}, nil},
		{"alias", `{"inputs": {"note": {"type": "string"}}, "steps": [
		  {"name": "b", "set": "${{ inputs.note }}", "next": ["s"]},
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "a", "when": "s.l"}, {"step": "c"}]},
		  {"name": "a", "set": "${{ inputs.note }}", "next": ["read"]},
		  {"name": "c", "set": "${{ inputs.note }}", "next": ["read"]},
		  {"name": "read", "set": "${{ coalesce(a, c) + \"!\" }}"}]}`, nil, map[string]any{"s": map[string]any{"l": true}}, nil},
	}
	for _, c := range cases {
		got := soundRun(t, c.doc, c.in, c.outs, c.fails)
		if !strings.HasPrefix(got, "VALIDATE") && !strings.HasPrefix(got, "completed") {
			t.Errorf("%s: validated but %s", c.name, got)
		}
	}
}

// TestHostSchemasKeepThePromise are counterexamples that need a host's
// params schema.
func TestHostSchemasKeepThePromise(t *testing.T) {
	kinds := schemaKinds{
		"task": {nil, nil},
		"num":  {mustSchema(t, `{"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}}`), nil},
	}
	for _, c := range []struct {
		name, doc string
		outs      map[string]any
	}{
		{"union-with-param", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"name": {"type": "string"}}}, "next": ["b"]},
		  {"name": "b", "num": "x", "with": {"n": "${{ a.name || 0 }}"}}]}`, map[string]any{"a": map[string]any{"name": "bob"}}},
		{"absent-with", `{"steps": [{"name": "b", "num": "x"}]}`, nil},
		{"absent-with-on-failure", `{"steps": [{"name": "a", "task": "x"}], "on_failure": [{"name": "f", "num": "x"}]}`, nil},
	} {
		fails := map[string]bool{"a": c.name == "absent-with-on-failure"}
		got := soundRunKinds(t, kinds, c.doc, nil, c.outs, fails)
		if fails["a"] {
			// The run fails by design; its on_failure step must not.
			if strings.Contains(got, "on_failure") {
				t.Errorf("%s: validated but %s", c.name, got)
			}
		} else if !strings.HasPrefix(got, "VALIDATE") && !strings.HasPrefix(got, "completed") {
			t.Errorf("%s: validated but %s", c.name, got)
		}
	}
}

// TestNoFalsePositives are definitions that are safe and must validate.
func TestNoFalsePositives(t *testing.T) {
	for name, doc := range map[string]string{
		"reader-only-a-catch-reaches": `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}},
		   "catch": "b", "next": ["c"]},
		  {"name": "b", "set": "${{ a.error.message }}"},
		  {"name": "c", "set": "${{ a.id }}"}]}`,
		"narrowing": `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"n": {"type": "number"}}},
		   "next": [{"step": "b", "when": "a.n != nil && a.n > 3"}]},
		  {"name": "b", "task": "x"}]}`,
		"or default": `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "properties": {"name": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": {"name": "${{ a.name || \"anon\" }}"}, "next": [{"step": "r", "when": "s.name == \"anon\""}]},
		  {"name": "r", "task": "x"}]}`,
		"loop reads its own last pass": `{"steps": [
		  {"name": "draft", "task": "x", "max_visits": 3, "with": {"prev": "${{ draft?.text }}"},
		   "output": {"type": "object", "required": ["text"], "properties": {"text": {"type": "string"}}}, "next": ["review"]},
		  {"name": "review", "task": "x", "output": {"type": "object", "required": ["ok"], "properties": {"ok": {"type": "boolean"}}},
		   "match": "first", "next": [{"step": "draft", "when": "!review.ok"}, {"step": "publish"}]},
		  {"name": "publish", "task": "x", "with": {"text": "${{ draft.text }}"}}]}`,
	} {
		if got := problemsFor(t, doc, testKinds); got != "" {
			t.Errorf("%s: %s", name, got)
		}
	}
}

// TestJoinAbsentFields: a value that is one of two object shapes may lack
// a field the other has, so a plain read of it must be rejected at Load
// (expr v1.3.1 requires ?.). The each and catch cases run with and without
// the failure that makes the error shape appear.
func TestJoinAbsentFields(t *testing.T) {
	for _, c := range []struct {
		name, doc string
		outs      map[string]any
		fails     map[string]bool
	}{
		{"list-literal", `{"steps": [
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["y"], "properties": {"y": {"type": "string"}}}, "next": ["s"]},
		  {"name": "s", "set": "${{ coalesce([a, {\"x\": \"q\"}][0].x, \"none\") }}"}]}`, map[string]any{"a": map[string]any{"y": "v"}}, nil},
		{"coalesce-alternatives", `{"steps": [
		  {"name": "s", "task": "x", "match": "first", "next": [{"step": "a", "when": "s.l"}, {"step": "b"}]},
		  {"name": "a", "task": "x", "output": {"type": "object", "required": ["y"], "properties": {"y": {"type": "string"}}}, "next": ["read"]},
		  {"name": "b", "task": "x", "output": {"type": "object", "required": ["x"], "properties": {"x": {"type": "string"}}}, "next": ["read"]},
		  {"name": "read", "set": "${{ coalesce(coalesce(a, b).x, \"none\") }}"}]}`,
			map[string]any{"s": map[string]any{"l": true}, "a": map[string]any{"y": "v"}}, nil},
		{"each-continue-filter", joinEachDoc(`filter(work, it.error == nil)`), nil, joinFails},
		{"each-continue-map", joinEachDoc(`map(work, coalesce(it.v, 0))`), nil, joinFails},
		{"catch-converge-error", joinCatchDoc(`a.error == nil`), nil, joinFails},
		{"catch-converge-id", joinCatchDoc(`coalesce(a.id, \"none\")`), nil, joinFails},
	} {
		for _, fails := range []map[string]bool{nil, c.fails} {
			got := soundRun(t, c.doc, nil, c.outs, fails)
			if !strings.HasPrefix(got, "VALIDATE") && !strings.HasPrefix(got, "completed") {
				t.Errorf("%s (fails %v): validated but %s", c.name, fails, got)
			}
		}
	}
}

// TestJoinAbsentFieldsSafeReads: the ?. forms of the cases above validate
// and run, whichever side of the join each value came from.
func TestJoinAbsentFieldsSafeReads(t *testing.T) {
	for name, doc := range map[string]string{
		"each-continue-filter": joinEachDoc(`filter(work, it?.error == nil)`),
		"each-continue-map":    joinEachDoc(`map(work, coalesce(it?.v, 0))`),
		"catch-converge-error": joinCatchDoc(`a?.error == nil`),
		"catch-converge-id":    joinCatchDoc(`coalesce(a?.id, \"none\")`),
	} {
		for _, fails := range []map[string]bool{nil, joinFails} {
			if got := soundRun(t, doc, nil, nil, fails); !strings.HasPrefix(got, "completed") {
				t.Errorf("%s (fails %v): %s", name, fails, got)
			}
		}
	}
}

// joinFails fails the first item of work and the first attempt of a, so
// the error shape reaches the reading step.
var joinFails = map[string]bool{"work": true, "a": true}

func joinEachDoc(expr string) string {
	return `{"steps": [
	  {"name": "work", "task": "x", "each": "[1, 2]", "continue_on_error": true,
	   "output": {"type": "object", "required": ["v"], "properties": {"v": {"type": "number"}}}, "next": ["s"]},
	  {"name": "s", "set": "${{ ` + expr + ` }}"}]}`
}

func joinCatchDoc(expr string) string {
	return `{"steps": [
	  {"name": "a", "task": "x", "output": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}},
	   "catch": "b", "next": ["b"]},
	  {"name": "b", "set": "${{ ` + expr + ` }}"}]}`
}

// TestOutsideThePromise pins what the promise in definitions.md does not
// cover: each validates and can still fail at run time, as documented.
func TestOutsideThePromise(t *testing.T) {
	kinds := schemaKinds{
		"task":  {nil, nil},
		"set_n": {mustSchema(t, `{"type": "object", "properties": {"n": {"type": "integer"}, "c": {"enum": ["red", "blue"]}}}`), nil},
	}
	list := `{"type": "object", "required": ["items", "n", "s"], "properties": {"items": {"type": "array", "items": {"type": "string"}},
	  "n": {"type": "number"}, "s": {"type": "string"}}}`
	for _, c := range []struct {
		name, doc string
		outs      map[string]any
		want      string
	}{
		{"index out of range", `{"steps": [{"name": "a", "task": "x", "output": ` + list + `, "next": ["s"]},
		  {"name": "s", "set": "${{ a.items[0] }}"}]}`, map[string]any{"a": map[string]any{"items": []any{}, "n": 1, "s": ""}}, "failed expression"},
		{"value error", `{"steps": [{"name": "a", "task": "x", "output": ` + list + `, "next": ["s"]},
		  {"name": "s", "set": "${{ int(a.s) }}"}]}`, map[string]any{"a": map[string]any{"items": []any{}, "n": 1, "s": "abc"}}, "failed expression"},
		{"number to integer param", `{"steps": [{"name": "a", "task": "x", "output": ` + list + `, "next": ["b"]},
		  {"name": "b", "set_n": "x", "with": {"n": "${{ a.n }}"}}]}`, map[string]any{"a": map[string]any{"items": []any{}, "n": 1.5, "s": ""}}, "failed invalid_params"},
		{"string to enum param", `{"steps": [{"name": "a", "task": "x", "output": ` + list + `, "next": ["b"]},
		  {"name": "b", "set_n": "x", "with": {"c": "${{ a.s }}"}}]}`, map[string]any{"a": map[string]any{"items": []any{}, "n": 1, "s": "green"}}, "failed invalid_params"},
		{"untyped step", `{"steps": [{"name": "a", "task": "x", "next": ["s"]}, {"name": "s", "set": "${{ a.k + 1 }}"}]}`,
			map[string]any{"a": map[string]any{}}, "failed expression"},
		{"empty schema is untyped", `{"steps": [{"name": "a", "task": "x", "output": {}, "next": ["s"]}, {"name": "s", "set": "${{ a.k }}"}]}`,
			map[string]any{"a": map[string]any{}}, "failed expression"},
		{"fields of a bare object", `{"steps": [{"name": "a", "task": "x", "output": {"type": "object"}, "next": ["s"]}, {"name": "s", "set": "${{ a.k }}"}]}`,
			map[string]any{"a": map[string]any{}}, "failed expression"},
		{"inputs without an inputs block", `{"steps": [{"name": "s", "set": "${{ inputs.foo }}"}]}`, nil, "failed expression"},
	} {
		got := soundRunKinds(t, kinds, c.doc, nil, c.outs, nil)
		require.True(t, strings.HasPrefix(got, c.want), c.name+": "+got)
	}
}
