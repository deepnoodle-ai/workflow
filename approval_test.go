package workflow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

const heldDoc = `{
	"input": {"type": "object", "properties": {"target": {"type": "string"}}},
	"steps": [
		{"name": "deploy", "action": "deploy", "with": {"target": "${{ input.target }}"},
			"retry": {"max_attempts": 3},
			"approval": {"errors": ["guard_blocked"], "timeout": "24h"},
			"catch": [{"errors": ["guard_blocked", "approval_failed"], "next": "report"}],
			"next": "done"},
		{"name": "report", "action": "report", "with": {"error": "${{ deploy.error }}"}},
		{"name": "done", "action": "done"}
	]
}`

var prod = map[string]any{"target": "prod"}

// guarded is a fake host: deploy is blocked unless approved, and the
// approval job answers with answer. Every job it is given is kept.
type guarded struct {
	answer result
	jobs   []Command
}

func (g *guarded) handle(c Command) result {
	g.jobs = append(g.jobs, c)
	switch {
	case c.StepKind == KindApproval:
		return g.answer
	case c.Step != "deploy":
		return ok(nil)
	case !c.Approved:
		return fail("guard_blocked", "command-risk flagged publish (yes 92%)", false)
	}
	return ok(map[string]any{"deployed": c.Params["target"]})
}

func approve(reason string) result {
	return ok(map[string]any{"approved": true, "reason": reason})
}

func reject(reason string) result {
	return ok(map[string]any{"approved": false, "reason": reason})
}

// heldHost starts a run of heldDoc and holds its first deploy.
func heldHost(t *testing.T, answer result) (*fakeHost, *guarded) {
	t.Helper()
	g := &guarded{answer: answer}
	h := newHost(t, mustLoad(t, heldDoc), g.handle)
	h.start(prod)
	h.deliver("main/deploy#1")
	return h, g
}

func TestApprovalHoldApproveRun(t *testing.T) {
	g := &guarded{answer: approve("release 4.2")}
	h := newHost(t, mustLoad(t, heldDoc), g.handle)
	h.start(prod)
	cmds := h.deliver("main/deploy#1")

	// The blocked attempt is held: an approval job takes its place.
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandScheduleTimer))
	require.Equal(t, []string{"main/deploy#1"}, idsOf(cmds, CommandCancelStep),
		"the held attempt's timer is dropped")
	require.Equal(t, "action", cmds[0].StepKind)
	cmds = cmds[1:]
	job := cmds[0]
	require.Equal(t, KindApproval, job.StepKind)
	require.Equal(t, "deploy", job.Step)
	require.Equal(t, 1, job.Attempt)
	require.Equal(t, -1, job.Index)
	require.Equal(t, prod, job.Params)
	require.Equal(t, &Hold{
		Kind:    "action",
		Attempt: 1,
		Error: &StepError{
			Type:    "guard_blocked",
			Message: "command-risk flagged publish (yes 92%)",
		},
	}, job.Hold)
	require.Equal(t, []string{"approved"}, job.OutputSchema.Required)
	require.False(t, job.Approved)
	require.Equal(t, t0.Add(24*time.Hour), cmds[1].NotBefore)
	require.Equal(t, KindApproval, cmds[1].StepKind)
	require.Equal(t, StatusRunning, h.st.Status)
	require.Equal(t, []StepID{"main/deploy.approval#1"}, h.st.InFlight())
	held := h.st.Unit("main/deploy#1")
	require.Equal(t, UnitFailed, held.Status, "the held attempt has settled for the host")
	require.Equal(t, "guard_blocked", held.Error.Type)

	// Yes runs the same job again, approved.
	cmds = h.deliver("main/deploy.approval#1")
	require.Equal(t, []string{"main/deploy#2"}, idsOf(cmds, CommandDispatchStep))
	require.True(t, cmds[0].Approved)
	require.Equal(t, prod, cmds[0].Params)
	require.Nil(t, cmds[0].Hold)
	require.Equal(t, UnitCompleted, h.st.Unit("main/deploy.approval#1").Status)
	require.Equal(t, map[string]any{"approved": true, "reason": "release 4.2"},
		h.st.Unit("main/deploy.approval#1").Output)

	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{"deployed": "prod"}, h.st.Unit("main/deploy#2").Output)
	require.Equal(t, []StepID{
		"main/deploy#1", "main/deploy#2", "main/deploy.approval#1", "main/done#1",
	}, h.dispatched())

	// The approval job is its own unit: no side effect shares its key.
	require.Equal(t, "main/deploy.approval", StepID("main/deploy.approval#1").Unit())
	require.Equal(t, "deploy", StepID("main/deploy.approval#1").Step())
	require.Equal(t, 1, StepID("main/deploy.approval#1").Attempt())
}

