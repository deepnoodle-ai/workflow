package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// approvalDoc is an approval gate: the wait and the request start
// together, so the wait is in place before anyone is asked.
const approvalDoc = `{"steps": [
  {"name": "build", "action": "x", "next": ["approve", "notify"]},
  {"name": "notify", "action": "x"},
  {"name": "approve", "wait": true, "timeout": "72h",
   "output": {"type": "object", "required": ["ok"], "properties": {"ok": {"type": "boolean"}}},
   "catch": [{"errors": ["timeout"], "next": "expired"}],
   "next": {"switch": "approve.ok", "cases": {"true": "deploy"}, "default": "rejected"}},
  {"name": "deploy", "action": "x"},
  {"name": "rejected", "set": "rejected"},
  {"name": "expired", "set": "expired"}]}`

// startApproval runs the approval gate up to its wait.
func startApproval(t *testing.T) *fakeHost {
	t.Helper()
	h := newHost(t, mustLoad(t, approvalDoc), func(Command) result { return ok(nil) })
	h.start(nil)
	cmds := h.deliver("main/build#1")
	require.Equal(t, []string{"main.notify/notify#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, []string{"main/approve#1"}, idsOf(cmds, CommandScheduleTimer))
	for _, c := range cmds {
		if c.Kind == CommandScheduleTimer {
			require.Equal(t, KindWait, c.StepKind)
			require.Equal(t, h.now.Add(72*time.Hour), c.NotBefore)
		}
	}
	h.deliver("main.notify/notify#1")
	return h
}

// deliverByName resolves a step name as a host's Deliver does, then
// applies the event.
func (h *fakeHost) deliverByName(ev Event) ([]Command, error) {
	h.t.Helper()
	id, err := Resolve(h.def, h.st, ev.StepID)
	if err != nil {
		return nil, err
	}
	ev.StepID = id
	delete(h.pending, id)
	delete(h.timers, id)
	return h.apply(ev), nil
}

func TestWaitApproved(t *testing.T) {
	h := startApproval(t)
	cmds, err := h.deliverByName(Event{
		Kind:   EventStepCompleted,
		StepID: "approve",
		Output: map[string]any{"ok": true},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"main/deploy#1"}, idsOf(cmds, CommandDispatchStep))
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)

	// A second delivery by name is a duplicate: no error, no change.
	before := stateBytes(t, h.st)
	id, err := Resolve(h.def, h.st, "approve")
	require.NoError(t, err)
	next, cmds, err := Apply(h.def, h.st, Event{At: h.now, Kind: EventStepCompleted, StepID: id})
	require.NoError(t, err)
	require.Empty(t, cmds)
	require.Equal(t, before, stateBytes(t, next))
}

func TestWaitSecondDeliveryWhileRunning(t *testing.T) {
	// The run is still going (deploy runs) when the webhook retries.
	h := startApproval(t)
	_, err := h.deliverByName(Event{
		Kind:   EventStepCompleted,
		StepID: "approve",
		Output: map[string]any{"ok": true},
	})
	require.NoError(t, err)
	id, err := Resolve(h.def, h.st, "approve")
	require.NoError(t, err)
	require.Equal(t, StepID("main/approve#1"), id)
	next, cmds, err := Apply(h.def, h.st, Event{
		At:     h.now,
		Kind:   EventStepCompleted,
		StepID: id,
		Output: map[string]any{"ok": false},
	})
	require.NoError(t, err)
	require.Empty(t, cmds)
	require.True(t, next == h.st, "a duplicate returns the state unchanged")
}

func TestWaitRejected(t *testing.T) {
	h := startApproval(t)
	_, err := h.deliverByName(Event{
		Kind:   EventStepCompleted,
		StepID: "main/approve#1", // a full StepID still works
		Output: map[string]any{"ok": false},
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, "rejected", h.st.nodes["main/rejected"].Output)
}

func TestWaitTimesOut(t *testing.T) {
	h := startApproval(t)
	cmds := h.fire()
	require.Equal(t, []string{"main/approve#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, "expired", h.st.nodes["main/expired"].Output)
	require.Equal(t, t0.Add(72*time.Hour), h.st.UpdatedAt)

	// A late answer is not taken, and the sender is told.
	_, err := Resolve(h.def, h.st, "approve")
	require.ErrorIs(t, err, ErrNotWaiting)
	require.Contains(t, err.Error(), "no longer waiting")
}

func TestWaitLateAnswerWhileRunning(t *testing.T) {
	// The timeout is caught and the run goes on, so the late answer
	// reaches Resolve on a running run.
	def := mustLoad(t, `{"steps": [
	  {"name": "approve", "wait": true, "timeout": "1h",
	   "catch": [{"errors": ["timeout"], "next": "escalate"}]},
	  {"name": "escalate", "action": "x"}]}`)
	h := newHost(t, def, func(Command) result { return ok(nil) })
	h.start(nil)
	h.fire()
	require.Equal(t, StatusRunning, h.st.Status)
	_, err := Resolve(def, h.st, "approve")
	require.ErrorIs(t, err, ErrNotWaiting)
	require.Contains(t, err.Error(), "step approve is no longer waiting")
}

func TestWaitOutputIsChecked(t *testing.T) {
	h := startApproval(t)
	_, err := h.deliverByName(Event{
		Kind:   EventStepCompleted,
		StepID: "approve",
		Output: map[string]any{"ok": "yes"},
	})
	require.NoError(t, err)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeInvalidOutput, h.st.Error.Type)
}

func TestWaitFailedDeliveryIsCaught(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "approve", "wait": true, "timeout": "1h",
	   "catch": [{"errors": ["denied"], "next": "denied"}], "next": ["deploy"]},
	  {"name": "deploy", "action": "x"},
	  {"name": "denied", "set": "${{ approve.error.message }}"}]}`)
	h := newHost(t, def, func(Command) result { return ok(nil) })
	h.start(nil)
	_, err := h.deliverByName(Event{
		Kind:   EventStepFailed,
		StepID: "approve",
		Error:  &StepError{Type: "denied", Message: "not this week"},
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, "not this week", h.st.nodes["main/denied"].Output)
}

func TestWaitRetryWaitsAgain(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "approve", "wait": true, "timeout": "1h",
	   "retry": {"max_attempts": 2, "backoff": "2m"}}]}`)
	h := newHost(t, def, func(Command) result { return ok(nil) })
	h.start(nil)
	cmds := h.fire()
	require.Empty(t, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, []string{"main/approve#2"}, idsOf(cmds, CommandScheduleTimer))
	// The retry's timeout counts from the end of its backoff.
	deadline := t0.Add(time.Hour + 2*time.Minute + time.Hour)
	require.Equal(t, deadline, h.timers["main/approve#2"].NotBefore)

	// A delivery during the backoff settles the new attempt.
	id, err := Resolve(def, h.st, "approve")
	require.NoError(t, err)
	require.Equal(t, StepID("main/approve#2"), id)
	_, err = h.deliverByName(Event{Kind: EventStepCompleted, StepID: "approve", Output: "ok"})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestResolveErrors(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "start", "action": "x", "next": ["a", "b"]},
	  {"name": "a", "action": "x", "next": ["approve"]},
	  {"name": "b", "action": "x", "next": ["approve"]},
	  {"name": "approve", "wait": true, "timeout": "1h"}]}`)
	h := newHost(t, def, func(Command) result { return ok(nil) })
	h.start(nil)

	// Not reached yet.
	_, err := Resolve(def, h.st, "approve")
	require.ErrorIs(t, err, ErrNotWaiting)
	require.Contains(t, err.Error(), "approve")

	// Not a step.
	_, err = Resolve(def, h.st, "aprove")
	require.ErrorIs(t, err, ErrUnknownStep)

	// A StepID, even a wrong one, passes through for Apply to judge.
	id, err := Resolve(def, h.st, "main/nope#1")
	require.NoError(t, err)
	require.Equal(t, StepID("main/nope#1"), id)
}

func TestResolveAmbiguous(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "start", "action": "x", "next": ["a", "b"]},
	  {"name": "a", "action": "x", "next": ["approve"]},
	  {"name": "b", "action": "x", "next": ["approve"]},
	  {"name": "approve", "wait": true, "timeout": "1h"}]}`)
	h := newHost(t, def, func(Command) result { return ok(nil) })
	h.start(nil)
	h.deliver("main/start#1")
	h.deliver("main/a#1")
	h.deliver("main.b/b#1")
	_, err := Resolve(def, h.st, "approve")
	require.ErrorIs(t, err, ErrInvalidEvent)
	require.Contains(t, err.Error(), "main.b/approve#1, main/approve#1")
}

