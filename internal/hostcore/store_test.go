package hostcore

import (
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

var kinds = workflow.KindMap{"agent": nil, "action": nil}

func load(t *testing.T, doc string) *workflow.Definition {
	t.Helper()
	def, err := workflow.Load([]byte(doc), kinds)
	require.NoError(t, err)
	return def
}

func TestHasNUL(t *testing.T) {
	for in, want := range map[string]bool{
		`"a\u0000b"`:     true,
		`"a\\u0000b"`:    false, // an escaped backslash followed by text
		`"a\\\u0000b"`:   true,
		`{"\u0000": 1}`:  true,
		`"\u00001"`:      true,
		`"plain"`:        false,
		`"a\"\u0000"`:    true,
		`"\u0001\u000a"`: false,
	} {
		require.Equal(t, want, hasNUL([]byte(in)), in)
	}
}

// TestOutcomeWithoutAUnit covers an event whose unit has left the state in
// the same Apply: the record still settles, from the event.
func TestOutcomeWithoutAUnit(t *testing.T) {
	def := load(t, `{"steps": [{"name": "nap", "sleep": "1s", "next": ["w"]}, {"name": "w", "agent": "x", "timeout": "1m"}]}`)
	gone := &workflow.State{}
	for _, c := range []struct {
		ev      workflow.Event
		status  durable.StepStatus
		errType string
	}{
		{workflow.Event{
			Kind:   workflow.EventStepCompleted,
			StepID: "main/w@2#1",
			Output: "x",
		},
			durable.StepCompleted, ""},
		{workflow.Event{
			Kind:   workflow.EventStepFailed,
			StepID: "main/w#1",
			Error:  &workflow.StepError{Type: "boom"},
		}, durable.StepFailed, "boom"},
		{workflow.Event{
			Kind:   workflow.EventTimerFired,
			StepID: "main.x/nap@3#1",
		}, durable.StepCompleted, ""},
		{workflow.Event{Kind: workflow.EventTimerFired, StepID: "main/w[2]#4"},
			durable.StepFailed, workflow.ErrorTypeTimeout},
	} {
		status, _, stepErr := AttemptOutcome(def, gone, c.ev)
		require.Equal(t, c.status, status, c.ev.StepID)
		if c.errType == "" {
			require.Nil(t, stepErr)
		} else {
			require.Equal(t, c.errType, stepErr.Type)
		}
	}
}