// A no, a missed deadline or a broken approval job fails the step, not
// retryable, and its catch routes it. The deploy never runs again.
func TestApprovalRefusedOrBroken(t *testing.T) {
	cases := []struct {
		name    string
		answer  result
		timeout bool
		typ     string
		msg     string
	}{
		{"rejected", reject("wrong target"), false, "guard_blocked",
			"command-risk flagged publish (yes 92%); rejected: wrong target"},
		{"rejected without a reason", ok(map[string]any{"approved": false}), false,
			"guard_blocked", "command-risk flagged publish (yes 92%); rejected"},
		{"timed out", result{}, true, "guard_blocked",
			"command-risk flagged publish (yes 92%); not approved within 24h"},
		{"handler error", fail("error", "review queue down", true), false,
			ErrorTypeApprovalFailed, "approval job failed: error: review queue down"},
		{"invalid answer", ok(map[string]any{"approved": "yes"}), false,
			ErrorTypeApprovalFailed,
			"approval job failed: invalid_output: the answer is not valid"},
		{"no answer", ok(nil), false,
			ErrorTypeApprovalFailed, "approval job failed: invalid_output"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, g := heldHost(t, c.answer)
			var cmds []Command
			if c.timeout {
				cmds = h.fire()
				require.Equal(t, []string{"main/deploy.approval#1"},
					idsOf(cmds, CommandCancelStep))
			} else {
				h.deliver("main/deploy.approval#1")
			}

			n := h.st.nodes["main/deploy"]
			require.Equal(t, nodeFailed, n.Status)
			require.Equal(t, c.typ, n.Error.Type)
			require.True(t, strings.HasPrefix(n.Error.Message, c.msg), n.Error.Message)
			require.False(t, n.Error.Retryable)
			require.Contains(t, h.pendingIDs(), StepID("main/report#1"), "catch routes it")

			h.runToEnd(nil)
			require.Equal(t, StatusCompleted, h.st.Status)
			deploys := 0
			for _, j := range g.jobs {
				if j.Step == "deploy" && j.StepKind != KindApproval {
					deploys++
				}
			}
			require.Equal(t, 1, deploys)
		})
	}
}

// A handler's failure, or a claim that ran out, arrives as StepFailed.
func TestApprovalAbandoned(t *testing.T) {
	h, _ := heldHost(t, result{})
	h.apply(Event{
		Kind:   EventStepFailed,
		StepID: "main/deploy.approval#1",
		Error:  &StepError{Type: "abandoned", Message: "claimed 3 times", Retryable: true},
	})
	n := h.st.nodes["main/deploy"]
	require.Equal(t, ErrorTypeApprovalFailed, n.Error.Type)
	require.Equal(t, UnitFailed, n.Approval.Unit.Status)
	require.Equal(t, "abandoned", n.Approval.Unit.Error.Type)
}

// UC-7: an approved job gets the step's whole retry budget.
func TestApprovalRetryBudget(t *testing.T) {
	def := mustLoad(t, heldDoc)
	var deploys []Command
	h := newHost(t, def, func(c Command) result {
		switch {
		case c.StepKind == KindApproval:
			return approve("")
		case c.Step != "deploy":
			return ok(nil)
		}
		deploys = append(deploys, c)
		switch {
		case c.Attempt == 1:
			return fail("guard_unavailable", "decide is down", true)
		case !c.Approved:
			return fail("guard_blocked", "flagged", false)
		}
		return fail("deploy_failed", "registry 503", true)
	})
	h.start(prod)
	h.runToEnd(nil)

	ids := make([]StepID, len(deploys))
	for i, c := range deploys {
		ids[i] = c.StepID
		require.Equal(t, c.Attempt > 2, c.Approved, c.StepID)
	}
	require.Equal(t, []StepID{
		"main/deploy#1", "main/deploy#2", "main/deploy#3", "main/deploy#4", "main/deploy#5",
	}, ids)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "deploy_failed", h.st.Error.Type)
	require.Equal(t, StepID("main/deploy#5"), h.st.Error.StepID)

	// The approved job's retries back off from the first delay again.
	require.Equal(t, time.Time{}, deploys[2].NotBefore)
	require.Equal(t, 2*time.Second, deploys[4].NotBefore.Sub(deploys[3].NotBefore))
}

