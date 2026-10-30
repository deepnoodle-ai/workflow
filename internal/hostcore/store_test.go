package hostcore

import (
	"testing"
	"time"

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

// TestOutcomeOfADecisionTimerOnASleep: a decision on a sleep step's edges
// has a timer of its own. When it fires for a decision that has left the
// state, the record fails as a timeout; it is not the sleep completing.
func TestOutcomeOfADecisionTimerOnASleep(t *testing.T) {
	def, err := workflow.Load([]byte(`{"steps": [
	  {"name": "nap", "sleep": "1s", "match": "first", "next": [
	    {"step": "w", "when": "decide(\"Go on?\", input) > 0.5"}, "w2"]},
	  {"name": "w", "agent": "x"}, {"name": "w2", "agent": "x"}]}`),
		workflow.KindMap{"agent": nil, workflow.KindDecision: nil})
	require.NoError(t, err)

	// Take the decision's StepID from the engine, so this test follows the
	// StepID grammar.
	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{Kind: workflow.EventStart, At: at})
	require.NoError(t, err)
	_, cmds, err = workflow.Apply(def, st, workflow.Event{
		Kind: workflow.EventTimerFired, StepID: cmds[0].StepID, At: at.Add(time.Second),
	})
	require.NoError(t, err)
	var id workflow.StepID
	for _, c := range cmds {
		if c.Kind == workflow.CommandDispatchStep && c.StepKind == workflow.KindDecision {
			id = c.StepID
		}
	}
	require.NotEqual(t, workflow.StepID(""), id)

	status, _, stepErr := AttemptOutcome(def, &workflow.State{},
		workflow.Event{Kind: workflow.EventTimerFired, StepID: id})
	require.Equal(t, durable.StepFailed, status)
	require.Equal(t, workflow.ErrorTypeTimeout, stepErr.Type)
}
