package workflow

import (
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestStepIDParts(t *testing.T) {
	cases := []struct {
		id         StepID
		unit, step string
		attempt    int
	}{
		{"main/charge#2", "main/charge", "charge", 2},
		{"main/verify[3]#1", "main/verify[3]", "verify", 1},
		{"main.tests/lint@2#1", "main.tests/lint@2", "lint", 1},
		{"main/poll@4[0]#3", "main/poll@4[0]", "poll", 3},
		{"failure/page#1", "failure/page", "page", 1},
		{"finally/release#1", "finally/release", "release", 1},
		{"main/charge", "", "", 0},
		{"", "", "", 0},
	}
	for _, c := range cases {
		require.Equal(t, c.unit, c.id.Unit(), string(c.id))
		require.Equal(t, c.step, c.id.Step(), string(c.id))
		require.Equal(t, c.attempt, c.id.Attempt(), string(c.id))
	}
}

// A retry is a new attempt at the same unit: the StepID changes and the
// unit does not.
func TestRetryKeepsTheUnit(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "charge", "action": "x", "retry": {"max_attempts": 2}}]}`)
	attempts := 0
	h := newHost(t, def, func(c Command) result {
		attempts++
		if attempts == 1 {
			return fail("timeout", "no answer", true)
		}
		return ok(nil)
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	ids := h.dispatched()
	require.Equal(t, []StepID{"main/charge#1", "main/charge#2"}, ids)
	require.Equal(t, ids[0].Unit(), ids[1].Unit())
}

func TestAbort(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler())
	h.start(nil)
	next, cmds := Abort(h.st, &RunError{Type: "definition_unavailable", Message: "gone"},
		t0.Add(time.Minute))
	require.Equal(t, StatusFailed, next.Status)
	require.Equal(t, "definition_unavailable", next.Error.Type)
	require.Equal(t, []CommandKind{CommandCancelStep, CommandRunFailed},
		[]CommandKind{cmds[0].Kind, cmds[1].Kind})
	require.Equal(t, StepID("main/lock#1"), cmds[0].StepID)
	require.Empty(t, next.InFlight())
	require.Equal(t, StatusRunning, h.st.Status, "Abort works on a copy")

	// The aborted state is final for Apply too.
	same, more, err := Apply(def, next, Event{At: t0, Kind: EventStepCompleted, StepID: "main/lock#1"})
	require.NoError(t, err)
	require.Empty(t, more)
	require.True(t, same == next)
	again, none := Abort(next, &RunError{Type: "x"}, t0)
	require.True(t, again == next)
	require.Empty(t, none)
}