func TestOutstandingWait(t *testing.T) {
	h := startApproval(t)
	cmds, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, 1, len(cmds))
	require.Equal(t, CommandScheduleTimer, cmds[0].Kind)
	require.Equal(t, StepID("main/approve#1"), cmds[0].StepID)
	require.Equal(t, t0.Add(72*time.Hour), cmds[0].NotBefore)
}

func TestWaitLoadChecks(t *testing.T) {
	for name, tc := range map[string]struct{ step, want string }{
		"no timeout": {
			`"wait": true`,
			"s: a wait step needs a timeout",
		},
		"not true": {
			`"wait": "yes", "timeout": "1h"`,
			"s: wait must be true",
		},
		"false": {
			`"wait": false, "timeout": "1h"`,
			"s: wait must be true",
		},
		"with": {
			`"wait": true, "timeout": "1h", "with": {"a": 1}`,
			"s: with is not allowed on a wait step",
		},
		"each": {
			`"wait": true, "timeout": "1h", "each": "[1]"`,
			"s: each is not allowed on a wait step",
		},
		"concurrency": {
			`"wait": true, "timeout": "1h", "concurrency": 2`,
			"s: concurrency and continue_on_error need each",
		},
		"unknown key": {
			`"wait": true, "timeout": "1h", "model": "m"`,
			"s: unknown keys for a wait step: model",
		},
		"two kinds": {
			`"wait": true, "timeout": "1h", "sleep": "1m"`,
			"s: more than one step kind",
		},
		"in finally": {
			`"action": "x"}], "finally": [{"name": "f", "wait": true, "timeout": "1h"`,
			"f: finally steps cannot be wait steps",
		},
		"in on_failure": {
			`"action": "x"}], "on_failure": [{"name": "f", "wait": true, "timeout": "1h"`,
			"f: on_failure steps cannot be wait steps",
		},
		"loop bound": {
			`"wait": true, "timeout": "1h", "next": ["s"]`,
			"s: cycle s has no bound",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := problemsFor(t, `{"steps": [{"name": "s", `+tc.step+`}]}`, testKinds)
			require.Contains(t, got, tc.want)
		})
	}
}

