package workflow

import (
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// answers is a fake decision handler: it answers each question by its
// text, and runs every other job with output.
func answers(byQuestion map[string]any, output any) func(Command) result {
	return func(c Command) result {
		if c.StepKind != KindDecision {
			return ok(output)
		}
		out := map[string]any{}
		for _, q := range c.Params["questions"].([]any) {
			q := q.(map[string]any)
			out[q["id"].(string)] = byQuestion[q["question"].(string)]
		}
		return ok(out)
	}
}

const refundDoc = `{"steps": [
	{"name": "intake", "action": "fetch", "match": "first", "next": [
		{"step": "approve", "when": "decide(\"Is this refund legitimate?\", intake.body) > 0.9"},
		{"step": "deny", "when": "decide(\"Is this refund legitimate?\", intake.body) < 0.2"},
		"human_review"
	]},
	{"name": "approve", "action": "x"},
	{"name": "deny", "action": "x"},
	{"name": "human_review", "action": "x"}
]}`

var body = map[string]any{"body": "Order 4411 arrived broken."}

func decisionJobs(h *fakeHost) []Command {
	var out []Command
	for _, c := range h.all {
		if c.Kind == CommandDispatchStep && c.StepKind == KindDecision {
			out = append(out, c)
		}
	}
	return out
}

func TestDecisionRoutesOnOneAsk(t *testing.T) {
	def := mustLoad(t, refundDoc)
	routes := map[float64]string{0.95: "approve", 0.1: "deny", 0.55: "human_review"}
	for answer, want := range routes {
		h := newHost(t, def, answers(map[string]any{"Is this refund legitimate?": answer},
			map[string]any{"body": "Order 4411 arrived broken."}))
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, StatusCompleted, h.st.Status)

		jobs := decisionJobs(h)
		require.Len(t, jobs, 1, "two edges, one question")
		job := jobs[0]
		require.Equal(t, StepID("main/intake.next#1"), job.StepID)
		require.Equal(t, "intake", job.Step)
		require.Equal(t, -1, job.Index)
		qs := job.Params["questions"].([]any)
		require.Len(t, qs, 1)
		q := qs[0].(map[string]any)
		require.Equal(t, "decide", q["type"])
		require.Equal(t, "Order 4411 arrived broken.", q["subject"])
		require.Equal(t, questionID("decide", "Is this refund legitimate?",
			"Order 4411 arrived broken.", nil), q["id"])

		require.Contains(t, h.dispatched(), StepID("main/"+want+"#1"))
		require.Len(t, h.dispatched(), 3)
	}
}

func TestDecisionWaitsAndIsSaved(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(map[string]any{"Is this refund legitimate?": 0.95}, body))
	h.start(nil)
	h.deliver("main/intake#1")
	require.Equal(t, []StepID{"main/intake.next#1"}, h.st.InFlight())
	require.NotNil(t, h.st.Unit("main/intake.next#1"))
	require.Equal(t, []StepID{"main/intake.next#1"}, h.pendingIDs())
	_, hasTimer := h.timers["main/intake.next#1"]
	require.True(t, hasTimer, "the default timeout bounds the attempt")

	h.deliver("main/intake.next#1")
	d := h.st.nodes["main/intake"].Decision
	require.Equal(t, UnitCompleted, d.Unit.Status)
	require.Len(t, d.Answers, 1)
	for _, a := range d.Answers {
		require.Equal(t, "Is this refund legitimate?", a.Question)
		require.Equal(t, 0.95, a.Value)
	}
	require.False(t, strings.Contains(mustJSON(t, h.st.nodes["main/intake"].Decision), "4411"),
		"no subject copy in the decision")

	// A duplicate answer is ignored, and the run never asks again.
	_, cmds, err := Apply(def, h.st, Event{
		Kind: EventStepCompleted, StepID: "main/intake.next#1", At: h.now, Output: map[string]any{},
	})
	require.NoError(t, err)
	require.Empty(t, cmds)
	h.runToEnd(nil)
	require.Len(t, decisionJobs(h), 1)
}

