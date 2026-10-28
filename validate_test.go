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
	ve, ok := errors.AsType[*ValidationError](err)
	require.True(t, ok)
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

	// A duplicate key would otherwise drop all but its last value, and
	// invalid UTF-8 would be replaced.
	_, err = Load([]byte(`{"steps": [{"name": "a", "action": "x", "next": "b", "next": "c"}]}`), testKinds)
	require.Error(t, err)
	require.Contains(t, err.Error(), `duplicate object member name "next" within "/steps/0"`)
	_, err = Load([]byte("{\"steps\": [{\"name\": \"a\", \"action\": \"x\", \"description\": \"\xff\"}]}"), testKinds)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid UTF-8")
	_, err = ParseSchema([]byte(`{"type": "string", "type": "number"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `duplicate object member name "type"`)
}

func TestLoadMapMatchesLoad(t *testing.T) {
	// A host that authors in YAML decodes it and calls LoadMap; ints from
	// the YAML decoder are normalized the same way JSON numbers are.
	doc := map[string]any{
		"name": "y",
		"steps": []any{
			map[string]any{
				"name":        "a",
				"action":      "x",
				"concurrency": 2,
				"each":        "[1]",
				"with":        map[string]any{"n": 1},
			},
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

// Each mistake is reported once, with where it is.
func TestSchemaProblemsSayWhere(t *testing.T) {
	cases := []struct{ doc, want string }{
		{`{"input": {"type": "object", "properties": {"a": "ghost"}}, "steps": [{"name": "a", "action": "x"}]}`,
			`input: unknown schema "ghost"`},
		{`{"steps": [{"name": "a", "action": "x"}], "outputs": {"o": {"value": "${{ a }}", "schema": "ghost"}}}`,
			`outputs.o.schema: unknown schema "ghost"`},
		{`{"steps": [{"name": "a", "action": "x"}],
		  "outputs": {"o": {"value": "${{ a }}", "schema": {"type": "string", "default": 3}}}}`,
			`outputs.o.schema`},
		{`{"steps": [{"name": "a", "action": "x", "output": false}]}`,
			"the schema false accepts no value"},
	}
	for _, c := range cases {
		_, err := Load([]byte(c.doc), testKinds)
		require.Error(t, err, c.doc)
		require.Contains(t, err.Error(), c.want, c.doc)
	}
	_, err := Load([]byte(`{"steps": [{"name": "a", "action": "x", "output": true}]}`), testKinds)
	require.NoError(t, err, "true is the schema that accepts anything")
}

func TestCleanupReadErrorsAreReportedOnce(t *testing.T) {
	_, err := Load([]byte(`{"steps": [{"name": "a", "action": "x"}],
	  "on_failure": [{"name": "o", "set": "${{ ghost.x + 1 }}"}],
	  "finally": [{"name": "f", "set": "${{ spook.y }}"}]}`), testKinds)
	require.Error(t, err)
	ve, ok := errors.AsType[*ValidationError](err)
	require.True(t, ok)
	require.Equal(t, 2, len(ve.Problems), err.Error())
}

// An unconditional edge after a conditional one reads as an else but is
// always followed, so Load asks which one is meant.
func TestElseLookalikeNeedsMatch(t *testing.T) {
	const doc = `{"steps": [
	  {"name": "a", "action": "x", MATCH"next": [{"step": "b", "when": "a.ok"}, "c"]},
	  {"name": "b", "action": "x"}, {"name": "c", "action": "x"}]}`
	require.Contains(t, problemsFor(t, strings.Replace(doc, "MATCH", "", 1), testKinds),
		"a: next: the edge to c has no when and follows the conditional edge to b, so it is always followed")
	for _, match := range []string{`"match": "first", `, `"match": "all", `} {
		require.Equal(t, "", problemsFor(t, strings.Replace(doc, "MATCH", match, 1), testKinds), match)
	}
	// Unconditional edges first read as what they are: always, and also b.
	require.Equal(t, "", problemsFor(t, `{"steps": [
	  {"name": "a", "action": "x", "next": ["c", {"step": "b", "when": "a.ok"}]},
	  {"name": "b", "action": "x"}, {"name": "c", "action": "x"}]}`, testKinds))
}

// Load rejects a join that names a step a path can skip, and accepts one
// whose named steps every arriving path settles, even inside a branch
// that is itself conditional.
func TestJoinNamesStepsThatSettle(t *testing.T) {
	ok := []string{
		// A fork and a join.
		`{"steps": [{"name": "s", "action": "x", "next": ["a", "b"]},
		  {"name": "a", "action": "x", "next": ["j"]}, {"name": "b", "action": "x", "next": ["j"]},
		  {"name": "j", "join": ["a", "b"]}]}`,
		// The fork is on a branch taken only when s.go holds; when the join
		// fires, that branch was taken.
		`{"steps": [{"name": "s", "action": "x", "next": [{"step": "y", "when": "s.go"}]},
		  {"name": "y", "action": "x", "next": ["a", "b"]},
		  {"name": "a", "action": "x", "next": ["j"]}, {"name": "b", "action": "x", "next": ["j"]},
		  {"name": "j", "join": ["a", "b"]}]}`,
		// Both branches of an if/else pass through b.
		`{"steps": [{"name": "s", "action": "x", "match": "first", "next": [{"step": "b", "when": "s.go"}, "c"]},
		  {"name": "c", "action": "x", "next": ["b"]}, {"name": "b", "action": "x", "next": ["j"]},
		  {"name": "j", "join": ["b"]}]}`,
		// A caught error routes through b too.
		`{"steps": [{"name": "s", "action": "x", "catch": [{"next": "b"}], "next": ["b"]},
		  {"name": "b", "action": "x", "next": ["j"]}, {"name": "j", "join": ["b", "s"]}]}`,
	}
	for _, doc := range ok {
		require.Equal(t, "", problemsFor(t, doc, testKinds), doc)
	}
	bad := map[string]string{
		// An if/else: the join fires with only one of them.
		`{"steps": [{"name": "s", "action": "x", "match": "first", "next": [{"step": "a", "when": "s.go"}, "b"]},
		  {"name": "a", "action": "x", "next": ["j"]}, {"name": "b", "action": "x", "next": ["j"]},
		  {"name": "j", "join": ["a"]}]}`: "join: a may not have settled",
		// A caught error skips a.
		`{"steps": [{"name": "s", "action": "x", "catch": [{"next": "j"}], "next": ["a"]},
		  {"name": "a", "action": "x", "next": ["j"]}, {"name": "j", "join": ["a"]}]}`: "join: a may not have settled",
		// p's only edge is conditional: when p.go is false the branch ends
		// and the join fires with q alone.
		`{"steps": [{"name": "s", "action": "x", "next": ["p", "q"]},
		  {"name": "p", "action": "x", "next": [{"step": "a", "when": "p.go"}]},
		  {"name": "a", "action": "x", "next": ["j"]}, {"name": "q", "action": "x", "next": ["j"]},
		  {"name": "j", "join": ["a"]}]}`: "join: a may not have settled",
		// The same with match: first and no default edge.
		`{"steps": [{"name": "s", "action": "x", "next": ["p", "q"]},
		  {"name": "p", "action": "x", "match": "first", "next": [{"step": "a", "when": "p.go"}]},
		  {"name": "a", "action": "x", "next": ["j"]}, {"name": "q", "action": "x", "next": ["j"]},
		  {"name": "j", "join": ["a"]}]}`: "join: a may not have settled",
	}
	for doc, want := range bad {
		require.Contains(t, problemsFor(t, doc, testKinds), want, doc)
	}
}
