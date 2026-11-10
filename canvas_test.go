package workflow

import (
	"encoding/json"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// canvasJSON groups every step of layoutDoc, including on_failure and
// finally steps, with one nested group, and has a free note and notes
// attached to a step and a group.
const canvasJSON = `{
  "groups": [
    {"name": "intake", "description": "Fetch and route **the** work.",
      "steps": ["fetch", "route"],
      "layout": {"x": -20, "y": 0, "width": 400, "height": 180.5, "collapsed": true}},
    {"name": "work", "steps": ["tally", "nap", "hold", "recover", "done"]},
    {"name": "waiting", "parent": "work", "steps": [],
      "layout": {"x": 0, "y": 300, "width": 200, "height": 100}},
    {"name": "wrap_up", "steps": ["alert", "cleanup"]}
  ],
  "notes": [
    {"text": "# Why\nRouting is by kind.", "x": 10, "y": -40},
    {"text": "retries here", "x": 0, "y": 60, "width": 120, "height": 40, "attach": "fetch"},
    {"text": "runs last", "x": 5, "y": 5, "attach": "wrap_up"}
  ]
}`

// withCanvas adds canvasJSON to doc.
func withCanvas(t *testing.T, doc string) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(doc), &m))
	var c any
	require.NoError(t, json.Unmarshal([]byte(canvasJSON), &c))
	m["canvas"] = c
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return string(data)
}

func TestCanvasParses(t *testing.T) {
	def := mustLoad(t, withCanvas(t, withLayout(t, layoutDoc)))

	c := def.Canvas
	require.NotNil(t, c)
	require.Equal(t, 4, len(c.Groups))
	require.Equal(t, &Group{
		Name:        "intake",
		Description: "Fetch and route **the** work.",
		Steps:       []string{"fetch", "route"},
		Layout:      &GroupLayout{X: -20, Y: 0, Width: 400, Height: 180.5, Collapsed: true},
	}, c.Groups[0])
	require.Equal(t, &Group{Name: "work", Steps: []string{"tally", "nap", "hold", "recover", "done"}},
		c.Groups[1])
	require.Equal(t, "work", c.Groups[2].Parent)
	require.Equal(t, []string{}, c.Groups[2].Steps)
	require.False(t, c.Groups[2].Layout.Collapsed)
	require.Equal(t, []*Note{
		{Text: "# Why\nRouting is by kind.", X: 10, Y: -40},
		{Text: "retries here", Y: 60, Width: 120, Height: 40, Attach: "fetch"},
		{Text: "runs last", X: 5, Y: 5, Attach: "wrap_up"},
	}, c.Notes)
	require.Equal(t, map[string]any{"action": "fetch", "model": "m1"}, def.Step("fetch").Config)

	// The document round-trips with its canvas, and the canvas is part of
	// the hash.
	out, err := json.Marshal(def)
	require.NoError(t, err)
	again, err := Load(out, testKinds)
	require.NoError(t, err)
	require.Equal(t, def.Hash(), again.Hash())
	require.Equal(t, def.Canvas, again.Canvas)
	require.NotEqual(t, mustLoad(t, withLayout(t, layoutDoc)).Hash(), def.Hash())

	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	var want any
	require.NoError(t, json.Unmarshal([]byte(canvasJSON), &want))
	require.Equal(t, want, m["canvas"])
}