// After a yes, a listed error fails or retries as usual: it never holds
// the visit again.
func TestApprovalHoldsOnce(t *testing.T) {
	def := mustLoad(t, heldDoc)
	h := newHost(t, def, func(c Command) result {
		switch {
		case c.StepKind == KindApproval:
			return approve("")
		case c.Step == "deploy":
			return fail("guard_blocked", "flagged", false)
		}
		return ok(nil)
	})
	h.start(prod)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status, "catch routed to report")
	require.Equal(t, []StepID{
		"main/deploy#1", "main/deploy#2", "main/deploy.approval#1", "main/report#1",
	}, h.dispatched())
	require.Equal(t, "flagged", h.st.nodes["main/deploy"].Error.Message)
}

// UC-8: a definition cannot approve its own job.
func TestApprovalNotSetByParams(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "deploy", "action": "deploy",
		"with": {"approved": true, "Approved": true}}]}`)
	h := newHost(t, def, func(Command) result { return ok(nil) })
	h.start(nil)
	require.False(t, h.pending["main/deploy#1"].Approved)
}

func TestApprovalLateEventForHeldAttempt(t *testing.T) {
	h, _ := heldHost(t, result{})
	for _, ev := range []Event{
		{Kind: EventStepCompleted, StepID: "main/deploy#1", Output: map[string]any{}},
		{Kind: EventStepFailed, StepID: "main/deploy#1", Error: &StepError{Type: "x"}},
		{Kind: EventTimerFired, StepID: "main/deploy#1"},
	} {
		ev.At = h.now
		next, cmds, err := Apply(h.def, h.st, ev)
		require.NoError(t, err)
		require.True(t, next == h.st, "ignored")
		require.Empty(t, cmds)
	}

	// An approval job that is not there is unknown.
	_, _, err := Apply(h.def, h.st, Event{At: h.now, Kind: EventStepCompleted,
		StepID: "main/deploy.approval#2"})
	require.True(t, errors.Is(err, ErrUnknownStep))
	_, _, err = Apply(h.def, h.st, Event{At: h.now, Kind: EventStepCompleted,
		StepID: "main/report.approval#1"})
	require.True(t, errors.Is(err, ErrUnknownStep))
}

func TestApprovalResolve(t *testing.T) {
	g := &guarded{answer: approve("")}
	def := mustLoad(t, heldDoc)
	h := newHost(t, def, g.handle)
	h.start(prod)

	// Before a hold the deploy runs, but its name does not reach it.
	_, err := Resolve(def, h.st, "deploy")
	require.True(t, errors.Is(err, ErrNotWaiting), err)
	id, err := Resolve(def, h.st, "main/deploy#1")
	require.NoError(t, err)
	require.Equal(t, StepID("main/deploy#1"), id)

	h.deliver("main/deploy#1")
	id, err = Resolve(def, h.st, "deploy")
	require.NoError(t, err)
	require.Equal(t, StepID("main/deploy.approval#1"), id)

	// UC-5: a duplicate approval by name, while the approved job runs and
	// after the run ends, is refused.
	h.deliver("main/deploy.approval#1")
	_, err = Resolve(def, h.st, "deploy")
	require.True(t, errors.Is(err, ErrNotWaiting), err)
	h.runToEnd(nil)
	_, err = Resolve(def, h.st, "deploy")
	require.True(t, errors.Is(err, ErrNotWaiting), err)

	// By StepID, a duplicate is ignored.
	next, cmds, err := Apply(def, h.st, Event{At: h.now, Kind: EventStepCompleted,
		StepID: "main/deploy.approval#1", Output: map[string]any{"approved": true}})
	require.NoError(t, err)
	require.True(t, next == h.st)
	require.Empty(t, cmds)
}

// UC-6: a pause holds the deadline; Resume re-arms it in full. An answer
// delivered while paused dispatches the approved job, which the host holds.
func TestApprovalPause(t *testing.T) {
	h, _ := heldHost(t, approve(""))
	h.apply(Event{Kind: EventPause, Reason: "freeze"})
	h.now = h.now.Add(25 * time.Hour)
	cmds := h.fire()
	require.Empty(t, cmds)
	require.Equal(t, StatusPaused, h.st.Status)
	require.Equal(t, UnitRunning, h.st.nodes["main/deploy"].Approval.Unit.Status)

	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(out, CommandDispatchStep))
	require.Empty(t, idsOf(out, CommandScheduleTimer), "the deadline waits for Resume")

	cmds = h.apply(Event{Kind: EventResume})
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandScheduleTimer))
	require.Equal(t, h.now.Add(24*time.Hour), cmds[0].NotBefore)
	require.Equal(t, KindApproval, cmds[0].StepKind)

	h.apply(Event{Kind: EventPause})
	cmds = h.deliver("main/deploy.approval#1")
	require.Equal(t, []string{"main/deploy#2"}, idsOf(cmds, CommandDispatchStep))
	require.True(t, cmds[0].Approved)
	require.Equal(t, StatusPaused, h.st.Status)
	h.apply(Event{Kind: EventResume})
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestApprovalCancel(t *testing.T) {
	h, _ := heldHost(t, approve(""))
	cmds := h.apply(Event{Kind: EventCancel, Reason: "stop"})
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, StatusCancelled, h.st.Status)
	require.Empty(t, h.st.InFlight())

	// A late approval runs nothing.
	next, cmds, err := Apply(h.def, h.st, Event{At: h.now, Kind: EventStepCompleted,
		StepID: "main/deploy.approval#1", Output: map[string]any{"approved": true}})
	require.NoError(t, err)
	require.True(t, next == h.st)
	require.Empty(t, cmds)
	_, err = Resolve(h.def, h.st, "deploy")
	require.True(t, errors.Is(err, ErrNotWaiting), err)
}

func TestApprovalAbort(t *testing.T) {
	h, _ := heldHost(t, result{})
	next, cmds := Abort(h.st, &RunError{Type: "gone", Message: "gone"}, h.now)
	require.Equal(t, StatusFailed, next.Status)
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandCancelStep))
	require.Empty(t, next.InFlight())
}

func TestApprovalOutstanding(t *testing.T) {
	h, _ := heldHost(t, result{})
	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	var want []Command
	for _, c := range h.all {
		if c.StepID == "main/deploy.approval#1" {
			want = append(want, c)
		}
	}
	require.Equal(t, mustJSON(t, want), mustJSON(t, out))
}

// A step timeout, a listed error type, holds like any other.
func TestApprovalHoldsATimeout(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "deploy", "action": "deploy", "timeout": "1m",
		"approval": {"errors": ["timeout"], "timeout": "1h"}}]}`)
	h := newHost(t, def, func(c Command) result {
		return ok(map[string]any{"approved": true})
	})
	h.start(nil)
	delete(h.pending, "main/deploy#1") // the handler hangs
	cmds := h.fire()
	require.Equal(t, []string{"main/deploy#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandDispatchStep))
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, []StepID{"main/deploy#1", "main/deploy#2", "main/deploy.approval#1"},
		h.dispatched())
}

