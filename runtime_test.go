package workflow

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestRetryShorthandMustBeInteger(t *testing.T) {
	doc := `{"steps": [{"name": "a", "action": "x", "retry": 2.5}]}`
	problems := validationProblems(t, doc, testKinds)
	require.Equal(t, []string{"a: retry must be an integer"}, problems)

	// 2^63 is a float64 that converts to a wrapped int on some platforms.
	doc = `{"steps": [{"name": "a", "action": "x", "max_visits": 9223372036854775808}]}`
	problems = validationProblems(t, doc, testKinds)
	require.Equal(t, []string{"a: max_visits must be an integer"}, problems)

	def := mustLoad(t, `{"steps": [{"name": "a", "action": "x", "retry": 3}]}`)
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
	def := mustLoad(t, `{"input": {"type": "object", "properties": {"s": {"type": "string"}}}, "steps": [{"name": "big", "set": "${{ input.s }}"}]}`)
	big := map[string]any{"s": strings.Repeat("x", 200)}
	def.limits = Limits{MaxOutputBytes: 100}
	h := newHost(t, def, nil)
	h.start(big)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeOutputTooLarge, h.final.Error.Type)
	require.Contains(t, h.final.Error.Message, "limit is 100")

	def.limits = Limits{MaxOutputBytes: 1000}
	h = newHost(t, def, nil)
	h.start(big)
	require.Equal(t, StatusCompleted, h.st.Status)
}

