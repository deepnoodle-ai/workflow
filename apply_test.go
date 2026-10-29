package workflow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestDuplicateEventsAreIgnored(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, prHandler)
	h.start(prInput)

	// A second Start is a no-op.
	next, cmds, err := Apply(def, h.st, Event{At: t0, Kind: EventStart})
	require.NoError(t, err)
	require.Empty(t, cmds)
	require.True(t, next == h.st)

	for h.final == nil {
		ids := h.pendingIDs()
		if len(ids) == 0 {
			break
		}
		id := ids[0]
		c := h.pending[id]
		h.deliver(id)
		// Redeliver the same completion: same state, no commands.
		before := mustJSON(t, h.st)
		_, dup, err := Apply(def, h.st, Event{
			At:     t0.Add(time.Hour),
			Kind:   EventStepCompleted,
			StepID: id,
			Output: prHandler(c).output,
		})
		require.NoError(t, err)
		require.Empty(t, dup)
		require.Equal(t, before, mustJSON(t, h.st))
		// A failure for the settled attempt is ignored too.
		_, dup, err = Apply(def, h.st, Event{
			At:     t0,
			Kind:   EventStepFailed,
			StepID: id,
			Error:  &StepError{Type: "x"},
		})
		require.NoError(t, err)
		require.Empty(t, dup)
	}
	require.Equal(t, StatusCompleted, h.st.Status)

	// After the run finishes every event is a no-op.
	_, cmds, err = Apply(def, h.st, Event{At: t0, Kind: EventCancel})
	require.NoError(t, err)
	require.Empty(t, cmds)
}

func TestUnknownStepIsAnError(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, prHandler)
	h.start(prInput)
	for _, id := range []StepID{"main/nope#1", "main/pending#2", "main/pending[0]#1", "garbage"} {
		_, _, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: id})
		require.Error(t, err, id)
	}
	_, _, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: "main/nope#1"})
	require.ErrorIs(t, err, ErrUnknownStep)
}

func TestCancelMidFanOut(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, prHandler)
	h.start(prInput)
	h.deliver("main/pending#1")
	h.deliver("main.correctness/correctness#1")
	h.deliver("main.contracts/contracts#1")
	h.deliver("main.tests/tests#1")
	h.deliver("main/verify[1]#1")
	require.Equal(t, []StepID{"main/verify[0]#1", "main/verify[2]#1"}, h.pendingIDs())

	cmds := h.apply(Event{Kind: EventCancel, Reason: "superseded by a newer commit"})
	want := []string{"main/verify[0]#1", "main/verify[2]#1"}
	require.Equal(t, want, idsOf(cmds, CommandCancelStep))
	require.Equal(t, CommandRunCancelled, cmds[len(cmds)-1].Kind)
	require.Equal(t, "superseded by a newer commit", cmds[len(cmds)-1].Reason)
	require.Empty(t, idsOf(cmds, CommandDispatchStep), "a cancelled run does not run on_failure")
	require.Equal(t, StatusCancelled, h.st.Status)

	// Late completions are ignored.
	for _, id := range []StepID{"main/verify[0]#1", "main/verify[2]#1"} {
		next, late, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: id})
		require.NoError(t, err)
		require.Empty(t, late)
		require.True(t, next == h.st)
	}
}

const retryDoc = `{
  "steps": [
    {"name": "flaky", "action": "x",
     "retry": {"max_attempts": 3, "backoff": "10s", "factor": 3, "errors": ["unavailable"]}}
  ]
}`

// A backoff past the int64 range of a Duration stops at max_backoff.
func TestRetryBackoffDoesNotOverflow(t *testing.T) {
	r := &Retry{Backoff: time.Second, MaxBackoff: 100 * 365 * 24 * time.Hour, Factor: 1e12}
	for attempt := 1; attempt < 6; attempt++ {
		d := r.backoff(attempt)
		require.True(t, d > 0 && d <= r.MaxBackoff, "attempt %d: %s", attempt, d)
	}
	require.Equal(t, r.MaxBackoff, r.backoff(5))
}

