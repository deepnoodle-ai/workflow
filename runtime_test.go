package workflow

import (
	"strings"
	"time"
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

func TestSetValueObeysMaxOutputBytes(t *testing.T) {
	def := mustLoad(t, `{"inputs": {"s": {"type": "string"}}, "steps": [{"name": "big", "set": "${{ inputs.s }}"}]}`)
	big := map[string]any{"s": strings.Repeat("x", 200)}
	def.Limits = Limits{MaxOutputBytes: 100}
	h := newHost(t, def, nil)
	h.start(big, nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeOutputTooLarge, h.final.Error.Type)
	require.Contains(t, h.final.Error.Message, "limit is 100")

	def.Limits = Limits{MaxOutputBytes: 1000}
	h = newHost(t, def, nil)
	h.start(big, nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestEventTimeNeverRunsBackwards(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "a", "task": "x", "timeout": "1m", "next": ["b"]},
	  {"name": "b", "sleep": "10m"}]}`)
	st, _, err := Apply(def, nil, Event{At: t0, Kind: EventStart})
	require.NoError(t, err)
	early := t0.Add(-time.Hour)
	st, cmds, err := Apply(def, st, Event{At: early, Kind: EventStepCompleted, StepID: "main/a#1", Output: 1})
	require.NoError(t, err)
	a := st.Nodes["main/a"]
	require.Equal(t, t0, a.SettledAt)
	require.False(t, a.SettledAt.Before(a.EnteredAt))
	require.Equal(t, t0, st.Nodes["main/b"].EnteredAt)
	require.Equal(t, CommandScheduleTimer, cmds[0].Kind)
	require.Equal(t, t0.Add(10*time.Minute), cmds[0].NotBefore, "the sleep is not scheduled early")

	st, _, err = Apply(def, st, Event{At: early, Kind: EventTimerFired, StepID: "main/b#1"})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, st.Status)
	require.False(t, st.FinishedAt.Before(st.StartedAt))
	require.Equal(t, t0, st.UpdatedAt)
}

// A failed each item cancels its siblings in flight, so a catch proceeds
// at once, and the siblings' late results, in any order and with any
// outcome, change nothing.
func TestEachFailureCancelsSiblingsAndIgnoresLateResults(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "work", "task": "x", "each": "[0, 1, 2]", "timeout": "1m",
	   "catch": "handle", "next": ["done"]},
	  {"name": "handle", "task": "x"},
	  {"name": "done", "task": "x"}]}`)
	st, cmds, err := Apply(def, nil, Event{At: t0, Kind: EventStart})
	require.NoError(t, err)
	require.Equal(t, []string{"main/work[0]#1", "main/work[1]#1", "main/work[2]#1"}, idsOf(cmds, CommandDispatchStep))

	st, cmds, err = Apply(def, st, Event{At: t0, Kind: EventStepFailed, StepID: "main/work[0]#1",
		Error: &StepError{Type: "boom", Message: "item 0 broke"}})
	require.NoError(t, err)
	require.Equal(t, []string{"main/work[1]#1", "main/work[2]#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"main/handle#1"}, idsOf(cmds, CommandDispatchStep), "the catch proceeds at once")
	want := mustJSON(t, st)

	late := []Event{
		{At: t0, Kind: EventStepCompleted, StepID: "main/work[1]#1", Output: 1},
		{At: t0, Kind: EventStepFailed, StepID: "main/work[2]#1", Error: &StepError{Type: "other", Message: "x"}},
		{At: t0, Kind: EventTimerFired, StepID: "main/work[1]#1"},
	}
	for _, order := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}} {
		cur := st
		for _, i := range order {
			next, cmds, err := Apply(def, cur, late[i])
			require.NoError(t, err)
			require.Empty(t, cmds)
			require.True(t, next == cur, "a late result is a no-op")
			cur = next
		}
		require.Equal(t, want, mustJSON(t, cur))
	}
}