func TestEventTimeNeverRunsBackwards(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "a", "action": "x", "timeout": "1m", "next": ["b"]},
	  {"name": "b", "sleep": "10m"}]}`)
	st, _, err := Apply(def, nil, Event{At: t0, Kind: EventStart})
	require.NoError(t, err)
	early := t0.Add(-time.Hour)
	st, cmds, err := Apply(def, st, Event{
		At:     early,
		Kind:   EventStepCompleted,
		StepID: "main/a#1",
		Output: 1,
	})
	require.NoError(t, err)
	a := st.nodes["main/a"]
	require.Equal(t, t0, a.SettledAt)
	require.False(t, a.SettledAt.Before(a.EnteredAt))
	require.Equal(t, t0, st.nodes["main/b"].EnteredAt)
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
	  {"name": "work", "action": "x", "each": "[0, 1, 2]", "timeout": "1m",
	   "catch": "handle", "next": ["done"]},
	  {"name": "handle", "action": "x"},
	  {"name": "done", "action": "x"}]}`)
	st, cmds, err := Apply(def, nil, Event{At: t0, Kind: EventStart})
	require.NoError(t, err)
	require.Equal(t, []string{"main/work[0]#1", "main/work[1]#1", "main/work[2]#1"},
		idsOf(cmds, CommandDispatchStep))

	st, cmds, err = Apply(def, st, Event{
		At:     t0,
		Kind:   EventStepFailed,
		StepID: "main/work[0]#1",
		Error:  &StepError{Type: "boom", Message: "item 0 broke"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"main/work[1]#1", "main/work[2]#1"}, idsOf(cmds, CommandCancelStep))
	require.Equal(t, []string{"main/handle#1"}, idsOf(cmds, CommandDispatchStep),
		"the catch proceeds at once")
	want := mustJSON(t, st)

	late := []Event{
		{At: t0, Kind: EventStepCompleted, StepID: "main/work[1]#1", Output: 1},
		{
			At:     t0,
			Kind:   EventStepFailed,
			StepID: "main/work[2]#1",
			Error:  &StepError{Type: "other", Message: "x"},
		},
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

func checkBytes(t *testing.T, st *State) {
	t.Helper()
	sum := 0
	for _, n := range st.nodes {
		sum += n.Bytes
	}
	require.Equal(t, sum, st.Bytes)
}

// A poll/sleep loop is bounded only by its sleep, so it may run forever.
// Its state must not grow with the number of passes.
func TestPollLoopRunsIndefinitelyWithinStateLimit(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "poll", "action": "x", "match": "first", "next": [
	    {"step": "done", "when": "poll.done"},
	    {"step": "wait"}]},
	  {"name": "wait", "sleep": "1m", "next": ["poll"]},
	  {"name": "done", "set": "${{ poll.n }}"}]}`)
	def.limits = Limits{MaxStateBytes: 10 << 10}
	const passes = 500
	calls := 0
	h := newHost(t, def, func(c Command) result {
		calls++
		return ok(map[string]any{
			"n":    calls,
			"done": calls == passes,
			"pad":  strings.Repeat("p", 900),
		})
	})
	h.start(nil)
	maxNodes := 0
	for h.final == nil {
		if len(h.pending) > 0 {
			h.deliver(h.pendingIDs()[0])
		} else {
			h.fire()
		}
		checkBytes(t, h.st)
		require.True(t, h.st.Bytes <= 2*950, "bytes %d", h.st.Bytes)
		maxNodes = max(maxNodes, len(h.st.nodes))
	}
	require.Equal(t, StatusCompleted, h.st.Status, h.st.Error)
	require.Equal(t, map[string]any{"done": float64(passes)}, h.st.Outputs)
	require.True(t, maxNodes <= 4, "nodes %d", maxNodes)

	// The surviving visits are the latest; a result for a dropped visit
	// is a late delivery, not an unknown step.
	require.NotNil(t, h.st.nodes[fmt.Sprintf("main/poll@%d", passes)])
	require.Nil(t, h.st.nodes["main/poll@3"])
}

// A loop that forks a branch which ends without rejoining runs the
// branch's step as visit 1 on every pass. The outputs, default or
// declared, still see the last pass, and the earlier passes are dropped.
func TestLoopBranchThatEndsKeepsOnlyItsLastPass(t *testing.T) {
	const doc = `{"steps": [
	  {"name": "poll", "action": "x", "output": "poll_result", "next": ["note", "tick", "tock"]},
	  {"name": "note", "action": "x"},
	  {"name": "tick", "action": "x", "next": ["j"]},
	  {"name": "tock", "action": "x", "next": ["j"]},
	  {"name": "j", "join": ["tick", "tock"], "next": [{"step": "wait", "when": "!poll.done"}]},
	  {"name": "wait", "sleep": "1m", "next": ["poll"]}],
	  "schemas": {"poll_result": {
	    "type": "object",
	    "properties": {"done": {"type": "boolean"}},
	    "required": ["done"]}}
	  %s}`
	const passes = 12
	for name, outputs := range map[string]string{
		"default":  "",
		"declared": `, "outputs": {"note": "${{ note }}"}`,
	} {
		t.Run(name, func(t *testing.T) {
			def := mustLoad(t, fmt.Sprintf(doc, outputs))
			polls, notes := 0, 0
			h := newHost(t, def, func(c Command) result {
				switch c.Step {
				case "poll":
					polls++
					return ok(map[string]any{"done": polls == passes})
				case "note":
					notes++
					return ok(map[string]any{"n": notes})
				}
				return ok(map[string]any{})
			})
			h.start(nil)

			maxNodes := 0
			for h.final == nil {
				if len(h.pending) > 0 {
					h.deliver(h.pendingIDs()[0])
				} else {
					h.fire()
				}
				checkBytes(t, h.st)
				maxNodes = max(maxNodes, len(h.st.nodes))
			}

			require.Equal(t, StatusCompleted, h.st.Status, h.st.Error)
			require.Equal(t, map[string]any{"n": float64(passes)}, h.st.Outputs["note"])
			// At most a step's settled visit and the next one, not one per pass.
			require.True(t, maxNodes <= 2*len(def.Steps), "nodes %d", maxNodes)
		})
	}
}

func TestResultForDroppedVisitIsIgnored(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "poll", "action": "x", "timeout": "5m", "next": ["wait"]},
	  {"name": "wait", "sleep": "1m", "next": ["poll"]}]}`)
	h := newHost(t, def, func(Command) result { return ok("v") })
	h.start(nil)
	for range 3 {
		h.deliver(h.pendingIDs()[0])
		h.fire()
	}
	require.Nil(t, h.st.nodes["main/poll"], "the first visit was dropped")
	for _, ev := range []Event{
		{At: t0, Kind: EventStepCompleted, StepID: "main/poll#1", Output: "late"},
		{
			At:     t0,
			Kind:   EventStepFailed,
			StepID: "main/poll#2",
			Error:  &StepError{Type: "x"},
		},
		{At: t0, Kind: EventTimerFired, StepID: "main/poll#1"},
		{At: t0, Kind: EventTimerFired, StepID: "main/wait#1"},
	} {
		next, cmds, err := Apply(def, h.st, ev)
		require.NoError(t, err, ev.StepID)
		require.Empty(t, cmds)
		require.True(t, next == h.st)
	}
	for _, id := range []StepID{"main/poll@9#1", "other/poll#1", "main/nope#1", "main/poll@1#1"} {
		_, _, err := Apply(def, h.st, Event{At: t0, Kind: EventStepCompleted, StepID: id})
		require.ErrorIs(t, err, ErrUnknownStep, id)
	}
}