func TestRetryWithBackoff(t *testing.T) {
	def := mustLoad(t, retryDoc)
	attempts := 0
	h := newHost(t, def, func(c Command) result {
		attempts++
		if c.Attempt < 3 {
			return fail("unavailable", "try later", true)
		}
		return ok("done")
	})
	h.start(nil)
	cmds := h.deliver("main/flaky#1")
	require.Len(t, cmds, 1)
	require.Equal(t, StepID("main/flaky#2"), cmds[0].StepID)
	require.Equal(t, 2, cmds[0].Attempt)
	require.Equal(t, t0.Add(10*time.Second), cmds[0].NotBefore)

	cmds = h.deliver("main/flaky#2")
	require.Equal(t, StepID("main/flaky#3"), cmds[0].StepID)
	require.Equal(t, t0.Add(10*time.Second+30*time.Second), cmds[0].NotBefore)

	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, 3, attempts)
	require.Equal(t, map[string]any{"flaky": "done"}, h.st.Outputs)
}

func TestRetryIsExplicit(t *testing.T) {
	cases := map[string]struct {
		doc string
		err result
	}{
		"no retry declared": {`{"steps": [{"name": "a", "action": "x"}]}`, fail("unavailable", "down", true)},
		"not retryable":     {retryDoc, fail("unavailable", "down", false)},
		"type not listed":   {retryDoc, fail("invalid", "bad request", true)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			def := mustLoad(t, tc.doc)
			h := newHost(t, def, func(Command) result { return tc.err })
			h.start(nil)
			h.runToEnd(nil)
			require.Equal(t, StatusFailed, h.st.Status)
			require.Len(t, h.dispatched(), 1)
		})
	}

	// Retries stop at max_attempts.
	def := mustLoad(t, retryDoc)
	h := newHost(t, def, func(Command) result { return fail("unavailable", "down", true) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, []StepID{"main/flaky#1", "main/flaky#2", "main/flaky#3"}, h.dispatched())
	require.Equal(t, StepID("main/flaky#3"), h.final.Error.StepID)
}

const eachDoc = `{
  "steps": [
    {"name": "items", "set": "${{ input.items }}", "next": ["work"]},
    {"name": "work", "each": "items", "action": "x", "with": {"n": "${{ item }}", "i": "${{ index }}"},
     "concurrency": 2 %s, "next": ["after"]},
    {"name": "after", "set": "${{ work }}"}
  ]
}`

func TestEachConcurrencyAndOrder(t *testing.T) {
	def := mustLoad(t, strings.Replace(eachDoc, "%s", "", 1))
	h := newHost(t, def, func(c Command) result { return ok(c.Params["n"].(float64) * 10) })
	h.start(map[string]any{"items": []any{1, 2, 3, 4, 5}})
	require.Equal(t, []StepID{"main/work[0]#1", "main/work[1]#1"}, h.pendingIDs())

	// Completing item 1 frees a slot for item 2.
	cmds := h.deliver("main/work[1]#1")
	require.Equal(t, []string{"main/work[2]#1"}, idsOf(cmds, CommandDispatchStep))

	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	// Output is in item order regardless of completion order.
	require.Equal(t, map[string]any{"after": []any{10.0, 20.0, 30.0, 40.0, 50.0}}, h.st.Outputs)
}

func TestEachFailingItemCancelsItemsInFlight(t *testing.T) {
	def := mustLoad(t, strings.Replace(eachDoc, "%s", "", 1))
	h := newHost(t, def, func(c Command) result {
		if c.Index == 1 {
			return fail("boom", "item 1 broke", false)
		}
		return ok(c.Params["n"])
	})
	h.start(map[string]any{"items": []any{1, 2, 3, 4, 5}})

	// Item 1 fails while item 0 is in flight: the step is certain to fail,
	// so item 0 is cancelled, nothing new is dispatched, and the run fails.
	cmds := h.deliver("main/work[1]#1")
	require.Equal(t, []string{"main/work[0]#1"}, idsOf(cmds, CommandCancelStep))
	require.Empty(t, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, CommandRunFailed, cmds[len(cmds)-1].Kind)
	require.Equal(t, "item 1 broke", h.final.Error.Message)
	require.Equal(t, StepID("main/work[1]#1"), h.final.Error.StepID)
	n := h.st.nodes["main/work"]
	require.Equal(t, UnitCancelled, n.Units[0].Status)
	require.Equal(t, UnitSkipped, n.Units[4].Status)
}

