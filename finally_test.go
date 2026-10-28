package workflow

import (
	"slices"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// finallyDoc acquires a lock, works, and always releases the lock. The
// finally steps read the outcome, a step that may not have run, and an
// earlier finally step that may have failed.
const finallyDoc = `{
  "steps": [
    {"name": "lock", "action": "x", "output": {"type": "object", "required": ["id"], "properties": {"id": {"type": "string"}}},
     "next": ["work"]},
    {"name": "work", "action": "x", "timeout": "1h"}
  ],
  "on_failure": [{"name": "report", "action": "x", "with": {"why": "${{ error.message }}"}}],
  "finally": [
    {"name": "release", "action": "x",
     "with": {"lock": "${{ lock?.id }}", "status": "${{ run.status }}", "error": "${{ run.error?.message }}"}},
    {"name": "notify", "action": "x", "with": {"released": "${{ release?.error == nil }}", "status": "${{ run.status }}"}}
  ],
  "outputs": {"lock": "${{ lock.id }}"}
}`

func finallyHandler(failing ...string) func(Command) result {
	return func(c Command) result {
		if slices.Contains(failing, c.Step) {
			return fail("boom", c.Step+" broke", false)
		}
		if c.Step == "lock" {
			return ok(map[string]any{"id": "L1"})
		}
		return ok(nil)
	}
}

func TestFinallyRunsAfterCompletion(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler())
	h.start(nil)
	h.deliver("main/lock#1")
	cmds := h.deliver("main/work#1")

	// The outcome is decided and the outputs are evaluated, but the run
	// finishes only once its finally steps have run.
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandDispatchStep))
	require.Nil(t, h.final)
	require.Equal(t, StatusCompleting, h.st.Status)
	require.Equal(t, StatusCompleted, h.st.Status.Outcome())
	require.False(t, h.st.Status.Terminal())
	require.True(t, h.st.FinishedAt.IsZero())
	require.Equal(t, map[string]any{"lock": "L1"}, h.st.Outputs)
	require.Equal(t, []StepID{"finally/release#1"}, h.st.InFlight())
	out, err := Outstanding(def, h.st)
	require.NoError(t, err)
	require.Equal(t, []string{"finally/release#1"}, idsOf(out, CommandDispatchStep))
	require.Equal(t, map[string]any{"lock": "L1", "status": "completed", "error": nil}, cmds[0].Params)

	cmds = h.deliver("finally/release#1")
	require.Equal(t, map[string]any{"released": true, "status": "completed"}, cmds[0].Params)
	cmds = h.deliver("finally/notify#1")
	require.Len(t, cmds, 1)
	require.Equal(t, CommandRunCompleted, cmds[0].Kind)
	require.Equal(t, map[string]any{"lock": "L1"}, cmds[0].Outputs)
	require.Empty(t, cmds[0].FinallyErrors)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.False(t, h.st.FinishedAt.IsZero())
	require.Empty(t, h.st.InFlight())
}

func TestFinallyRunsAfterFailure(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler("work"))
	h.start(nil)
	h.deliver("main/lock#1")
	cmds := h.deliver("main/work#1")
	require.Equal(t, []string{"failure/report#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusFailing, h.st.Status)

	// on_failure first, then finally, both while the run is failing.
	cmds = h.deliver("failure/report#1")
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, map[string]any{"lock": "L1", "status": "failed", "error": "work broke"}, cmds[0].Params)
	require.Equal(t, StatusFailing, h.st.Status)
	require.Nil(t, h.final)

	h.runToEnd(nil)
	require.Equal(t, CommandRunFailed, h.final.Kind)
	require.Equal(t, "work", h.final.Error.Step)
	require.Equal(t, StatusFailed, h.st.Status)
}

func TestFinallyRunsAfterOnFailureFails(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler("work", "report"))
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "report", h.final.Error.OnFailure.Step)
	require.Equal(t, []StepID{"failure/report#1", "finally/notify#1", "finally/release#1", "main/lock#1", "main/work#1"},
		h.dispatched())
}