func TestDecisionAnswerZeroSurvivesReload(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(map[string]any{"Is this refund legitimate?": 0}, body))
	h.start(nil)
	h.runToEnd(nil)
	require.Contains(t, h.dispatched(), StepID("main/deny#1"))
}

func TestDecisionSwitchOnChoose(t *testing.T) {
	doc := `{"steps": [
		{"name": "intake", "action": "fetch", "next": {
			"switch": "choose(\"Which team owns this?\", intake, [\"billing\", \"engineering\", \"other\"])",
			"cases": {"billing": "billing_q", "engineering": "eng_q", "other": "triage"}
		}},
		{"name": "billing_q", "action": "x"},
		{"name": "eng_q", "action": "x"},
		{"name": "triage", "action": "x"}
	]}`
	def := mustLoad(t, doc)
	h := newHost(t, def, answers(map[string]any{"Which team owns this?": "engineering"},
		map[string]any{"b": 1, "a": "<x>"}))
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Contains(t, h.dispatched(), StepID("main/eng_q#1"))
	q := decisionJobs(h)[0].Params["questions"].([]any)[0].(map[string]any)
	require.Equal(t, `{"a":"<x>","b":1}`, q["subject"], "canonical JSON, keys sorted, no HTML escapes")
	require.Equal(t, []any{"billing", "engineering", "other"}, q["options"])
}

func TestDecisionAsksEveryCall(t *testing.T) {
	doc := `{"steps": [
		{"name": "a", "action": "fetch", "match": "first", "next": [
			{"step": "b", "when": "false && decide(\"one?\", a) > 0.5"},
			{"step": "c", "when": "if(true, true, decide(\"two?\", a) > 0.5)"},
			{"step": "d", "when": "try(decide(\"three?\", a) > 0.5, false)"}
		]},
		{"name": "b", "action": "x"},
		{"name": "c", "action": "x"},
		{"name": "d", "action": "x"}
	]}`
	def := mustLoad(t, doc)
	h := newHost(t, def, answers(map[string]any{"one?": 0.1, "two?": 0.2, "three?": 0.3}, "x"))
	h.start(nil)
	h.runToEnd(nil)
	require.Len(t, decisionJobs(h)[0].Params["questions"].([]any), 3)
	require.Contains(t, h.dispatched(), StepID("main/c#1"))
}