func TestEachContinueOnError(t *testing.T) {
	def := mustLoad(t, strings.Replace(eachDoc, "%s", `, "continue_on_error": true`, 1))
	h := newHost(t, def, func(c Command) result {
		if c.Index == 1 {
			return fail("boom", "item 1 broke", false)
		}
		return ok(c.Params["n"])
	})
	h.start(map[string]any{"items": []any{1, 2, 3}})
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, []any{
		1.0,
		map[string]any{"error": map[string]any{"type": "boom", "message": "item 1 broke"}},
		3.0,
	}, h.st.Outputs["after"])
}

func TestEachEmptyList(t *testing.T) {
	def := mustLoad(t, strings.Replace(eachDoc, "%s", "", 1))
	h := newHost(t, def, nil)
	h.start(map[string]any{"items": []any{}})
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{"after": []any{}}, h.st.Outputs)
}

const onFailureDoc = `{
  "steps": [{"name": "a", "action": "x"}],
  "on_failure": [
    {"name": "notify", "action": "notify", "with": {"msg": "${{ error.step }}: ${{ error.message }}"}},
    {"name": "cleanup", "action": "cleanup", "with": {"why": "${{ error.type }}"}}
  ]
}`

func TestOnFailureRunsInOrder(t *testing.T) {
	def := mustLoad(t, onFailureDoc)
	h := newHost(t, def, func(c Command) result {
		if c.Step == "a" {
			return fail("boom", "a broke", false)
		}
		return ok(nil)
	})
	h.start(nil)
	cmds := h.deliver("main/a#1")
	require.Equal(t, []string{"failure/notify#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, "a: a broke", cmds[0].Params["msg"])
	cmds = h.deliver("failure/notify#1")
	require.Equal(t, []string{"failure/cleanup#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, "boom", cmds[0].Params["why"])
	h.runToEnd(nil)
	require.Equal(t, CommandRunFailed, h.final.Kind)
	require.Equal(t, "boom", h.final.Error.Type)
	require.Nil(t, h.final.Error.OnFailure)
}

func TestOnFailureFailureKeepsBothErrors(t *testing.T) {
	def := mustLoad(t, onFailureDoc)
	h := newHost(t, def, func(c Command) result { return fail("boom", c.Step+" broke", false) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "a broke", h.final.Error.Message)
	require.NotNil(t, h.final.Error.OnFailure)
	require.Equal(t, "notify", h.final.Error.OnFailure.Step)
	require.Equal(t, "notify broke", h.final.Error.OnFailure.Message)
	require.Len(t, h.dispatched(), 2, "cleanup never runs")
	require.Contains(t, h.final.Error.Error(), "on_failure: step notify")
}

func TestCancelDuringOnFailure(t *testing.T) {
	def := mustLoad(t, onFailureDoc)
	h := newHost(t, def, func(c Command) result { return fail("boom", "x", false) })
	h.start(nil)
	h.deliver("main/a#1")
	cmds := h.apply(Event{Kind: EventCancel})
	require.Equal(t, []string{"failure/notify#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, StatusCancelled, h.st.Status)
}

func TestTimeout(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "slow", "action": "x", "timeout": "5m", "retry": {"max_attempts": 2, "backoff": "1m"}}]}`)
	h := newHost(t, def, func(Command) result { return ok("late") })
	cmds := h.apply(Event{Kind: EventStart})
	require.Equal(t, CommandScheduleTimer, cmds[1].Kind)
	require.Equal(t, t0.Add(5*time.Minute), cmds[1].NotBefore)

	// The timer fires before the step finishes: cancel it and retry.
	h.now = t0.Add(5 * time.Minute)
	cmds = h.fire()
	require.Equal(t, []string{"main/slow#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"main/slow#2"}, idsOf(cmds, CommandDispatchStep))
	// The retry's timeout counts from when it may start.
	require.Equal(t, h.now.Add(time.Minute+5*time.Minute), cmds[2].NotBefore)

	// The first attempt's late result is ignored.
	_, late, err := Apply(def, h.st, Event{
		At:     h.now,
		Kind:   EventStepCompleted,
		StepID: "main/slow#1",
	})
	require.NoError(t, err)
	require.Empty(t, late)

	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestSleep(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "wait", "sleep": "1h", "next": ["after"]},
	  {"name": "after", "action": "x"}]}`)
	h := newHost(t, def, func(Command) result { return ok(1) })
	cmds := h.apply(Event{Kind: EventStart})
	require.Equal(t, []Command{{
		Kind:      CommandScheduleTimer,
		StepID:    "main/wait#1",
		Step:      "wait",
		StepKind:  "sleep",
		Index:     -1,
		Attempt:   1,
		NotBefore: t0.Add(time.Hour),
	}}, cmds)

	_, _, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: "main/wait#1"})
	require.ErrorIs(t, err, ErrInvalidEvent)

	cmds = h.fire()
	require.Equal(t, []string{"main/after#1"}, idsOf(cmds, CommandDispatchStep))
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestCatchRoutesFailure(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "charge", "action": "x", "catch": [{"errors": ["declined"], "next": "apologize"}], "next": ["ship"]},
	  {"name": "ship", "action": "x"},
	  {"name": "apologize", "action": "x", "with": {"why": "${{ charge.error.message }}"}}]}`)
	h := newHost(t, def, func(c Command) result {
		if c.Step == "charge" {
			return fail("declined", "card declined", false)
		}
		return ok(c.Params)
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	want := map[string]any{"apologize": map[string]any{"why": "card declined"}}
	require.Equal(t, want, h.st.Outputs)

	// An error type the catch does not list fails the run.
	h = newHost(t, def, func(c Command) result { return fail("network", "down", false) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
}

// A step's own edges run only after it completes, so they read its
// output even when its catch loops back to it, where a with: that reads
// the step's last visit may see {error: ...}.
func TestCaughtStepEdgesReadItsOutput(t *testing.T) {
	const doc = `{"steps": [
	  {"name": "plan", "action": "x", "max_visits": 3, "next": "attempt"},
	  {"name": "attempt", "action": "x",
	   "output": {"type": "object", "required": ["done"], "properties": {"done": {"type": "boolean"}}},
	   "catch": "plan", "next": [{"step": "plan", "when": "!attempt.done"}]}]}`
	def := mustLoad(t, doc)

	attempts := 0
	h := newHost(t, def, func(c Command) result {
		if c.Step != "attempt" {
			return ok(nil)
		}
		attempts++
		if attempts == 1 {
			return fail("boom", "first try", false)
		}
		return ok(map[string]any{"done": attempts == 3})
	})

	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, 3, attempts)

	// plan's with: may see the caught failure of the last visit.
	got := problemsFor(t, strings.Replace(doc, `"next": "attempt"}`,
		`"next": "attempt", "with": {"d": "${{ attempt.done }}"}}`, 1), testKinds)
	require.Contains(t, got, `field "done" may be absent; use ?.`)
}

func TestMatchFirstAndConditions(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "classify", "agent": "triage", "match": "first", "next": [
	    {"step": "fix", "when": "classify.kind == \"bug_fix\""},
	    {"step": "feature", "when": "classify.kind == \"feature\""},
	    {"step": "other"}]},
	  {"name": "fix", "action": "x"}, {"name": "feature", "action": "x"}, {"name": "other", "action": "x"}]}`)
	for kind, want := range map[string]string{
		"bug_fix": "fix",
		"feature": "feature",
		"docs":    "other",
	} {
		h := newHost(t, def, func(c Command) result { return ok(map[string]any{"kind": kind}) })
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, []StepID{"main/classify#1", StepID("main/" + want + "#1")}, h.dispatched())
	}
}

