package workflow

import (
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// Regression tests for joins inside and downstream of loops. Handlers here
// decide by StepID, never by a counter, so every completion order sees the
// same outputs.

// A poll loop on one branch must hold a join downstream of it: the path
// sitting in the loop can still reach the join.
func TestJoinWaitsForUpstreamPollLoop(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"s","action":"x","next":["poll","other"]},
	 {"name":"poll","action":"x","max_visits":3,"match":"first",
	  "next":[{"step":"wait","when":"poll.again"},{"step":"j"}]},
	 {"name":"wait","sleep":"30s","next":"poll"},
	 {"name":"other","action":"x","next":"j"},
	 {"name":"j","join":[],"next":"fin"},
	 {"name":"fin","action":"x"}
	]}`)
	handle := func(c Command) result {
		if c.Step == "poll" {
			return ok(map[string]any{"again": !laterVisit(c)})
		}
		return ok(nil)
	}

	// The reported order: poll asks to wait, then other finishes.
	h := newHost(t, def, handle)
	h.start(nil)
	h.deliver("main/s#1")
	h.deliver("main.poll/poll#1")
	h.deliver("main.other/other#1")
	require.Equal(t, 0, countDispatches(h, "fin"))
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, 1, countDispatches(h, "fin"))

	ref := requireOrderIndependent(t, def, handle, 30)
	require.Equal(t, 1, countDispatches(ref, "fin"))
}

// A branch that loops back above the fork holds a join that its next pass
// can reach, whichever branch finishes first.
func TestJoinWaitsForLoopAroundFork(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"start","action":"x","max_visits":2,"next":["a","b"]},
	 {"name":"a","action":"x","next":"j"},
	 {"name":"b","action":"x","next":[{"step":"start","when":"b.again"}]},
	 {"name":"j","join":[],"next":"fin"},
	 {"name":"fin","action":"x"}
	]}`)
	handle := func(c Command) result {
		if c.Step == "b" {
			return ok(map[string]any{"again": !laterVisit(c)})
		}
		return ok(nil)
	}
	for _, first := range []StepID{"main.a/a#1", "main.b/b#1"} {
		h := newHost(t, def, handle)
		h.start(nil)
		h.deliver("main/start#1")
		h.deliver(first)
		h.runToEnd(nil)
		require.Equal(t, StatusCompleted, h.st.Status, "%s first", first)
		require.Equal(t, 1, countDispatches(h, "fin"), "%s first", first)
	}
	requireOrderIndependent(t, def, handle, 30)
}

// The order edges are listed in must not change which steps a loop pass
// resets. Here the loop {a, b} can be entered at either step, and the join
// after it names b, which settled in the first pass only.
func TestLoopEdgeOrderDoesNotMatter(t *testing.T) {
	const doc = `{"steps":[
	 {"name":"s","action":"x","match":"all","next":EDGES},
	 {"name":"a","action":"x","max_visits":2,"match":"first",
	  "next":[{"step":"j","when":"a.done"},{"step":"b"}]},
	 {"name":"b","action":"x","next":"a"},
	 {"name":"j","join":["b"],"next":"fin"},
	 {"name":"fin","action":"x"}
	]}`
	handle := func(c Command) result {
		if c.Step == "a" {
			return ok(map[string]any{"done": laterVisit(c)})
		}
		return ok(nil)
	}
	var states []string
	for _, edges := range []string{
		`["a", {"step":"b","when":"false"}]`,
		`[{"step":"b","when":"false"}, "a"]`,
	} {
		def := mustLoadLenient(t, strings.Replace(doc, "EDGES", edges, 1))
		h := newHost(t, def, handle)
		h.start(nil)
		h.runToEnd(nil)
		require.Equal(t, StatusCompleted, h.st.Status, "%s: %v", edges, h.st.Error)
		require.Equal(t, 1, countDispatches(h, "fin"))
		st := *h.st
		st.DefinitionHash = ""
		states = append(states, mustJSON(t, &st))
	}
	require.Equal(t, states[0], states[1])
}

// A join inside a loop checks its named steps against the current pass:
// a step that settled only in an earlier pass does not count, and a step
// upstream of the loop counts in every pass.
func TestJoinInLoopChecksCurrentPass(t *testing.T) {
	const doc = `{"steps":[
	 {"name":"setup","action":"x","next":"h"},
	 {"name":"h","action":"x","max_visits":3,"match":"all","next":[{"step":"a","when":"WHEN"},"b"]},
	 {"name":"a","action":"x","next":"j"},
	 {"name":"b","action":"x","next":"j"},
	 {"name":"j","join":["setup","a","b"],"next":"c"},
	 {"name":"c","action":"x","next":[{"step":"h","when":"c.again"}]}
	]}`
	handle := func(c Command) result {
		return ok(map[string]any{"again": !laterVisit(c), "first": !laterVisit(c)})
	}
	// a runs in both passes: completes after two.
	// Load cannot tell that these joins always see a, so the test turns
	// its check off to reach the run-time one.
	def := mustLoadLenient(t, strings.Replace(doc, "WHEN", "true", 1))
	h := requireOrderIndependent(t, def, handle, 20)
	require.Equal(t, StatusCompleted, h.st.Status, "%v", h.st.Error)
	require.Equal(t, 2, countDispatches(h, "c"))

	// a runs only in the first pass: the second pass of j is missing it.
	def = mustLoadLenient(t, strings.Replace(doc, "WHEN", "h.first", 1))
	h = requireOrderIndependent(t, def, handle, 20)
	require.Equal(t, StatusFailed, h.st.Status)
	require.Equal(t, ErrorTypeJoin, h.st.Error.Type)
	require.Contains(t, h.st.Error.Message, "settled a in this pass")
}

