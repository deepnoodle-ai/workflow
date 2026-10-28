package workflow

import (
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestRetryShorthandMustBeInteger(t *testing.T) {
	problems := validationProblems(t, `{"steps": [{"name": "a", "task": "x", "retry": 2.5}]}`, testKinds)
	require.Equal(t, []string{"a: retry must be an integer"}, problems)

	def := mustLoad(t, `{"steps": [{"name": "a", "task": "x", "retry": 3}]}`)
	require.Equal(t, 3, def.Steps[0].Retry.MaxAttempts)
}

func TestTemplatePaddingMakesAString(t *testing.T) {
	cases := []struct {
		src  string
		want any
	}{
		{"${{ 1 + 2 }}", float64(3)},
		{"  ${{ 1 + 2 }}  ", "  3  "},
		{"${{ 1 + 2 }}\n", "3\n"},
		{`  ${{ "a" }}  `, "  a  "},
		{"${{ 1 }}${{ 2 }}", "12"},
		{"$${{ x }}", "${{ x }}"},
	}
	for _, c := range cases {
		tm, err := compileTemplate(c.src)
		require.NoError(t, err, c.src)
		v, err := tm.eval(map[string]any{})
		require.NoError(t, err, c.src)
		v, _, _ = normalize(v)
		require.Equal(t, c.want, v, c.src)
	}

	// The type checker agrees: a padded template is a string.
	problems := validationProblems(t, `{"steps": [
	  {"name": "a", "set": {"n": "  ${{ 1 + 2 }}  "}, "next": ["b"]},
	  {"name": "b", "set": "${{ a.n + 1 }}"}]}`, testKinds)
	require.Equal(t, 1, len(problems), problems)
	require.Contains(t, problems[0], "string")
}