func TestNoMatchingEdgeEndsPath(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "a", "action": "x", "next": [{"step": "b", "when": "a > 5"}, {"step": "c", "when": "a > 1"}]},
	  {"name": "b", "action": "x"}, {"name": "c", "action": "x"}]}`)
	h := newHost(t, def, func(c Command) result { return ok(0) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{"a": 0.0}, h.st.Outputs)
}

func TestLoopWithMaxVisits(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "draft", "agent": "writer", "max_visits": 3, "next": ["review"]},
	  {"name": "review", "agent": "editor", "match": "first", "next": [
	    {"step": "draft", "when": "!review.approved"}, {"step": "publish"}]},
	  {"name": "publish", "action": "x"}]}`)

	approveOn := 2
	h := newHost(t, def, func(c Command) result {
		if c.Step == "review" {
			return ok(map[string]any{
				"approved": strings.HasSuffix(string(c.StepID), "@2#1") && approveOn == 2,
			})
		}
		return ok(nil)
	})
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, []StepID{
		"main/draft#1", "main/draft@2#1", "main/publish#1", "main/review#1", "main/review@2#1",
	}, h.dispatched())

	// Never approving hits the bound.
	h = newHost(t, def, func(c Command) result { return ok(map[string]any{"approved": false}) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeMaxVisits, h.final.Error.Type)
}