func TestCanvasPartsAreOptional(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "a", "action": "x"}], "canvas": {}}`)
	require.Equal(t, &Canvas{}, def.Canvas)
	require.Nil(t, mustLoad(t, `{"steps": [{"name": "a", "action": "x"}]}`).Canvas)

	def = mustLoad(t, `{"steps": [{"name": "a", "action": "x"}],
	  "canvas": {"groups": [{"name": "g"}], "notes": [{"text": "hi", "x": 0, "y": 0}]}}`)
	require.Equal(t, &Group{Name: "g"}, def.Canvas.Groups[0])
	require.Equal(t, &Note{Text: "hi"}, def.Canvas.Notes[0])
}

func TestCanvasProblems(t *testing.T) {
	for _, tc := range []struct{ canvas, want string }{
		{`7`, "canvas must be an object"},
		{`{"boxes": []}`, `canvas: unknown key "boxes" (it has groups and notes)`},
		{`{"groups": {}}`, "canvas.groups must be a list"},
		{`{"notes": [3]}`, "canvas.notes[0] must be an object"},

		// Groups.
		{`{"groups": [{"steps": ["a"]}]}`, "canvas.groups[0] needs a name"},
		{`{"groups": [{"name": ""}]}`, "canvas.groups[0] needs a name"},
		{`{"groups": [{"name": 3}]}`, "canvas.groups[0].name must be a string"},
		{`{"groups": [{"name": "g-1"}]}`,
			`canvas.groups[0]: name "g-1" must be an identifier (letters, digits, underscore)`},
		{`{"groups": [{"name": "a"}]}`, `canvas.groups[0]: name "a" is a step's name`},
		{`{"groups": [{"name": "g"}, {"name": "g"}]}`,
			`canvas.groups[1]: duplicate group name "g"`},
		{`{"groups": [{"name": "g", "color": "red"}]}`, `canvas.groups[0]: unknown key "color"`},
		{`{"groups": [{"name": "g", "description": 1}]}`,
			"canvas.groups[0].description must be a string"},
		{`{"groups": [{"name": "g", "steps": "a"}]}`,
			"canvas.groups[0].steps must be a list of strings"},
		{`{"groups": [{"name": "g", "steps": ["zz"]}]}`, `canvas: group "g": unknown step "zz"`},
		{`{"groups": [{"name": "g", "steps": ["a", "a"]}]}`,
			`canvas: group "g": step "a" appears twice`},
		{`{"groups": [{"name": "g", "steps": ["a"]}, {"name": "h", "steps": ["a"]}]}`,
			`canvas: group "h": step "a" is already in group "g"`},
		{`{"groups": [{"name": "g", "steps": ["f"]}, {"name": "h", "steps": ["f"]}]}`,
			`canvas: group "h": step "f" is already in group "g"`},
		{`{"groups": [{"name": "g", "parent": "zz"}]}`,
			`canvas: group "g": parent: unknown group "zz"`},
		{`{"groups": [{"name": "g", "parent": "a"}]}`,
			`canvas: group "g": parent: unknown group "a"`},
		{`{"groups": [{"name": "g", "parent": "g"}]}`,
			`canvas: group "g": parent: a group cannot be its own parent`},
		{`{"groups": [{"name": "g"}, {"name": "h", "parent": "g"}, {"name": "i", "parent": "h"}]}`,
			`canvas: group "i": parent: group "h" is inside "g" itself; groups nest one level deep`},

		// A group's layout.
		{`{"groups": [{"name": "g", "layout": []}]}`, "canvas.groups[0].layout must be an object"},
		{`{"groups": [{"name": "g", "layout": {"x": 0, "y": 0, "width": 1}}]}`,
			"canvas.groups[0].layout needs height"},
		{`{"groups": [{"name": "g", "layout": {"x": "0", "y": 0, "width": 1, "height": 1}}]}`,
			"canvas.groups[0].layout.x must be a number"},
		{`{"groups": [{"name": "g", "layout": {"x": 0, "y": 0, "width": 0, "height": 1}}]}`,
			"canvas.groups[0].layout.width must be greater than 0"},
		{`{"groups": [{"name": "g", "layout": {"x": 0, "y": 0, "width": 1, "height": -2}}]}`,
			"canvas.groups[0].layout.height must be greater than 0"},
		{`{"groups": [{"name": "g", "layout": {"x": 0, "y": 0, "width": 1, "height": 1,
		  "collapsed": "yes"}}]}`, "canvas.groups[0].layout.collapsed must be a boolean"},
		{`{"groups": [{"name": "g", "layout": {"x": 0, "y": 0, "width": 1, "height": 1,
		  "z": 1}}]}`, `canvas.groups[0].layout: unknown key "z"`},

		// Notes.
		{`{"notes": [{"x": 0, "y": 0}]}`, "canvas.notes[0] needs text"},
		{`{"notes": [{"text": "", "x": 0, "y": 0}]}`, "canvas.notes[0] needs text"},
		{`{"notes": [{"text": 1, "x": 0, "y": 0}]}`, "canvas.notes[0].text must be a string"},
		{`{"notes": [{"text": "t", "y": 0}]}`, "canvas.notes[0] needs x"},
		{`{"notes": [{"text": "t", "x": 0, "y": null}]}`, "canvas.notes[0].y must be a number"},
		{`{"notes": [{"text": "t", "x": 0, "y": 0, "width": 0}]}`,
			"canvas.notes[0].width must be greater than 0"},
		{`{"notes": [{"text": "t", "x": 0, "y": 0, "height": "big"}]}`,
			"canvas.notes[0].height must be a number"},
		{`{"notes": [{"text": "t", "x": 0, "y": 0, "color": "y"}]}`,
			`canvas.notes[0]: unknown key "color"`},
		{`{"notes": [{"text": "t", "x": 0, "y": 0, "attach": 2}]}`,
			"canvas.notes[0].attach must be a string"},
		{`{"notes": [{"text": "t", "x": 0, "y": 0, "attach": "zz"}]}`,
			`canvas.notes[0]: attach: no step or group named "zz"`},
	} {
		doc := `{"steps": [{"name": "a", "action": "x", "next": "b"}, {"name": "b", "action": "x"}],
		  "finally": [{"name": "f", "action": "x"}], "canvas": ` + tc.canvas + `}`
		require.Contains(t, problemsFor(t, doc, testKinds), tc.want, tc.canvas)
	}
}

// An unnamed group reports only its missing name, not its steps as repeats.
func TestCanvasUnnamedGroupReportsOnlyItsName(t *testing.T) {
	doc := `{"steps": [{"name": "a", "action": "x"}], "canvas": {"groups": [{"steps": ["a"]}]}}`
	got := problemsFor(t, doc, testKinds)
	require.Contains(t, got, "canvas.groups[0] needs a name")
	require.NotContains(t, got, "appears twice")
}

// A canvas changes nothing about a run: the same events dispatch the same
// commands and reach the same state, apart from the definition hash.
func TestCanvasDoesNotChangeRuns(t *testing.T) {
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
		drawn := newHost(t, mustLoad(t, withCanvas(t, layoutDoc)), handle(fail))
		require.NotEqual(t, plain.def.Hash(), drawn.def.Hash())
		for _, h := range []*fakeHost{plain, drawn} {
			h.start(map[string]any{})
			h.runToEnd(nil)
		}

		require.Equal(t, mustJSON(t, plain.all), mustJSON(t, drawn.all))
		a, b := *plain.st, *drawn.st
		a.DefinitionHash, b.DefinitionHash = "", ""
		require.Equal(t, string(stateBytes(t, &a)), string(stateBytes(t, &b)))
		require.Equal(t, plain.dispatched(), drawn.dispatched())
	}
}