func TestApprovalStepID(t *testing.T) {
	for id, want := range map[StepID][3]string{
		"main/deploy.approval#1":        {"main/deploy.approval", "deploy", "1"},
		"main.a~2/ship@3.approval#1":    {"main.a~2/ship@3.approval", "ship", "1"},
		"failure/x.approval#4":          {"failure/x.approval", "x", "4"},
		"main/deploy[1].approval#1":     {"", "", ""},
		"main/deploy.approval#01":       {"", "", ""},
		"main/.approval#1":              {"", "", ""},
		"main/deploy.approval.approval": {"", "", ""},
	} {
		require.Equal(t, want[0], id.Unit(), id)
		require.Equal(t, want[1], id.Step(), id)
		_, _, _, suffix, ok := id.parse()
		require.Equal(t, want[0] != "", ok && suffix == suffixApproval, id)
	}
}

func TestApprovalLoadErrors(t *testing.T) {
	step := func(fields string) string {
		return `{"steps": [{"name": "a", ` + fields + `}]}`
	}
	ok := `"approval": {"errors": ["guard_blocked"], "timeout": "1h"}`
	cases := []struct{ doc, want string }{
		{step(`"wait": true, "timeout": "1h", ` + ok),
			"approval is not allowed on a wait step"},
		{step(`"set": 1, ` + ok),
			"approval is not allowed on a set step"},
		{step(`"sleep": "1s", ` + ok),
			"approval is not allowed on a sleep step"},
		{step(`"action": "x", "each": "${{ [1] }}", ` + ok),
			"approval is not allowed on an each step"},
		{step(`"action": "x", "approval": {"timeout": "1h"}`),
			"approval.errors must list"},
		{step(`"action": "x", "approval": {"errors": [], "timeout": "1h"}`),
			"approval.errors must list"},
		{step(`"action": "x", "approval": {"errors": ["e"]}`),
			"approval.timeout must be a positive"},
		{step(`"action": "x", "approval": {"errors": ["e"], "timeout": "-1s"}`),
			"approval.timeout must be"},
		{step(`"action": "x", "approval": {"errors": ["e"], "timeout": "1h", "who": 1}`),
			`unknown key "who"`},
		{step(`"action": "x", "approval": true`),
			"approval must be an object"},
		{step(`"approval": {"errors": ["e"], "timeout": "1h"}`),
			"no step kind"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "f", "action": "x", ` +
			ok + `}]}`, "finally steps cannot use approval"},
		{`{"steps": [{"name": "a", "action": "x"}], "on_failure": [{"name": "f", "action": "x", ` +
			ok + `}]}`, "on_failure steps cannot use approval"},
	}
	for _, c := range cases {
		_, err := Load([]byte(c.doc), testKinds)
		require.Error(t, err, c.doc)
		require.Contains(t, err.Error(), c.want, c.doc)
	}

	// The host must register the approval kind.
	_, err := Load([]byte(step(`"action": "x", `+ok)), KindMap{"action": nil})
	require.Error(t, err)
	require.Contains(t, err.Error(), `approval needs the host to register kind "approval"`)

	// The definition hash covers approval.
	a := mustLoad(t, step(`"action": "x", `+ok))
	b := mustLoad(t, step(`"action": "x", "approval": {"errors": ["e"], "timeout": "1h"}`))
	require.NotEqual(t, a.Hash(), b.Hash())
	require.Equal(t, &Approval{Errors: []string{"guard_blocked"}, Timeout: time.Hour},
		a.Step("a").Approval)
}

// A step timeout shorter than the review: the hold cancels the held
// attempt's timer, and one that fired anyway is ignored, so no unapproved
// attempt runs.
func TestApprovalStepTimeoutDuringHold(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "deploy", "action": "deploy", "timeout": "1m",
		"retry": {"max_attempts": 3},
		"approval": {"errors": ["guard_blocked"], "timeout": "2h"}}]}`)
	g := &guarded{answer: approve("")}
	h := newHost(t, def, g.handle)
	h.start(nil)
	cmds := h.deliver("main/deploy#1")
	require.Equal(t, []string{"main/deploy#1"}, idsOf(cmds, CommandCancelStep))
	_, timer := h.timers["main/deploy#1"]
	require.False(t, timer)

	h.now = h.now.Add(time.Minute)
	next, cmds, err := Apply(def, h.st, Event{
		At:     h.now,
		Kind:   EventTimerFired,
		StepID: "main/deploy#1",
	})
	require.NoError(t, err)
	require.True(t, next == h.st, "ignored")
	require.Empty(t, cmds)
	require.Equal(t, []StepID{"main/deploy#1", "main/deploy.approval#1"}, h.dispatched())
}