func TestNamedJoinMissingStepFails(t *testing.T) {
	const doc = `{"steps": [
	  {"name": "start", "action": "x", "next": ["a", {"step": "b", "when": "false"}]},
	  {"name": "a", "action": "x", "next": ["j"]},
	  {"name": "b", "action": "x", "next": ["j"]},
	  {"name": "j", "join": ["a", "b"]}]}`
	// Load sees that b may be skipped.
	require.Contains(t, problemsFor(t, doc, testKinds),
		"j: join: b may not have settled when the join fires")
	// The run-time check behind it fails the run at the join.
	def := mustLoadLenient(t, doc)
	h := newHost(t, def, func(Command) result { return ok(1) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeJoin, h.final.Error.Type)
	require.Contains(t, h.final.Error.Message, "b")
}

func TestUnnamedJoinWaitsForEveryReachingPath(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "start", "action": "x", "next": ["a", "b", {"step": "c", "when": "false"}]},
	  {"name": "a", "action": "x", "next": ["a2"]},
	  {"name": "a2", "action": "x", "next": ["j"]},
	  {"name": "b", "action": "x", "next": ["j"]},
	  {"name": "c", "action": "x", "next": ["j"]},
	  {"name": "j", "join": [], "next": ["done"]},
	  {"name": "done", "set": "${{ [a2, b] }}"}]}`)
	h := newHost(t, def, func(c Command) result { return ok(c.Step) })
	h.start(nil)
	h.deliver("main/start#1")
	h.deliver("main.b/b#1")
	h.deliver("main.a/a#1")
	require.Equal(t, []string{"main.b"}, h.st.joins["j"])
	h.deliver("main.a/a2#1")
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, map[string]any{"done": []any{"a2", "b"}}, h.st.Outputs)
	require.Equal(t, pathEnded, h.st.paths["main"].Status)
	require.Equal(t, "main/done", h.st.paths["main"].Node)
}

func TestNestedForkJoin(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "s", "action": "x", "next": ["a", "b"]},
	  {"name": "a", "action": "x", "next": ["x", "y"]},
	  {"name": "x", "action": "x", "next": ["ja"]},
	  {"name": "y", "action": "x", "next": ["ja"]},
	  {"name": "ja", "join": ["x", "y"], "next": ["a_done"]},
	  {"name": "a_done", "action": "x", "next": ["j"]},
	  {"name": "b", "action": "x", "next": ["j"]},
	  {"name": "j", "join": ["a_done", "b"], "next": ["end"]},
	  {"name": "end", "action": "x"}]}`)
	h := newHost(t, def, func(c Command) result { return ok(1) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, []StepID{
		"main.a.x/x#1", "main.a.y/y#1", "main.a/a#1", "main.a/a_done#1",
		"main.b/b#1", "main/end#1", "main/s#1",
	}, h.dispatched())
}

