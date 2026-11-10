package workflow

import (
	"encoding/json"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// layoutDoc exercises every step list and kind, and each way to write an
// edge: a name, a {step, when}, a switch and a catch.
const layoutDoc = `{
  "name": "layout",
  "steps": [
    {"name": "fetch", "action": "fetch", "model": "m1", "next": "route",
      "catch": "recover"},
    {"name": "route", "set": "${{ fetch.kind }}", "next": {"switch": "route",
      "cases": {"a": "tally", "b": ["tally", "nap"]}, "default": "nap"}},
    {"name": "tally", "expr": "len(fetch.items)",
      "next": [{"step": "nap", "when": "tally > 0"}]},
    {"name": "nap", "sleep": "1s", "next": ["hold"]},
    {"name": "hold", "wait": true, "timeout": "1h", "next": ["done"]},
    {"name": "recover", "agent": "fixer", "next": ["done"]},
    {"name": "done", "join": []}
  ],
  "on_failure": [{"name": "alert", "action": "page"}],
  "finally": [{"name": "cleanup", "agent": "janitor"}]
}`

// withLayout adds a layout to every step of doc, with bend points for
// each edge the loaded definition has.
func withLayout(t *testing.T, doc string) string {
	t.Helper()
	def := mustLoad(t, doc)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(doc), &m))
	i := 0
	for _, list := range []string{"steps", "on_failure", "finally"} {
		for _, raw := range m[list].([]any) {
			sm := raw.(map[string]any)
			edges := map[string]any{}
			for _, target := range def.Step(sm["name"].(string)).successors() {
				edges[target] = []any{map[string]any{"x": i * 10, "y": 5.5}}
			}
			sm["layout"] = map[string]any{"x": i * 100, "y": -40.25, "edges": edges}
			i++
		}
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return string(data)
}

func TestLayoutParsesOnEveryStep(t *testing.T) {
	doc := withLayout(t, layoutDoc)
	def := mustLoad(t, doc)

	all := append(append(append([]*Step{}, def.Steps...), def.OnFailure...), def.Finally...)
	for i, s := range all {
		require.NotNil(t, s.Layout, s.Name)
		require.Equal(t, float64(i*100), s.Layout.X, s.Name)
		require.Equal(t, -40.25, s.Layout.Y, s.Name)
		distinct := map[string]bool{}
		for _, target := range s.successors() {
			distinct[target] = true
		}
		require.Equal(t, len(distinct), len(s.Layout.Edges), s.Name)
		for _, target := range s.successors() {
			require.Equal(t, []Point{{X: float64(i * 10), Y: 5.5}}, s.Layout.Edges[target])
		}
		if s.Config != nil {
			_, has := s.Config["layout"]
			require.False(t, has, "%s: layout leaked into Config", s.Name)
		}
	}
	require.Equal(t, map[string]any{"action": "fetch", "model": "m1"}, def.Step("fetch").Config)
	require.Equal(t, 2, len(def.Step("route").Layout.Edges))
	require.Equal(t, 2, len(def.Step("fetch").Layout.Edges))

	// The document round-trips with its layout.
	out, err := json.Marshal(def)
	require.NoError(t, err)
	again, err := Load(out, testKinds)
	require.NoError(t, err)
	require.Equal(t, def.Hash(), again.Hash())
	require.Equal(t, def.Step("route").Layout, again.Step("route").Layout)
}

func TestLayoutEdgesAreOptional(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "a", "action": "x", "layout": {"x": 1, "y": 2}}]}`)
	require.Equal(t, &Layout{X: 1, Y: 2}, def.Step("a").Layout)

	def = mustLoad(t, `{"steps": [{"name": "a", "action": "x", "next": "b",
	  "layout": {"x": 0, "y": 0, "edges": {"b": []}}}, {"name": "b", "action": "x"}]}`)
	require.Equal(t, []Point{}, def.Step("a").Layout.Edges["b"])
}

func TestLayoutProblems(t *testing.T) {
	for _, tc := range []struct{ layout, want string }{
		{`7`, "a: layout must be an object"},
		{`{"y": 1}`, "a: layout needs x"},
		{`{"x": "1", "y": 1}`, "a: layout.x must be a number"},
		{`{"x": 1, "y": 1, "w": 3}`, `a: layout: unknown key "w"`},
		{`{"x": 1, "y": 1, "edges": []}`, "a: layout.edges must be an object"},
		{`{"x": 1, "y": 1, "edges": {"b": {"x": 1}}}`,
			"a: layout.edges.b must be a list of points {x, y}"},
		{`{"x": 1, "y": 1, "edges": {"b": [3]}}`, "a: layout.edges.b[0] must be an object"},
		{`{"x": 1, "y": 1, "edges": {"b": [{"x": 1}]}}`, "a: layout.edges.b[0] needs y"},
		{`{"x": 1, "y": 1, "edges": {"b": [{"x": 1, "y": true}]}}`,
			"a: layout.edges.b[0].y must be a number"},
		{`{"x": 1, "y": 1, "edges": {"b": [{"x": 1, "y": 2, "z": 3}]}}`,
			`a: layout.edges.b[0]: unknown key "z"`},
		{`{"x": 1, "y": 1, "edges": {"c": []}}`,
			`a: layout.edges: "c" is not a next or catch target of the step`},
		{`{"x": 1, "y": 1, "edges": {"a": []}}`,
			`a: layout.edges: "a" is not a next or catch target of the step`},
	} {
		doc := `{"steps": [{"name": "a", "action": "x", "next": "b", "layout": ` + tc.layout +
			`}, {"name": "b", "action": "x"}, {"name": "c", "action": "x"}]}`
		require.Contains(t, problemsFor(t, doc, testKinds), tc.want, tc.layout)
	}

	// A step with no successors, such as a finally step, has no edges.
	got := problemsFor(t, `{"steps": [{"name": "a", "action": "x"}],
	  "finally": [{"name": "f", "action": "x", "layout": {"x": 1, "y": 1, "edges": {"a": []}}}]}`,
		testKinds)
	require.Contains(t, got, `f: layout.edges: "a" is not a next or catch target of the step`)
}

// A layout changes nothing about a run: the same events dispatch the same
// commands and reach the same state, apart from the definition hash.
func TestLayoutDoesNotChangeRuns(t *testing.T) {
	handle := func(fail bool) func(Command) result {
		return func(c Command) result {
			switch c.Step {
			case "fetch":
				if fail {
					return result{err: &StepError{Type: "boom", Message: "no"}}
				}
				return ok(map[string]any{"kind": "b", "items": []any{1, 2}})
			case "recover":
				return fail2(c)
			}
			return ok(map[string]any{"step": c.Step})
		}
	}
	for _, fail := range []bool{false, true} {
		plain := newHost(t, mustLoad(t, layoutDoc), handle(fail))
		laid := newHost(t, mustLoad(t, withLayout(t, layoutDoc)), handle(fail))
		require.NotEqual(t, plain.def.Hash(), laid.def.Hash())
		for _, h := range []*fakeHost{plain, laid} {
			h.start(map[string]any{})
			h.runToEnd(nil)
		}

		require.Equal(t, mustJSON(t, plain.all), mustJSON(t, laid.all))
		a, b := *plain.st, *laid.st
		a.DefinitionHash, b.DefinitionHash = "", ""
		require.Equal(t, string(stateBytes(t, &a)), string(stateBytes(t, &b)))
		require.Equal(t, plain.dispatched(), laid.dispatched())
	}
}

// fail2 makes recover fail too, so the failing run reaches on_failure.
func fail2(Command) result { return fail("still", "broken", false) }