// A branch that goes back to the loop head without passing the join puts
// its next pass in a later cohort; the join fires once per pass whatever
// order the branches finish in.
func TestJoinInLoopWithBypassingBranch(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"h","action":"x","max_visits":4,"next":["a","b"]},
	 {"name":"a","action":"x","next":"j"},
	 {"name":"b","action":"x","next":"y"},
	 {"name":"y","action":"x","next":[{"step":"h","when":"y.again"}]},
	 {"name":"j","join":[],"next":"z"},
	 {"name":"z","action":"x","next":[{"step":"h","when":"z.again"}]}
	]}`)
	handle := func(c Command) result { return ok(map[string]any{"again": !laterVisit(c)}) }
	h := requireOrderIndependent(t, def, handle, 40)
	require.Equal(t, StatusCompleted, h.st.Status, "%v", h.st.Error)
	// Visits count per lineage, so each lineage's first b loops again.
	// Pass 1 fires with main.a alone; pass 2 merges the a branches of the
	// two lineages that went around (main.a via z, main.b via y) on a fresh
	// path, since main.a.b went its own way; pass 3
	// is main.a.b's, which went around twice.
	require.Equal(t, 3, countDispatches(h, "z"))
	require.Equal(t, []string{"main.a/z", "main.j/z@2", "main.a.b.a/z@2"}, nodesOf(h, "z"))

	// Without z's back edge the join is after the loop, not in it: it
	// waits for every pass and fires once.
	def = mustLoad(t, strings.Replace(mustJSON(t, def), `,"next":[{"step":"h","when":"z.again"}]`, "", 1))
	require.Equal(t, 0, len(def.loopsOf["j"]))
	h = requireOrderIndependent(t, def, handle, 40)
	require.Equal(t, StatusCompleted, h.st.Status, "%v", h.st.Error)
	require.Equal(t, 1, countDispatches(h, "z"))
}

// A join can be the head of its loop: each firing starts a pass.
func TestJoinAsLoopHead(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"s","action":"x","next":["a","b"]},
	 {"name":"a","action":"x","next":"j"},
	 {"name":"b","action":"x","next":"j"},
	 {"name":"j","join":[],"max_visits":3,"next":"c"},
	 {"name":"c","action":"x","next":["j2",{"step":"d","when":"c.again"}]},
	 {"name":"d","action":"x","next":"j"},
	 {"name":"j2","join":["c"],"next":[]}
	]}`)
	require.Equal(t, []string{"j"}, def.loopsOf["c"][0].heads)
	handle := func(c Command) result { return ok(map[string]any{"again": !laterVisit(c)}) }
	h := requireOrderIndependent(t, def, handle, 30)
	require.Equal(t, StatusCompleted, h.st.Status, "%v", h.st.Error)
	require.Equal(t, 2, countDispatches(h, "c"))
}

// Loops and their back edges come from the graph alone: listing steps or
// edges in another order finds the same ones.
func TestLoopsIgnoreListingOrder(t *testing.T) {
	docs := []string{
		`{"steps":[
		 {"name":"s","action":"x","next":["a",{"step":"b","when":"false"}]},
		 {"name":"a","action":"x","max_visits":2,"next":["b","out"]},
		 {"name":"b","action":"x","next":["a","c"]},
		 {"name":"c","action":"x","max_visits":2,"next":"c2"},
		 {"name":"c2","action":"x","next":["c","b"]},
		 {"name":"out","action":"x"}]}`,
		`{"steps":[
		 {"name":"s","action":"x","match":"all","next":[{"step":"b","when":"false"},"a"]},
		 {"name":"out","action":"x"},
		 {"name":"c2","action":"x","next":["b","c"]},
		 {"name":"b","action":"x","next":["c","a"]},
		 {"name":"c","action":"x","max_visits":2,"next":"c2"},
		 {"name":"a","action":"x","max_visits":2,"next":["out","b"]}]}`,
	}
	var want map[[2]string]bool
	for i, doc := range docs {
		// The second document starts at s too; Load needs it first.
		def := mustLoad(t, doc)
		if i == 0 {
			want = def.back
			require.Equal(t, map[[2]string]bool{
				{"a", "b"}: true, {"b", "a"}: true, {"c2", "b"}: true, {"c2", "c"}: true,
			}, want)
			continue
		}
		require.Equal(t, want, def.back)
	}
}

func TestValidateJoinLoopRules(t *testing.T) {
	// A join in a loop that can be entered at two steps.
	problems := strings.Join(validationProblems(t, `{"steps":[
	 {"name":"s","action":"x","next":["a",{"step":"b","when":"false"}]},
	 {"name":"a","action":"x","max_visits":2,"next":["j"]},
	 {"name":"b","action":"x","next":["j"]},
	 {"name":"j","join":[],"next":[{"step":"a","when":"true"},{"step":"b","when":"true"}]}
	]}`, testKinds), "\n")
	require.Contains(t, problems, "j: join is in a loop entered at more than one step (a, b)")

	// A join naming a step it only follows through the loop head.
	problems = strings.Join(validationProblems(t, `{"steps":[
	 {"name":"h","action":"x","max_visits":2,"next":["a"]},
	 {"name":"a","action":"x","next":["j"]},
	 {"name":"j","join":["after"],"next":["after"]},
	 {"name":"after","action":"x","next":[{"step":"h","when":"true"}]}
	]}`, testKinds), "\n")
	require.Contains(t, problems, "j: join: step after is only upstream of this join through the head of a loop")
}