func TestOutputLimits(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "big", "action": "x"}]}`)
	def.limits = Limits{MaxOutputBytes: 100}
	h := newHost(t, def, func(Command) result { return ok(strings.Repeat("x", 200)) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeOutputTooLarge, h.final.Error.Type)
	require.Contains(t, h.final.Error.Message, "limit is 100")

	def = mustLoad(t, strings.Replace(eachDoc, "%s", "", 1))
	def.limits = Limits{MaxStateBytes: 1000}
	h = newHost(t, def, func(Command) result { return ok(strings.Repeat("y", 300)) })
	h.start(map[string]any{"items": []any{1, 2, 3, 4, 5}})
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeStateTooLarge, h.final.Error.Type)
}

// The limits apply to an output with its defaults filled in, which is
// what the run holds.
func TestOutputLimitsCountDefaults(t *testing.T) {
	def := mustLoad(t, fmt.Sprintf(`{"steps": [{"name": "a", "action": "x", "output": {
	  "type": "object",
	  "properties": {"note": {"type": "string", "default": %q}}}}]}`,
		strings.Repeat("d", 200)))
	def.limits = Limits{MaxOutputBytes: 100}
	h := newHost(t, def, func(Command) result { return ok(map[string]any{}) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeOutputTooLarge, h.final.Error.Type)
}

func TestExpressionFailureFailsRun(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "a", "action": "x", "next": ["b"]},
	  {"name": "b", "action": "x", "with": {"v": "${{ a.missing }}"}}]}`)
	h := newHost(t, def, func(Command) result { return ok(map[string]any{"present": 1}) })
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeExpression, h.final.Error.Type)
	require.Equal(t, "b", h.final.Error.Step)
	require.Contains(t, h.final.Error.Message, "missing")
}

func TestInput(t *testing.T) {
	def := mustLoad(t, `{
	  "input": {"type": "object", "required": ["repo"], "additionalProperties": false, "properties": {
	    "repo": {"type": "string"}, "depth": {"type": "integer", "default": 2}, "note": {"type": "string"},
	    "opts": {"type": "object", "default": {}, "properties": {"verbose": {"type": "boolean", "default": false}}}}},
	  "steps": [{"name": "a", "action": "x", "with": {"repo": "${{ input.repo }}", "depth": "${{ input.depth }}",
	    "note": "${{ input.note }}", "verbose": "${{ input.opts.verbose }}"}}]}`)
	_, _, err := Apply(def, nil, Event{
		At:    t0,
		Kind:  EventStart,
		Input: map[string]any{"depth": "deep", "typo": 1},
	})
	require.ErrorIs(t, err, ErrInvalidInput)
	require.Contains(t, err.Error(), `input: missing required property "repo"`)
	require.Contains(t, err.Error(), `input: unexpected property "typo"`)
	require.Contains(t, err.Error(), `input.depth: expected integer, got string`)

	h := newHost(t, def, nil)
	cmds := h.apply(Event{Kind: EventStart, Input: map[string]any{"repo": "r", "depth": nil}})
	require.Equal(t, map[string]any{"repo": "r", "depth": 2.0, "note": nil, "verbose": false},
		cmds[0].Params)
	require.Equal(t, map[string]any{"repo": "r", "depth": 2.0, "note": nil,
		"opts": map[string]any{"verbose": false}}, h.st.Input)
}

func TestInputMustBeAnObjectSchema(t *testing.T) {
	for _, doc := range []string{
		`"string"`, `{"type": "array"}`, `{"type": ["object", "null"]}`, `{"properties": {}}`,
	} {
		def := `{"input": ` + doc + `, "steps": [{"name": "a", "action": "x"}]}`
		got := problemsFor(t, def, testKinds)
		require.Contains(t, got, "input: must be an object schema", doc)
	}
	def := `{"input": "missing", "steps": [{"name": "a", "action": "x"}]}`
	got := problemsFor(t, def, testKinds)
	require.Contains(t, got, `unknown schema "missing"`)
	mustLoad(t, `{"schemas": {"args": {"type": "object"}}, "input": "args", "steps": [{"name": "a", "action": "x"}]}`)
}