func TestDecisionLoopAsksEachVisit(t *testing.T) {
	doc := `{"steps": [
		{"name": "draft", "agent": "writer", "max_visits": 5, "next": [
			{"step": "draft", "when": "decide(\"Done?\", draft.text) < 0.9"},
			{"step": "publish", "when": "decide(\"Done?\", draft.text) >= 0.9"}
		]},
		{"name": "publish", "action": "x"}
	]}`
	def := mustLoad(t, doc)
	scores := []float64{0.5, 0.7, 0.95}
	visit := 0
	h := newHost(t, def, func(c Command) result {
		switch {
		case c.StepKind == KindDecision:
			q := c.Params["questions"].([]any)[0].(map[string]any)
			require.Equal(t, "v"+string(rune('0'+visit)), q["subject"])
			return ok(map[string]any{q["id"].(string): scores[visit-1]})
		case c.Step == "draft":
			visit++
			return ok(map[string]any{"text": "v" + string(rune('0'+visit))})
		}
		return ok(nil)
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, 3, visit)
	require.Len(t, decisionJobs(h), 3)
	require.Equal(t, StepID("main/draft@3.next#1"), decisionJobs(h)[2].StepID)
}

func TestDecisionFailureIsTheStepsFailure(t *testing.T) {
	doc := `{"steps": [
		{"name": "intake", "action": "fetch", "retry": 5, "timeout": "30m", "catch": "inbox",
		 "match": "first", "next": [
			{"step": "page", "when": "decide(\"Urgent?\", intake) > 0.8"},
			"inbox"
		]},
		{"name": "page", "action": "x"},
		{"name": "inbox", "action": "x", "with": {"why": "${{ intake?.error?.message }}"}}
	]}`
	def := mustLoad(t, doc)
	h := newHost(t, def, func(c Command) result {
		if c.StepKind == KindDecision {
			return fail("decision_failed", "provider down", true)
		}
		return ok("body")
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Len(t, decisionJobs(h), 3, "the decision policy, not the step's retry: 5")
	var inbox Command
	for _, c := range h.all {
		if c.Kind == CommandDispatchStep && c.Step == "inbox" {
			inbox = c
		}
	}
	require.Equal(t, "provider down", inbox.Params["why"])

	// Without catch, the run fails and names the attempt.
	doc = strings.Replace(doc, ` "catch": "inbox",`, "", 1)
	h = newHost(t, mustLoad(t, doc), func(c Command) result {
		if c.StepKind == KindDecision {
			return fail("decision_failed", "provider down", true)
		}
		return ok("body")
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "intake", h.st.Error.Step)
	require.Equal(t, StepID("main/intake.next#3"), h.st.Error.StepID)
	require.Equal(t, "decision_failed", h.st.Error.Type)
}

func TestDecisionAfterJoinUsesTheDefaultPolicy(t *testing.T) {
	doc := `{"steps": [
		{"name": "start", "action": "x", "next": ["tests", "lint"]},
		{"name": "tests", "action": "x", "next": "check"},
		{"name": "lint", "action": "x", "next": "check"},
		{"name": "check", "join": ["tests", "lint"], "match": "first", "next": [
			{"step": "ship", "when": "decide(\"Ready?\", {\"tests\": tests, \"lint\": lint}) > 0.8"},
			"fix"
		]},
		{"name": "ship", "action": "x"},
		{"name": "fix", "action": "x"}
	]}`
	def := mustLoad(t, doc)
	h := newHost(t, def, func(c Command) result {
		if c.StepKind == KindDecision {
			return fail("decision_failed", "provider down", true)
		}
		return ok(c.Step)
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "check", h.st.Error.Step)
	jobs := decisionJobs(h)
	require.Len(t, jobs, 3)
	q := jobs[0].Params["questions"].([]any)[0].(map[string]any)
	require.Equal(t, `{"lint":"lint","tests":"tests"}`, q["subject"])

	// A hung model times out after a minute.
	h = newHost(t, def, func(c Command) result { return ok(c.Step) })
	h.start(nil)
	for _, id := range []StepID{"main/start#1", "main/tests#1", "main.lint/lint#1"} {
		h.deliver(id)
	}
	timer := h.timers["main/check.next#1"]
	require.Equal(t, h.now.Add(time.Minute), timer.NotBefore)
	cmds := h.fire()
	require.Equal(t, []string{"main/check.next#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"main/check.next#2"}, idsOf(cmds, CommandDispatchStep))
}

func TestDecisionInvalidAnswers(t *testing.T) {
	doc := `{"steps": [
		{"name": "a", "action": "x", "next": {
			"switch": "choose(\"Team?\", a, [\"x\", \"y\"])", "cases": {"x": "b", "y": "b"}
		}},
		{"name": "b", "action": "x"}
	]}`
	def := mustLoad(t, doc)
	for _, bad := range []any{"z", 1.5, nil, map[string]any{}} {
		h := newHost(t, def, func(c Command) result {
			if c.StepKind != KindDecision {
				return ok("s")
			}
			if m, isMap := bad.(map[string]any); isMap {
				return ok(m)
			}
			id := c.Params["questions"].([]any)[0].(map[string]any)["id"].(string)
			return ok(map[string]any{id: bad, "extra": 1})
		})
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, StatusFailed, h.st.Status, bad)
		require.Equal(t, ErrorTypeInvalidOutput, h.st.Error.Type, bad)
		require.Len(t, decisionJobs(h), 3, "a bad answer uses one of the 3 attempts")
	}
}

func TestDecisionCancelledWithTheRun(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(nil, body))
	h.start(nil)
	h.deliver("main/intake#1")
	cmds := h.apply(Event{Kind: EventCancel, Reason: "stop"})
	require.Equal(t, []string{"main/intake.next#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, StatusCancelled, h.st.Status)
	require.Empty(t, h.st.InFlight())
}

func TestDecisionStepID(t *testing.T) {
	for id, want := range map[StepID][3]string{
		"main/intake.next#1":       {"main/intake.next", "intake", "1"},
		"main.a~2/draft@3.next#12": {"main.a~2/draft@3.next", "draft", "12"},
	} {
		require.Equal(t, want[0], id.Unit())
		require.Equal(t, want[1], id.Step())
		_, _, _, suffix, ok := id.parse()
		require.True(t, ok && suffix == suffixDecision, id)
	}
	for _, bad := range []StepID{"main/a[1].next#1", "main/.next#1", "main/a.next#01"} {
		_, _, _, _, ok := bad.parse()
		require.False(t, ok, bad)
	}
}

func TestDecisionLoadErrors(t *testing.T) {
	step := func(next string) string {
		return `{"steps": [{"name": "a", "action": "x", "match": "first", "next": [` + next +
			`, "b"]}, {"name": "b", "action": "x"}, {"name": "c", "action": "x"}]}`
	}
	for _, tc := range []struct{ next, want string }{
		{`{"step": "c", "when": "decide(\"q?\", a)"}`,
			"a: next c: when: decide returns the probability of yes (0 to 1); compare it, for example decide(...) > 0.8"},
		{`{"step": "c", "when": "decide(a, a) > 0.5"}`,
			"a: next c: when: decide: the question must be a string literal"},
		{`{"step": "c", "when": "decide(\"\", a) > 0.5"}`,
			"a: next c: when: decide: the question must be a string literal"},
		{`{"step": "c", "when": "decide(\"q?\") > 0.5"}`,
			"a: next c: when: decide takes (question, subject)"},
		{`{"step": "c", "when": "choose(\"q?\", a, [\"x\", \"x\"]) == \"x\""}`,
			"a: next c: when: choose: the options must be a list of at least two distinct, non-empty string literals"},
		{`{"step": "c", "when": "choose(\"q?\", a, [\"x\", \"\"]) == \"x\""}`,
			"a: next c: when: choose: the options must be a list of at least two distinct, non-empty string literals"},
		{`{"step": "c", "when": "choose(\"q?\", a, [a]) == \"x\""}`,
			"a: next c: when: choose: the options must be a list of at least two distinct, non-empty string literals"},
		{`{"step": "c", "when": "decide(\"q?\", decide(\"r?\", a)) > 0.5"}`,
			"a: next c: when: decide: the subject cannot ask another decision"},
		{`{"step": "c", "when": "any([a], decide(\"q?\", it) > 0.5)"}`,
			"a: next c: when: decide cannot be used inside map, filter or another list function"},
		{`{"step": "c", "when": "choose(\"q?\", a, [\"x\"]) == \"x\""}`,
			"a: next c: when: choose: the options must be a list of at least two distinct, non-empty string literals"},
		{`{"step": "c", "when": "choose(\"q?\", a, [\"x\", \"z\"]) == \"y\""}`, `"y"`},
	} {
		require.Contains(t, problemsFor(t, step(tc.next), testKinds), tc.want)
	}

	// Anywhere but next.
	doc := `{"steps": [{"name": "a", "action": "x", "with": {"v": "${{ decide(\"q?\", 1) }}"}}]}`
	require.Contains(t, problemsFor(t, doc, testKinds),
		"decide can only be used in next: when or switch")

	// A host without the kind.
	doc = step(`{"step": "c", "when": "decide(\"q?\", a) > 0.5"}`)
	require.Contains(t, problemsFor(t, doc, KindMap{"action": nil}),
		"a: next c: when: decide needs the decision kind; register a decision handler with the host")

	// The kind is reserved.
	doc = `{"steps": [{"name": "a", "decision": "x"}]}`
	require.Contains(t, problemsFor(t, doc, testKinds),
		`a: kind "decision" is reserved for decide and choose; give the host's kind another name`)

	// The names are reserved.
	doc = `{"steps": [{"name": "decide", "action": "x"}]}`
	require.Contains(t, problemsFor(t, doc, testKinds), "decide: name is reserved")

	// A switch on choose must handle every option.
	doc = `{"steps": [
		{"name": "a", "action": "x", "next": {
			"switch": "choose(\"Team?\", a, [\"x\", \"y\", \"z\"])", "cases": {"x": "b", "y": "b"}
		}},
		{"name": "b", "action": "x"}
	]}`
	require.Contains(t, problemsFor(t, doc, testKinds), "does not handle z; add cases or a default")
}

func TestDecisionOutstanding(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(nil, body))
	h.start(nil)
	emitted := h.deliver("main/intake#1")
	cmds, err := Outstanding(def, h.st)
	require.NoError(t, err)
	require.Equal(t, mustJSON(t, emitted), mustJSON(t, cmds))
}

func TestDecisionTimeoutIsNotTheSteps(t *testing.T) {
	doc := strings.Replace(refundDoc, `"action": "fetch",`,
		`"action": "fetch", "timeout": "30m",`, 1)
	h := newHost(t, mustLoad(t, doc), answers(nil, body))
	h.start(nil)
	h.deliver("main/intake#1")
	require.Equal(t, h.now.Add(time.Minute), h.timers["main/intake.next#1"].NotBefore)
}

func TestDecisionReplayAsksTheSameQuestion(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(map[string]any{"Is this refund legitimate?": 0.55}, body))
	h.start(nil)
	first := h.deliver("main/intake#1")
	h.deliver("main/intake.next#1")

	// Re-apply the run's events to a fresh state: the decision job, its
	// subject text and its ID are the same, so a replay that asks a new
	// model asks the same question.
	events := []Event{
		{Kind: EventStart, At: t0},
		{Kind: EventStepCompleted, StepID: "main/intake#1", At: t0, Output: body},
	}
	var st *State
	var cmds []Command
	for _, ev := range events {
		var err error
		st, cmds, err = Apply(def, st, ev)
		require.NoError(t, err)
		st = roundTrip(t, st)
	}
	require.Equal(t, mustJSON(t, first), mustJSON(t, cmds))

	// Replaying the answer too routes the same way and asks nothing new.
	out := map[string]any{}
	q := first[0].Params["questions"].([]any)[0].(map[string]any)
	out[q["id"].(string)] = 0.55
	_, cmds, err := Apply(def, st, Event{
		Kind: EventStepCompleted, StepID: "main/intake.next#1", At: t0, Output: out,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"main/human_review#1"}, idsOf(cmds, CommandDispatchStep))
}

func TestDecisionGuardDoesNotProtectTheSubject(t *testing.T) {
	doc := `{"steps": [
		{"name": "intake", "action": "fetch", "match": "first",
		 "output": {"type": "object", "properties": {"body": {"type": ["object", "null"],
		   "properties": {"text": {"type": "string"}}, "required": ["text"]}}, "required": ["body"]},
		 "next": [
			{"step": "page", "when": "intake.body != nil && decide(\"Urgent?\", intake.body.text) > 0.5"},
			"inbox"
		]},
		{"name": "page", "action": "x"},
		{"name": "inbox", "action": "x"}
	]}`
	problems := problemsFor(t, doc, testKinds)
	require.Contains(t, problems, `intake: next page: when: decide("Urgent?", ...): subject: `)
	require.Contains(t, problems, "a guard in the condition does not protect its subject")

	// Optional access, or the whole value, is safe.
	for _, subject := range []string{"intake.body?.text", "intake.body"} {
		fixed := strings.Replace(doc, "intake.body.text", subject, 1)
		require.Equal(t, "", problemsFor(t, fixed, testKinds), subject)
	}
}

func TestDecisionAbortCancelsIt(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(nil, body))
	h.start(nil)
	h.deliver("main/intake#1")
	st, cmds := Abort(h.st, &RunError{Type: "definition_unavailable", Message: "gone"}, h.now)
	require.Equal(t, []string{"main/intake.next#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, StatusFailed, st.Status)
	require.Empty(t, st.InFlight())
}

func TestDecisionInFlightOrder(t *testing.T) {
	doc := `{"steps": [
		{"name": "a", "action": "x", "next": ["b", "c"]},
		{"name": "b", "action": "x", "next": [{"step": "d", "when": "decide(\"Go?\", b) > 0.5"}]},
		{"name": "c", "action": "x", "each": "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10]",
		 "with": {"i": "${{ item }}"}},
		{"name": "d", "action": "x"}
	]}`
	h := newHost(t, mustLoad(t, doc), answers(nil, "x"))
	h.start(nil)
	h.deliver("main/a#1")
	h.deliver("main/b#1")
	ids := h.st.InFlight()
	// Items stay in item order, as before decisions; the decision job
	// follows its node.
	require.Len(t, ids, 12)
	require.Equal(t, StepID("main.c/c[2]#1"), ids[2])
	require.Equal(t, StepID("main.c/c[10]#1"), ids[10])
	require.Equal(t, StepID("main/b.next#1"), ids[11])
}

func TestDecisionFailureIsNotReadAsTheOutput(t *testing.T) {
	// The questions do not fit in the state, so the decision fails in the
	// Apply that asked it, after the step's output was read for the
	// subject. The catch route must read the error, not that output.
	doc := `{"steps": [
		{"name": "intake", "action": "fetch", "catch": "inbox", "match": "first", "next": [
			{"step": "page", "when": "decide(\"Urgent?\", intake) > 0.8"}, "inbox"
		]},
		{"name": "page", "action": "x"},
		{"name": "inbox", "action": "x", "with": {"why": "${{ intake?.error?.type }}"}}
	]}`
	def, err := Load([]byte(doc), testKinds, WithLimits(Limits{MaxStateBytes: 40}))
	require.NoError(t, err)
	h := newHost(t, def, answers(nil, "body"))
	h.start(nil)
	cmds := h.deliver("main/intake#1")
	require.Empty(t, decisionJobs(h))
	var inbox Command
	for _, c := range cmds {
		if c.Step == "inbox" {
			inbox = c
		}
	}
	require.Equal(t, ErrorTypeStateTooLarge, inbox.Params["why"])
}

func TestDecisionBadAnswerThenAGoodOne(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, func(c Command) result {
		if c.StepKind != KindDecision {
			return ok(body)
		}
		if c.Attempt == 1 {
			return ok("yes") // misformatted
		}
		return answers(map[string]any{"Is this refund legitimate?": 0.95}, body)(c)
	})
	h.start(nil)
	h.deliver("main/intake#1")
	h.deliver("main/intake.next#1")
	retry := h.st.Unit("main/intake.next#2")
	require.NotNil(t, retry, "the malformed answer is retried")
	require.Equal(t, ErrorTypeInvalidOutput, retry.Error.Type)
	require.True(t, retry.Error.Retryable)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Contains(t, h.dispatched(), StepID("main/approve#1"))
	require.Len(t, decisionJobs(h), 2)
}

func TestDecisionAnswersCountOnce(t *testing.T) {
	def := mustLoad(t, refundDoc)
	h := newHost(t, def, answers(map[string]any{"Is this refund legitimate?": 0.95}, body))
	h.start(nil)
	h.deliver("main/intake#1")
	h.deliver("main/intake.next#1")
	n := h.st.nodes["main/intake"]
	require.Equal(t, jsonSize(body)+jsonSize(n.Decision.Answers), n.Bytes)
}

func TestDecisionSubjectRunTimeFailureFailsTheRun(t *testing.T) {
	// Load types subjects, but cannot see an index out of range: the
	// guard does not protect the subject, so an empty list fails the run.
	doc := `{"steps": [
		{"name": "a", "action": "x", "match": "first",
		 "output": {"type": "object", "required": ["items"],
		   "properties": {"items": {"type": "array", "items": {"type": "string"}}}},
		 "next": [
			{"step": "b", "when": "len(a.items) > 0 && decide(\"q\", a.items[0]) > 0.5"},
			"c"
		]},
		{"name": "b", "action": "x"},
		{"name": "c", "action": "x"}
	]}`
	h := newHost(t, mustLoad(t, doc), answers(nil, map[string]any{"items": []any{}}))
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeExpression, h.st.Error.Type)
	require.Contains(t, h.st.Error.Message, "next b: when: decide(\"q\", ...): subject:")
	require.Empty(t, decisionJobs(h))
}