func TestFinallyRunsAfterCancel(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler())
	h.start(nil)
	cmds := h.apply(Event{Kind: EventCancel, Reason: "superseded"})
	require.Equal(t, []string{"main/lock#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandDispatchStep))
	// lock never ran, and a cancelled run has no error.
	require.Equal(t, map[string]any{"lock": nil, "status": "cancelled", "error": nil}, cmds[len(cmds)-1].Params)
	require.Equal(t, StatusCancelling, h.st.Status)
	require.Equal(t, "superseded", h.st.Reason)
	require.Nil(t, h.final)

	// The cancelled step's late result is ignored.
	next, late, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: "main/lock#1",
		Output: map[string]any{"id": "L1"}})
	require.NoError(t, err)
	require.Empty(t, late)
	require.True(t, next == h.st)

	h.runToEnd(nil)
	require.Equal(t, CommandRunCancelled, h.final.Kind)
	require.Equal(t, "superseded", h.final.Reason)
	require.Empty(t, h.final.FinallyErrors)
	require.Equal(t, StatusCancelled, h.st.Status)
}

func TestCancelDuringOnFailureRunsFinally(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler("work"))
	h.start(nil)
	h.deliver("main/lock#1")
	h.deliver("main/work#1")
	require.Equal(t, StatusFailing, h.st.Status)
	cmds := h.apply(Event{Kind: EventCancel, Reason: "stop"})
	require.Equal(t, []string{"failure/report#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, "cancelled", cmds[len(cmds)-1].Params["status"])
	require.Nil(t, cmds[len(cmds)-1].Params["error"])
	require.Equal(t, StatusCancelling, h.st.Status)
	h.runToEnd(nil)
	require.Equal(t, CommandRunCancelled, h.final.Kind)
	require.Equal(t, "stop", h.final.Reason)
}

func TestFailingFinallyStepIsReported(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler("release"))
	h.start(nil)
	h.deliver("main/lock#1")
	h.deliver("main/work#1")
	cmds := h.deliver("finally/release#1")
	// The next finally step still runs, and sees the failure.
	require.Equal(t, []string{"finally/notify#1"}, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, map[string]any{"released": false, "status": "completed"}, cmds[0].Params)
	require.Equal(t, StatusCompleting, h.st.Status)
	h.runToEnd(nil)

	// The outcome does not change; the failure is on the state and the
	// terminal command.
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, CommandRunCompleted, h.final.Kind)
	require.Equal(t, map[string]any{"lock": "L1"}, h.final.Outputs)
	require.Len(t, h.final.FinallyErrors, 1)
	fe := h.final.FinallyErrors[0]
	require.Equal(t, "release", fe.Step)
	require.Equal(t, StepID("finally/release#1"), fe.StepID)
	require.Equal(t, "boom", fe.Type)
	require.Equal(t, "release broke", fe.Message)
	require.Equal(t, mustJSON(t, h.final.FinallyErrors), mustJSON(t, h.st.FinallyErrors))
}

func TestEveryFinallyStepRunsWhenAllFail(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler("work", "report", "release", "notify"))
	h.start(nil)
	h.runToEnd(nil)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, "work", h.final.Error.Step)
	require.Len(t, h.final.FinallyErrors, 2)
	require.Equal(t, "release", h.final.FinallyErrors[0].Step)
	require.Equal(t, "notify", h.final.FinallyErrors[1].Step)
}