func TestDefaultsAreCheckedAtLoad(t *testing.T) {
	got := problemsFor(t, `{"schemas": {"level": {"enum": ["info", "debug"], "default": "trace"}},
	  "input": {"type": "object", "properties": {"n": {"type": "integer", "default": "two"}}},
	  "steps": [{"name": "a", "action": "x", "output": {"type": "object", "properties": {"ok": {"type": "boolean", "default": 1}}}}]}`, testKinds)
	require.Contains(t, got, `schemas.level.default: "trace" is not one of`)
	require.Contains(t, got, `input.n.default: expected integer, got string`)
	require.Contains(t, got, `a: output.ok.default: expected boolean, got integer`)
}

func TestTextTemplatesAndTypes(t *testing.T) {
	def := mustLoad(t, `{"steps": [{"name": "a", "action": "x", "with": {
	  "list": "${{ [1, 2] }}", "spaced": "  ${{ input.n }}  ", "text": "n=${{ input.n }}", "obj": "obj=${{ input.o }}",
	  "literal": "plain", "nested": {"deep": ["${{ input.n + 1 }}", true]}}}]}`)
	h := newHost(t, def, nil)
	cmds := h.apply(Event{
		Kind:  EventStart,
		Input: map[string]any{"n": 5, "o": map[string]any{"k": 1}},
	})
	require.Equal(t, map[string]any{
		"list": []any{1.0, 2.0}, "spaced": "  5  ", "text": "n=5", "obj": `obj={"k":1}`,
		"literal": "plain", "nested": map[string]any{"deep": []any{6.0, true}},
	}, cmds[0].Params)
}

func TestDefinitionMismatch(t *testing.T) {
	a := mustLoad(t, `{"steps": [{"name": "a", "action": "x"}]}`)
	b := mustLoad(t, `{"steps": [{"name": "b", "action": "x"}]}`)
	st, _, err := Apply(a, nil, Event{At: t0, Kind: EventStart})
	require.NoError(t, err)
	_, _, err = Apply(b, st, Event{At: t0, Kind: EventCancel})
	require.ErrorIs(t, err, ErrDefinitionMismatch)

	_, _, err = Apply(&Definition{}, nil, Event{At: t0, Kind: EventStart})
	require.ErrorIs(t, err, ErrNotValidated)
}

func TestOutstandingRebuildsInFlightCommands(t *testing.T) {
	def := mustLoad(t, prReview)
	h := newHost(t, def, prHandler)
	h.start(prInput)
	h.deliver("main/pending#1")
	h.deliver("main.correctness/correctness#1")
	h.deliver("main.contracts/contracts#1")
	h.deliver("main.tests/tests#1")
	h.deliver("main/verify[1]#1")

	cmds, err := Outstanding(def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"main/verify[0]#1", "main/verify[2]#1"},
		idsOf(cmds, CommandDispatchStep))
	for _, c := range cmds {
		require.Equal(t, mustJSON(t, h.pending[c.StepID]), mustJSON(t, c))
	}
	require.Equal(t, []StepID{"main/verify[0]#1", "main/verify[2]#1"}, h.st.InFlight())
}

func TestStepIDParse(t *testing.T) {
	for id, want := range map[StepID][3]any{
		"main/verify[3]#1":       {"main/verify", 3, 1},
		"main.a~2/x@4#12":        {"main.a~2/x@4", -1, 12},
		"failure/on_failure_1#1": {"failure/on_failure_1", -1, 1},
	} {
		key, index, attempt, ok := id.parse()
		require.True(t, ok, id)
		require.Equal(t, want, [3]any{key, index, attempt})
	}
	for _, bad := range []StepID{"", "x", "x#0", "x#a", "x[a]#1", "x]#1", "x[-1]#1"} {
		_, _, _, ok := bad.parse()
		require.False(t, ok, bad)
	}
}