func TestWaitIsTypedByItsOutput(t *testing.T) {
	doc := `{"steps": [
	  {"name": "approve", "wait": true, "timeout": "1h",
	   "output": {"type": "object", "required": ["ok"], "properties": {"ok": {"type": "boolean"}}},
	   "next": [{"step": "deploy", "when": "approve.ok"}]},
	  {"name": "deploy", "action": "x"}]}`
	require.Equal(t, "", problemsFor(t, doc, testKinds))
	got := problemsFor(t, strings.Replace(doc, `"approve.ok"`, `"approve.okay"`, 1), testKinds)
	require.Contains(t, got, `no field "okay"`)
	asString := strings.Replace(doc, `"type": "boolean"`, `"type": "string"`, 1)
	got = problemsFor(t, asString, testKinds)
	require.Contains(t, got, "when must be a boolean, got string")
}

// output is allowed on a wait, which a delivery fills, and still not on a
// sleep, whose value is always nil.
func TestOutputOnWaitAndSleep(t *testing.T) {
	wait := `{"steps": [{"name": "s", "wait": true, "timeout": "1h", "output": "string"}]}`
	require.Equal(t, "", problemsFor(t, wait, testKinds))
	sleep := `{"steps": [{"name": "s", "sleep": "1h", "output": "string"}]}`
	got := problemsFor(t, sleep, testKinds)
	require.Contains(t, got, "s: output is not allowed on a sleep step")
}
