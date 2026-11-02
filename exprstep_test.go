package workflow

import (
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestExprStepOutputsItsValue(t *testing.T) {
	def := mustLoad(t, `{
	  "input": {"type": "object", "properties": {"items": {"type": "array", "items": {"type": "number"}}}},
	  "steps": [
	    {"name": "big", "expr": "input.items | filter(it > 1) | map(it * 10)", "next": ["total"]},
	    {"name": "total", "expr": "len(big)"}],
	  "outputs": {"big": "${{ big }}", "total": "${{ total }}"}}`)
	h := newHost(t, def, nil)
	h.start(map[string]any{"items": []any{1, 2, 3}})
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{"big": []any{20.0, 30.0}, "total": 2.0}, h.final.Outputs)

	// The type is inferred at load: total is a number, not a string.
	require.Contains(t, problemsFor(t, `{"steps": [
	  {"name": "n", "expr": "len([1, 2])", "next": ["s"]},
	  {"name": "s", "expr": "upper(n)"}]}`, testKinds), "s: expr: ")
}

// expr ends an expression at a line break, so a YAML | block needs the
// pipe at the end of each line; >- folds the lines and takes either style.
func TestExprStepMultiLine(t *testing.T) {
	build := func(src string) map[string]any {
		return map[string]any{
			"steps": []any{
				map[string]any{"name": "a", "action": "x", "next": []any{"b"}},
				map[string]any{"name": "b", "expr": src},
			},
			"outputs": map[string]any{"b": "${{ b }}"},
		}
	}
	pipeLast := "a.items |\n  filter(it > 1) |\n  map(it * 10)\n"
	folded := "a.items | filter(it > 1) | map(it * 10)"
	for _, src := range []string{pipeLast, folded} {
		def, err := LoadMap(build(src), testKinds)
		require.NoError(t, err, src)
		h := newHost(t, def, func(Command) result {
			return ok(map[string]any{"items": []any{1, 2, 3}})
		})
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, map[string]any{"b": []any{20.0, 30.0}}, h.final.Outputs, src)
	}

	_, err := LoadMap(build("a.items\n| map(it)\n"), testKinds)
	require.Error(t, err)
	require.Contains(t, err.Error(), "b: expr: ")
}

func TestExprStepLoadErrors(t *testing.T) {
	cases := []struct{ step, want string }{
		{`{"name": "a", "expr": {"n": 1}}`,
			"a: expr must be an expression string; write a literal, map or list with set"},
		{`{"name": "a", "expr": " "}`,
			"a: expr must be an expression string; write a literal, map or list with set"},
		{`{"name": "a", "expr": 3}`,
			"a: expr must be an expression string; write a literal, map or list with set"},
		{`{"name": "a", "expr": " ${{ input.n }} "}`,
			"a: expr takes a bare expression; remove the ${{ }}, or use set for a template"},
		{`{"name": "a", "expr": "primary"}`,
			`a: expr reads unknown name "primary"; to output the string "primary", use set`},
		{`{"name": "a", "expr": "primary + 1"}`,
			`a: expr reads unknown name "primary"`},
		{`{"name": "a", "expr": "1 +"}`, "a: expr: "},
		{`{"name": "a", "expr": "1", "with": {"n": 1}}`, "a: with is not allowed on an expr step"},
	}
	for _, c := range cases {
		got := problemsFor(t, `{"steps": [`+c.step+`]}`, testKinds)
		require.Contains(t, got, c.want, c.step)
		require.Equal(t, 1, strings.Count(got, "\n")+1, c.step)
		if strings.HasSuffix(c.want, `"primary"`) {
			require.NotContains(t, got, "use set", c.step)
		}
	}

	// A ${{ inside a string literal is just text.
	mustLoad(t, `{"steps": [{"name": "a", "expr": "\"write ${{ x }}\""}]}`)
}

// expr steps run in on_failure and finally like set steps, and are typed
// to a fixpoint around a loop.
func TestExprStepInPhasesAndLoops(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "a", "action": "x"}],
	  "on_failure": [{"name": "why", "expr": "upper(error.type)"}],
	  "finally": [
	    {"name": "how", "expr": "run.status"},
	    {"name": "report", "action": "x", "with": {"why": "${{ why }}", "how": "${{ how }}"}}]}`)
	var report map[string]any
	h := newHost(t, def, func(c Command) result {
		if c.Params != nil {
			report = c.Params
			return ok(nil)
		}
		return fail("boom", "no", false)
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, map[string]any{"why": "BOOM", "how": "failed"}, report)

	require.Equal(t, "", problemsFor(t, `{"steps": [
	  {"name": "a", "action": "x", "max_visits": 3, "with": {"n": "${{ coalesce(s, 0) + 1 }}"}, "next": ["s"]},
	  {"name": "s", "expr": "coalesce(s, 0) + 1", "next": [{"step": "a", "when": "s < 3"}]}]}`,
		testKinds))
}
