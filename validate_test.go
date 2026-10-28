package workflow

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func validationProblems(t *testing.T, doc string, kinds Kinds) []string {
	t.Helper()
	_, err := Load([]byte(doc), kinds)
	require.Error(t, err)
	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	out := make([]string, len(ve.Problems))
	for i, p := range ve.Problems {
		out[i] = p.Step + ": " + p.Message
	}
	return out
}

func TestValidateReportsEveryProblem(t *testing.T) {
	problems := validationProblems(t, `{
	  "steps": [
	    {"name": "start", "agent": "a", "next": ["ghost", "b"]},
	    {"name": "b", "agent": "a", "action": "x", "next": ["c"]},
	    {"name": "c", "join": ["island"], "with": {"x": 1}},
	    {"name": "island", "agent": "a"},
	    {"name": "loop1", "agent": "a", "next": ["loop2"]},
	    {"name": "loop2", "agent": "a", "next": ["loop1"]},
	    {"name": "badexpr", "agent": "a", "each": "((", "with": {"v": "${{ later.x }}"}},
	    {"name": "later", "agent": "a", "with": {"q": "${{ nobody }}"}},
	    {"name": "agnet", "agnet": "typo"},
	    {"name": "len", "agent": "a"},
	    {"name": "start", "agent": "a"},
	    {"name": "bad-name", "agent": "a"},
	    {"name": "cfg", "agent": "a", "model": 7}
	  ]
	}`, KindMap{
		"agent": func(cfg map[string]any) error {
			if m, ok := cfg["model"]; ok {
				if _, isStr := m.(string); !isStr {
					return fmt.Errorf("model must be a string")
				}
			}
			return nil
		},
		"action": nil,
	})
	joined := strings.Join(problems, "\n")
	for _, want := range []string{
		`start: next: unknown step "ghost"`,
		"b: more than one step kind: action, agent",
		"c: with is not allowed on a join step",
		`agnet: no step kind`,
		"len: name is reserved",
		"start: duplicate step name",
		"bad-name: name must be an identifier",
		"cfg: agent config: model must be a string",
		"badexpr: each:",
	} {
		require.Contains(t, joined, want)
	}
	require.True(t, len(problems) >= 9, joined)
}

// input is the one global a run is given; inputs and event are ordinary
// step names.
func TestReservedNames(t *testing.T) {
	require.Contains(t, problemsFor(t, `{"steps": [{"name": "input", "action": "x"}]}`, testKinds),
		"input: name is reserved")
	mustLoad(t, `{"steps": [{"name": "inputs", "action": "x", "next": ["event"]},
	  {"name": "event", "set": "${{ inputs }}"}]}`)
}

func TestValidateGraph(t *testing.T) {
	problems := strings.Join(validationProblems(t, `{
	  "steps": [
	    {"name": "start", "action": "x", "next": ["a", "b"]},
	    {"name": "a", "action": "x", "next": ["j"], "with": {"v": "${{ b }}", "w": "${{ item }}"}},
	    {"name": "b", "action": "x", "next": ["j"]},
	    {"name": "j", "join": ["a", "loop"], "next": ["loop"]},
	    {"name": "island", "action": "x", "next": ["j"]},
	    {"name": "loop", "action": "x", "next": [{"step": "loop2", "when": "loop.again"}]},
	    {"name": "loop2", "action": "x", "next": ["loop"], "with": {"x": "${{ nope }}"}}
	  ],
	  "outputs": {"r": "${{ zzz }}"}
	}`, testKinds), "\n")
	for _, want := range []string{
		"island: unreachable from the start step start",
		"j: join: step loop is not upstream",
		"loop: cycle loop -> loop2 has no bound",
		"a: with reads b, which cannot have settled",
		`a: with reads unknown name "item"`,
		`loop2: with reads unknown name "nope"`,
		`: outputs: unknown name "zzz"`,
	} {
		require.Contains(t, problems, want)
	}
}

func TestValidateAcceptsBoundedCycleAndEachVars(t *testing.T) {
	mustLoad(t, `{"steps": [
	  {"name": "list", "action": "x", "next": ["fan"]},
	  {"name": "fan", "action": "x", "each": "list", "with": {"i": "${{ item }}", "n": "${{ index }}"}, "next": ["poll"]},
	  {"name": "poll", "action": "x", "max_visits": 10, "match": "first",
	   "next": [{"step": "wait", "when": "!poll.done"}, {"step": "done"}]},
	  {"name": "wait", "sleep": "30s", "next": ["poll"]},
	  {"name": "done", "set": {"count": "${{ len(fan) }}", "last": "${{ poll }}"}}
	]}`)
}