func TestFinallyStepRetriesAndTimesOut(t *testing.T) {
	def := mustLoad(t, `{
	  "steps": [{"name": "a", "action": "x"}],
	  "finally": [
	    {"name": "flaky", "action": "x", "retry": {"max_attempts": 2, "backoff": "1s"}},
	    {"name": "slow", "action": "x", "timeout": "1m"},
	    {"name": "check", "set": "${{ flaky?.error == nil && slow?.error?.type == \"timeout\" }}"}
	  ]}`)
	attempts := 0
	h := newHost(t, def, func(c Command) result {
		if c.Step == "flaky" {
			attempts++
			if attempts == 1 {
				return fail("unavailable", "later", true)
			}
		}
		return ok(map[string]any{})
	})
	h.start(nil)
	h.deliver("main/a#1")
	cmds := h.deliver("finally/flaky#1")
	require.Equal(t, []string{"finally/flaky#2"}, idsOf(cmds, CommandDispatchStep))
	cmds = h.deliver("finally/flaky#2")
	require.Equal(t, []string{"finally/slow#1"}, idsOf(cmds, CommandDispatchStep))
	h.now = h.now.Add(time.Minute)
	cmds = h.fire()
	require.Equal(t, []string{"finally/slow#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, CommandRunCompleted, h.final.Kind)
	require.Len(t, h.final.FinallyErrors, 1, mustJSON(t, h.final.FinallyErrors))
	require.Equal(t, ErrorTypeTimeout, h.final.FinallyErrors[0].Type)
	require.Equal(t, true, h.st.nodes["finally/check"].Output)
}

func TestCancelDuringFinallyStopsCleanup(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler())
	h.start(nil)
	h.deliver("main/lock#1")
	h.deliver("main/work#1")
	require.Equal(t, StatusCompleting, h.st.Status)

	// A plain cancel cannot change a decided outcome, so the cleanup runs on.
	same, none, err := Apply(def, h.st, Event{At: t0, Kind: EventCancel, Reason: "shutting down"})
	require.NoError(t, err)
	require.Empty(t, none)
	require.True(t, same == h.st)

	cmds := h.apply(Event{Kind: EventCancel, Reason: "shutting down", Force: true})
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandCancelStep))
	require.Empty(t, idsOf(cmds, CommandDispatchStep))
	// The outcome was decided before the cancel: the run completes, with
	// its outputs, and reports the cleanup it did not finish.
	last := cmds[len(cmds)-1]
	require.Equal(t, CommandRunCompleted, last.Kind)
	require.Equal(t, map[string]any{"lock": "L1"}, last.Outputs)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, "", h.st.Reason)
	require.Len(t, last.FinallyErrors, 2)
	require.Equal(t, "release", last.FinallyErrors[0].Step)
	require.Equal(t, StepID("finally/release#1"), last.FinallyErrors[0].StepID)
	require.Equal(t, ErrorTypeCancelled, last.FinallyErrors[0].Type)
	require.Equal(t, "the run was cancelled while finally steps ran: shutting down", last.FinallyErrors[0].Message)
	require.Equal(t, "notify", last.FinallyErrors[1].Step)
	require.Equal(t, StepID(""), last.FinallyErrors[1].StepID)
	require.Equal(t, "did not run: the run was cancelled while finally steps ran: shutting down", last.FinallyErrors[1].Message)
	require.Empty(t, h.st.InFlight())

	// The run is over: another cancel and the late result are no-ops.
	next, more, err := Apply(def, h.st, Event{At: t0, Kind: EventCancel})
	require.NoError(t, err)
	require.Empty(t, more)
	require.True(t, next == h.st)
	_, more, err = Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: "finally/release#1"})
	require.NoError(t, err)
	require.Empty(t, more)
}