// The deadline message names the step's approval timeout.
func TestApprovalDeadlineMessage(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "deploy", "action": "deploy",
		"approval": {"errors": ["guard_blocked"], "timeout": "2h"}}]}`)
	h := newHost(t, def, (&guarded{}).handle)
	h.start(nil)
	h.deliver("main/deploy#1")
	cmds := h.fire()
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "command-risk flagged publish (yes 92%); not approved within 2h",
		h.st.Error.Message)
	require.Equal(t, []string{"main/deploy.approval#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, "no answer within 2h", h.st.nodes["main/deploy"].Approval.Unit.Error.Message)
}

// The approved job runs the params the person saw, even if what with reads
// changed during the review. No definition can change a path's scope while
// its step is held, so the test edits the stored input.
func TestApprovalRunsTheHeldParams(t *testing.T) {
	h, _ := heldHost(t, approve(""))
	require.Equal(t, prod, h.st.nodes["main/deploy"].Approval.Params)
	h.st.Input = map[string]any{"target": "staging"}

	out, err := Outstanding(h.def, h.st)
	require.NoError(t, err)
	require.Equal(t, prod, out[0].Params, "the approval job shows the held params")

	cmds := h.deliver("main/deploy.approval#1")
	require.Equal(t, []string{"main/deploy#2"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, prod, cmds[0].Params)

	// Every retry of the approved job runs them too.
	h.handle = func(Command) result { return fail("deploy_failed", "503", true) }
	cmds = h.deliver("main/deploy#2")
	require.Equal(t, []string{"main/deploy#3"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, prod, cmds[0].Params)
	require.True(t, cmds[0].Approved)
}
