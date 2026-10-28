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