// A cancel decides the outcome once: delivering it again, as a durable
// host may, leaves the finally steps it started running. Only a forced
// cancel stops them.
func TestCancelIsIdempotent(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler())
	h.start(nil)
	h.apply(Event{Kind: EventCancel, Reason: "first"})
	require.Equal(t, StatusCancelling, h.st.Status)
	require.Equal(t, []StepID{"finally/release#1"}, h.st.InFlight())
	cmds := h.apply(Event{Kind: EventCancel, Reason: "first"})
	require.Empty(t, cmds)
	require.Equal(t, StatusCancelling, h.st.Status)
	require.Equal(t, []StepID{"finally/release#1"}, h.st.InFlight())

	cmds = h.apply(Event{Kind: EventCancel, Reason: "operator", Force: true})
	require.Equal(t, []string{"finally/release#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, CommandRunCancelled, h.final.Kind)
	require.Equal(t, "first", h.final.Reason, "the run's reason is the cancel that decided it")
	require.Len(t, h.final.FinallyErrors, 2)
	require.Contains(t, h.final.FinallyErrors[0].Message, "operator")
	require.Equal(t, StatusCancelled, h.st.Status)
}

// A forced cancel of a running run skips its finally steps.
func TestForcedCancelSkipsFinally(t *testing.T) {
	def := mustLoad(t, finallyDoc)
	h := newHost(t, def, finallyHandler())
	h.start(nil)
	cmds := h.apply(Event{Kind: EventCancel, Reason: "now", Force: true})
	require.Equal(t, []string{"main/lock#1"}, idsOf(cmds, CommandCancelStep))
	require.Empty(t, idsOf(cmds, CommandDispatchStep))
	require.Equal(t, StatusCancelled, h.st.Status)
	require.Equal(t, "now", h.final.Reason)
	require.Len(t, h.final.FinallyErrors, 2)
	for _, fe := range h.final.FinallyErrors {
		require.Equal(t, ErrorTypeCancelled, fe.Type)
		require.Equal(t, "did not run: the run was cancelled with force", fe.Message)
	}
}

func TestFinallyWithoutHostSteps(t *testing.T) {
	// A finally made only of set steps finishes within the same Apply.
	def := mustLoad(t, `{"steps": [{"name": "a", "action": "x"}],
	  "finally": [{"name": "s", "set": "${{ run.status }}"}]}`)
	h := newHost(t, def, func(Command) result { return ok(1) })
	h.start(nil)
	cmds := h.deliver("main/a#1")
	require.Len(t, cmds, 1)
	require.Equal(t, CommandRunCompleted, cmds[0].Kind)
	require.Equal(t, map[string]any{"a": float64(1)}, cmds[0].Outputs)
	require.Equal(t, "completed", h.st.nodes["finally/s"].Output)

	h = newHost(t, def, nil)
	h.start(nil)
	cmds = h.apply(Event{Kind: EventCancel})
	require.Equal(t, []CommandKind{CommandCancelStep, CommandRunCancelled}, []CommandKind{cmds[0].Kind, cmds[1].Kind})
	require.Equal(t, "cancelled", h.st.nodes["finally/s"].Output)
}

func TestFinallyValidation(t *testing.T) {
	cases := []struct{ doc, want string }{
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "a", "action": "x"}]}`, "step a: duplicate step name"},
		{`{"steps": [{"name": "a", "action": "x"}], "on_failure": [{"name": "f", "action": "x"}], "finally": [{"name": "f", "action": "x"}]}`,
			"step f: duplicate step name"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"action": "x", "next": "a"}]}`,
			"finally_1: finally steps run in order and cannot use next, catch or each"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"action": "x", "each": "[1]"}]}`, "finally steps run in order"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "j", "join": []}]}`, "j: finally steps cannot be join steps"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "z", "sleep": "1s"}]}`, "z: finally steps cannot be sleep steps"},
		{`{"steps": [{"name": "run", "action": "x"}]}`, "run: name is reserved"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "f", "action": "x", "with": {"m": "${{ error.message }}"}}]}`,
			"with reads error, which finally steps do not have; the run's error is run.error"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [
		  {"name": "f", "action": "x", "with": {"m": "${{ g }}"}}, {"name": "g", "action": "x"}]}`, "f: with reads g, which has not run"},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "f", "set": "${{ f }}"}]}`, "f: set reads f, which has not run"},
		{`{"steps": [{"name": "a", "action": "x", "with": {"m": "${{ f }}"}}], "finally": [{"name": "f", "action": "x"}]}`,
			`a: with reads unknown name "f"`},
		{`{"steps": [{"name": "a", "action": "x"}], "on_failure": [{"name": "o", "action": "x", "with": {"m": "${{ f }}"}}],
		  "finally": [{"name": "f", "action": "x"}]}`, `o: with reads unknown name "f"`},
		{`{"steps": [{"name": "a", "action": "x"}], "outputs": {"m": "${{ f }}"}, "finally": [{"name": "f", "action": "x"}]}`,
			`outputs: unknown name "f"`},
		{`{"steps": [{"name": "a", "action": "x"}], "on_failure": [{"name": "o", "action": "x", "with": {"m": "${{ run.status }}"}}]}`,
			`o: with reads unknown name "run"`},
		{`{"steps": [{"name": "a", "action": "x"}], "finally": [{"name": "f", "action": "x", "with": {"m": "${{ nope }}"}}]}`,
			`f: with reads unknown name "nope"`},
	}
	for _, c := range cases {
		_, err := Load([]byte(c.doc), testKinds)
		require.Error(t, err, c.doc)
		require.Contains(t, err.Error(), c.want, c.doc)
	}
}

func TestFinallyTypes(t *testing.T) {
	const steps = `"steps": [
	  {"name": "a", "action": "x", "output": {"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}}}],
	  "on_failure": [{"name": "o", "set": {"k": 1}}]`
	cases := []struct{ with, want string }{
		{`"${{ run.status }}"`, ""},
		{`"${{ run.status == \"completed\" }}"`, ""},
		{`"${{ run.status == \"complete\" }}"`, `never "complete"`},
		{`"${{ run.error?.message }}"`, ""},
		{`"${{ run.error.message }}"`, "may be nil"},
		{`"${{ run.error?.mesage }}"`, `no field "mesage"`},
		{`"${{ run.outputs }}"`, `no field "outputs"`},
		{`"${{ a?.n }}"`, ""},
		{`"${{ a.n }}"`, "may be nil"},
		{`"${{ coalesce(a?.n, 0) + 1 }}"`, ""},
		{`"${{ o?.k }}"`, ""},
		{`"${{ o.k }}"`, "may be nil"},
		{`"${{ f1?.v }}"`, ""},
		{`"${{ f1.error }}"`, "may be absent"},
		{`"${{ f1?.error?.message }}"`, ""},
		{`"${{ f1?.vv }}"`, `"vv"`},
	}
	for _, c := range cases {
		doc := `{` + steps + `, "finally": [
		  {"name": "f1", "action": "x", "output": {"type": "object", "required": ["v"], "properties": {"v": {"type": "string"}}}},
		  {"name": "f2", "action": "x", "with": {"x": ` + c.with + `}}]}`
		_, err := Load([]byte(doc), testKinds)
		if c.want == "" {
			require.NoError(t, err, c.with)
			continue
		}
		require.Error(t, err, c.with)
		require.Contains(t, err.Error(), c.want, c.with)
	}
}