func TestValidateOnFailureRules(t *testing.T) {
	problems := strings.Join(validationProblems(t, `{
	  "steps": [{"name": "a", "action": "x"}],
	  "on_failure": [
	    {"action": "x", "next": ["a"]},
	    {"name": "j", "join": []},
	    {"action": "x", "with": {"m": "${{ error.message }} ${{ a }} ${{ mystery }}"}}
	  ]
	}`, testKinds), "\n")
	require.Contains(t, problems, "on_failure_1: on_failure steps run in order")
	require.Contains(t, problems, "j: on_failure steps cannot be join steps")
	require.Contains(t, problems, `on_failure_3: with reads unknown name "mystery"`)
	require.NotContains(t, problems, `"a"`)
}

func TestParseErrors(t *testing.T) {
	_, err := Load([]byte(`{"steps": [{"name": "a", "action": "x", "timeout": "soon", "retry": {"tries": 3}}], "bogus": 1}`), testKinds)
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown key "bogus"`)
	require.Contains(t, err.Error(), "timeout")
	require.Contains(t, err.Error(), `retry: unknown key "tries"`)

	_, err = Load([]byte(`not json`), testKinds)
	require.Error(t, err)
}

func TestLoadMapMatchesLoad(t *testing.T) {
	// A host that authors in YAML decodes it and calls LoadMap; ints from
	// the YAML decoder are normalized the same way JSON numbers are.
	doc := map[string]any{
		"name": "y",
		"steps": []any{
			map[string]any{"name": "a", "action": "x", "concurrency": 2, "each": "[1]", "with": map[string]any{"n": 1}},
		},
	}
	def, err := LoadMap(doc, testKinds)
	require.NoError(t, err)
	require.Equal(t, 2, def.Steps[0].Concurrency)

	same, err := Load([]byte(`{"name":"y","steps":[{"name":"a","action":"x","concurrency":2,"each":"[1]","with":{"n":1}}]}`), testKinds)
	require.NoError(t, err)
	require.Equal(t, def.Hash(), same.Hash())
}

// TestTemplateSyntax pins the template delimiters: ${{ ... }} evaluates,
// shell and JavaScript ${...} is text, and $${{ writes a literal ${{.
func TestTemplateSyntax(t *testing.T) {
	def := mustLoad(t, `{"input": {"type": "object", "properties": {"n": {"type": "integer", "default": 2}}},
	  "steps": [{"name": "a", "action": "x", "with": {
	    "n": "${{ input.n }}",
	    "text": "n is ${{ input.n + 1 }}",
	    "shell": "echo ${HOME} $((1 + 2))",
	    "js": "`+"`"+`hi ${name}`+"`"+`",
	    "escaped": "$${{ input.n }}"}}]}`)
	h := newHost(t, def, func(c Command) result { return ok(nil) })
	h.start(nil)
	params := h.all[0].Params
	require.Equal(t, map[string]any{
		"n": float64(2), "text": "n is 3", "shell": "echo ${HOME} $((1 + 2))",
		"js": "`hi ${name}`", "escaped": "${{ input.n }}",
	}, params)
}

// A step without a name, alone or among named steps, is a validation
// problem, not a panic in graph analysis.
func TestValidateNamelessStep(t *testing.T) {
	for _, doc := range []string{
		`{"steps":[{"action":"x"}]}`,
		`{"steps":[{"name":"a","action":"x","next":"b"},{"action":"x"},{"name":"b","action":"x"}]}`,
		`{"steps":[{"name":"a","action":"x","next":"b"},{"name":"b","action":"x"},{"action":"x","next":"a"}]}`,
	} {
		problems := strings.Join(validationProblems(t, doc, testKinds), "\n")
		require.Contains(t, problems, "every step needs a name", doc)
	}
}

// Duplicate names make the graph ambiguous; analysis is skipped instead of
// reporting problems about whichever step won.
func TestValidateDuplicateNameSkipsGraph(t *testing.T) {
	problems := validationProblems(t, `{"steps":[
	  {"name":"a","action":"x","next":"b"},
	  {"name":"b","action":"x"},
	  {"name":"b","action":"x","next":"a"}
	]}`, testKinds)
	require.Equal(t, []string{"b: duplicate step name"}, problems)
}

func TestValidateTargets(t *testing.T) {
	problems := strings.Join(validationProblems(t, `{"steps":[
	  {"name":"a","action":"x","next":[{"when":"true"}, "b", "b"],"catch":[{"errors":["x"]}]},
	  {"name":"b","action":"x","next":{"switch":"a.kind","cases":{"bug":["fix","fix"],"other":"","more":["fix"]},"default":["fix",""]}},
	  {"name":"fix","action":"x"}
	]}`, testKinds), "\n")
	for _, want := range []string{
		"a: next: an edge is missing its step",
		`a: next: step "b" appears twice`,
		"a: catch: a route is missing its next step",
		`b: next: case "bug": step "fix" appears twice`,
		`b: next: case "other" is missing a step name`,
		"b: next: default is missing a step name",
	} {
		require.Contains(t, problems, want)
	}
	require.NotContains(t, problems, `unknown step ""`)
	// The same step in different cases is fine.
	require.NotContains(t, problems, `case "more"`)
}