// A loop around a fork and a join drops each pass's branch visits once
// the next pass supersedes them, and outputs read the latest pass.
func TestForkLoopDropsSupersededPasses(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "fan", "action": "x", "next": ["a", "b"]},
	  {"name": "a", "action": "x", "next": ["j"]},
	  {"name": "b", "action": "x", "next": ["j"]},
	  {"name": "j", "join": ["a", "b"], "match": "first", "next": [
	    {"step": "fin", "when": "a.n >= 30"},
	    {"step": "wait"}]},
	  {"name": "wait", "sleep": "1s", "next": ["fan"]},
	  {"name": "fin", "set": "${{ a.n + b.n }}"}],
	  "outputs": {"a": "${{ a.n }}", "b": "${{ b.n }}", "fin": "${{ fin }}"}}`)
	count := map[string]int{}
	h := newHost(t, def, func(c Command) result {
		count[c.Step]++
		return ok(map[string]any{"n": count[c.Step], "pad": strings.Repeat("p", 100)})
	})
	h.start(nil)
	maxNodes := 0
	for h.final == nil {
		if ids := h.pendingIDs(); len(ids) > 0 {
			h.deliver(ids[len(ids)-1])
		} else {
			h.fire()
		}
		checkBytes(t, h.st)
		maxNodes = max(maxNodes, len(h.st.nodes))
	}
	require.Equal(t, StatusCompleted, h.st.Status, h.st.Error)
	require.Equal(t, map[string]any{"a": 30.0, "b": 30.0, "fin": 60.0}, h.st.Outputs)
	require.True(t, maxNodes <= 10, "nodes %d", maxNodes)
	require.True(t, h.st.Bytes < 3*200, "bytes %d", h.st.Bytes)
}

// loopWorkflow wraps a random graph in a loop that runs it three times,
// with every action step and the outputs reading every step.
func loopWorkflow(t *testing.T, seed int64) (*Definition, []byte) {
	g := &graphGen{rng: rand.New(rand.NewSource(seed))}
	start := g.add(map[string]any{"name": "start", "action": "x"})
	first, last := g.block(4, "")
	link(start, first["name"].(string), "")
	last["match"] = "first"
	link(last, "wait", "start.pass < 3")
	link(last, "fin", "")
	g.add(map[string]any{"name": "wait", "sleep": "1s", "next": []any{"start"}})
	g.add(map[string]any{"name": "fin", "set": "done"})
	var names []string
	for _, s := range g.steps[:len(g.steps)-1] {
		names = append(names, s["name"].(string))
	}
	all := "${{ [" + strings.Join(names, ", ") + "] }}"
	outputs := map[string]any{"all": all, "fin": "${{ fin }}"}
	for _, s := range g.steps {
		if s["action"] != nil {
			s["with"] = map[string]any{"seen": all, "item": s["with"]}
		}
	}
	data, err := json.Marshal(map[string]any{"steps": g.steps, "outputs": outputs})
	require.NoError(t, err)
	def, err := Load(data, testKinds)
	require.NoError(t, err, string(data))
	return def, data
}

func passHandler(c Command) result {
	res := deterministicHandler(c)
	pass := 1
	node, _, _, _ := c.StepID.parse()
	if at := strings.LastIndexByte(node, '@'); at > strings.LastIndexByte(node, '/') {
		pass, _ = strconv.Atoi(node[at+1:])
	}
	res.output.(map[string]any)["pass"] = float64(pass)
	return res
}

// Dropping superseded visits changes nothing a run can observe: across
// random graphs in a loop and shuffled completion orders, the commands
// (with their params, which read every step) and the outputs are the same
// as when every visit is kept, and every order reaches the same state.
func TestDroppingVisitsIsUnobservable(t *testing.T) {
	t.Parallel()
	for graph := range int64(20) {
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
			def, data := loopWorkflow(t, graph)
			keep, err := Load(data, testKinds)
			require.NoError(t, err)
			keep.keepVisits = true

			ref := newHost(t, keep, passHandler)
			ref.start(nil)
			ref.runToEnd(nil)
			require.Equal(t, StatusCompleted, ref.st.Status, "graph %d: %v", graph, ref.st.Error)
			wantCmds := sortedCommands(t, ref.all)
			wantOutputs := mustJSON(t, ref.st.Outputs)

			var wantState string
			for order := range int64(5) {
				h := newHost(t, def, passHandler)
				h.start(nil)
				h.runToEnd(rand.New(rand.NewSource(order*104729 + graph)))
				require.Equal(t, wantOutputs, mustJSON(t, h.st.Outputs),
					"graph %d order %d", graph, order)
				require.Equal(t, wantCmds, sortedCommands(t, h.all),
					"graph %d order %d", graph, order)
				checkBytes(t, h.st)
				require.True(t, len(h.st.nodes) < len(ref.st.nodes),
					"graph %d: nothing dropped", graph)
				if order == 0 {
					wantState = mustJSON(t, h.st)
				}
				require.Equal(t, wantState, mustJSON(t, h.st), "graph %d order %d", graph, order)
			}
		})
	}
}

func sortedCommands(t *testing.T, cmds []Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = mustJSON(t, c)
	}
	slices.Sort(out)
	return out
}

// A path keeps reading the visit it saw even after another path's loop
// made a newer visit of the same step.
func TestLivePathKeepsItsVisit(t *testing.T) {
	def := mustLoad(t, `{"steps": [
	  {"name": "x", "action": "x", "max_visits": 3, "next": ["p1", "q1"]},
	  {"name": "p1", "action": "x", "next": ["p2"]},
	  {"name": "p2", "action": "x", "with": {"x": "${{ x }}"}},
	  {"name": "q1", "action": "x", "match": "first", "next": [{"step": "x", "when": "q1 < 2"}]}]}`)
	calls := map[string]int{}
	h := newHost(t, def, func(c Command) result {
		calls[c.Step]++
		return ok(calls[c.Step])
	})
	h.start(nil)
	h.deliver("main/x#1")
	h.deliver("main.q1/q1#1") // loops: x@2 forks again
	h.deliver("main.q1/x@2#1")
	require.Equal(t, 2.0, h.st.nodes["main.q1/x@2"].Units[0].Output)
	cmds := h.deliver("main.p1/p1#1")
	require.Equal(t, map[string]any{"x": 1.0}, cmds[0].Params, "p2 reads the x its path saw")
}

// Dropping visits must not change how joins in and around loops fire: on
// the loop graphs of the order-independence test, a run that drops
// superseded visits dispatches the same commands as one that keeps them.
func TestDroppingVisitsKeepsLoopJoins(t *testing.T) {
	t.Parallel()
	for graph := range int64(40) {
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
			def := randomLoopWorkflow(t, graph)
			keep := *def
			keep.keepVisits = true

			ref := newHost(t, &keep, deterministicHandler)
			ref.start(nil)
			ref.runToEnd(nil)
			require.Equal(t, StatusCompleted, ref.st.Status, "graph %d: %v", graph, ref.st.Error)
			want := sortedCommands(t, ref.all)
			for order := range int64(2) {
				h := newHost(t, def, deterministicHandler)
				h.start(nil)
				h.runToEnd(rand.New(rand.NewSource(order*7919 + graph)))
				require.Equal(t, StatusCompleted, h.st.Status,
					"graph %d order %d: %v", graph, order, h.st.Error)
				require.Equal(t, want, sortedCommands(t, h.all), "graph %d order %d", graph, order)
				checkBytes(t, h.st)
			}
		})
	}
}
