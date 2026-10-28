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
	def, err := Parse([]byte(doc))
	require.NoError(t, err)
	err = def.Validate(kinds)
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
	    {"name": "badexpr", "agent": "a", "each": "((", "with": {"v": "${later.x}"}},
	    {"name": "later", "agent": "a", "with": {"q": "${nobody}"}},
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

func TestValidateGraph(t *testing.T) {
	problems := strings.Join(validationProblems(t, `{
	  "steps": [
	    {"name": "start", "task": "x", "next": ["a", "b"]},
	    {"name": "a", "task": "x", "next": ["j"], "with": {"v": "${b}", "w": "${item}"}},
	    {"name": "b", "task": "x", "next": ["j"]},
	    {"name": "j", "join": ["a", "loop"], "next": ["loop"]},
	    {"name": "island", "task": "x", "next": ["j"]},
	    {"name": "loop", "task": "x", "next": [{"step": "loop2", "when": "loop.again"}]},
	    {"name": "loop2", "task": "x", "next": ["loop"], "with": {"x": "${nope}"}}
	  ],
	  "outputs": {"r": "${zzz}"}
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
	  {"name": "list", "task": "x", "next": ["fan"]},
	  {"name": "fan", "task": "x", "each": "list", "with": {"i": "${item}", "n": "${index}"}, "next": ["poll"]},
	  {"name": "poll", "task": "x", "max_visits": 10, "match": "first",
	   "next": [{"step": "wait", "when": "!poll.done"}, {"step": "done"}]},
	  {"name": "wait", "sleep": "30s", "next": ["poll"]},
	  {"name": "done", "set": {"count": "${len(fan)}", "last": "${poll}"}}
	]}`)
}

func TestValidateOnFailureRules(t *testing.T) {
	problems := strings.Join(validationProblems(t, `{
	  "steps": [{"name": "a", "task": "x"}],
	  "on_failure": [
	    {"task": "x", "next": ["a"]},
	    {"name": "j", "join": []},
	    {"task": "x", "with": {"m": "${error.message} ${a} ${mystery}"}}
	  ]
	}`, testKinds), "\n")
	require.Contains(t, problems, "on_failure_1: on_failure steps run in order")
	require.Contains(t, problems, "j: on_failure steps cannot be join steps")
	require.Contains(t, problems, `on_failure_3: with reads unknown name "mystery"`)
	require.NotContains(t, problems, `"a"`)
}

func TestParseErrors(t *testing.T) {
	_, err := Parse([]byte(`{"steps": [{"name": "a", "task": "x", "timeout": "soon", "retry": {"tries": 3}}], "bogus": 1}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown key "bogus"`)
	require.Contains(t, err.Error(), "timeout")
	require.Contains(t, err.Error(), `retry: unknown key "tries"`)

	_, err = Parse([]byte(`not json`))
	require.Error(t, err)
}

func TestParseMapMatchesParse(t *testing.T) {
	// A host that authors in YAML decodes it and calls ParseMap; ints from
	// the YAML decoder are normalized the same way JSON numbers are.
	doc := map[string]any{
		"name": "y",
		"steps": []any{
			map[string]any{"name": "a", "task": "x", "concurrency": 2, "each": "[1]", "with": map[string]any{"n": 1}},
		},
	}
	def, err := ParseMap(doc)
	require.NoError(t, err)
	require.NoError(t, def.Validate(testKinds))
	require.Equal(t, 2, def.Steps[0].Concurrency)

	same, err := Parse([]byte(`{"name":"y","steps":[{"name":"a","task":"x","concurrency":2,"each":"[1]","with":{"n":1}}]}`))
	require.NoError(t, err)
	require.Equal(t, def.Hash(), same.Hash())
}