// TestFinallyKeepsThePromise runs finally steps that validate through
// every outcome: none of them may fail on an expression.
func TestFinallyKeepsThePromise(t *testing.T) {
	doc := `{
	  "steps": [
	    {"name": "a", "action": "x", "output": {"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}},
	     "catch": [{"errors": ["caught"], "next": "c"}], "next": ["b"]},
	    {"name": "b", "action": "x", "output": {"type": "object", "properties": {"note": {"type": "string"}}}},
	    {"name": "c", "set": "${{ a.error.message }}"}],
	  "on_failure": [{"name": "o", "set": {"k": "${{ error.type }}"}}],
	  "finally": [
	    {"name": "f1", "action": "x", "output": {"type": "object", "required": ["v"], "properties": {"v": {"type": "string"}}},
	     "with": {"n": "${{ coalesce(a?.n, 0) + 1 }}", "note": "${{ b?.note }}", "s": "${{ upper(run.status) }}"}},
	    {"name": "f2", "set": {
	      "err": "${{ run.error?.message }}",
	      "k": "${{ o?.k }}",
	      "v": "${{ f1?.v }}",
	      "failed": "${{ f1?.error?.message }}",
	      "len": "${{ len(coalesce(f1?.v, \"\")) }}",
	      "caught": "${{ coalesce(c, \"no\") }}",
	      "msg": "${{ if(run.status == \"failed\", \"failed: \" + coalesce(run.error?.type, \"?\"), run.status) }}"}}
	  ]}`
	def := mustLoad(t, doc)
	for _, failing := range [][]string{nil, {"a"}, {"b"}, {"f1"}, {"b", "f1"}} {
		for _, caught := range []bool{false, true} {
			for _, cancel := range []bool{false, true} {
				h := newHost(t, def, func(c Command) result {
					for _, f := range failing {
						if c.Step == f {
							typ := "boom"
							if caught && f == "a" {
								typ = "caught"
							}
							return fail(typ, "broke", false)
						}
					}
					switch c.Step {
					case "a":
						return ok(map[string]any{"n": 1})
					case "f1":
						return ok(map[string]any{"v": "x"})
					}
					return ok(map[string]any{})
				})
				h.start(nil)
				if cancel {
					h.apply(Event{Kind: EventCancel})
				}
				h.runToEnd(nil)
				for _, fe := range h.st.FinallyErrors {
					if fe.Step != "f1" || !slices.Contains(failing, "f1") {
						t.Errorf("failing %v caught %v cancel %v: finally step failed: %s: %s %s",
							failing, caught, cancel, fe.Step, fe.Type, fe.Message)
					}
				}
				require.True(t, h.st.Status.Terminal())
			}
		}
	}
}

// Finally and on_failure steps read the run's input like any step.
func TestCleanupStepsReadInput(t *testing.T) {
	def := mustLoad(t, `{"input": {"type": "object", "required": ["env"], "properties": {"env": {"type": "string"}}},
	  "steps": [{"name": "a", "action": "x"}],
	  "on_failure": [{"name": "o", "action": "x", "with": {"env": "${{ input.env }}"}}],
	  "finally": [{"name": "f", "action": "x", "with": {"env": "${{ input.env }}", "status": "${{ run.status }}"}}]}`)
	h := newHost(t, def, deterministicHandler)
	h.start(map[string]any{"env": "staging"})
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status, h.st.Error)
	var got map[string]any
	for _, c := range h.all {
		if c.Step == "f" {
			got = c.Params
		}
	}
	require.Equal(t, map[string]any{"env": "staging", "status": "completed"}, got)
}
