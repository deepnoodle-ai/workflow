package workflow

import (
	"math/rand"
	"sort"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// Regression tests for join merging, found in review.

// laterVisit reports whether a dispatch is a loop's second or later pass.
func laterVisit(c Command) bool { return strings.Contains(string(c.StepID), "@") }

// requireOrderIndependent runs def in sorted order and in many shuffled
// orders and requires the same final state and dispatches every time. It
// returns the reference host.
func requireOrderIndependent(t *testing.T, def *Definition, handle func(Command) result, seeds int) *fakeHost {
	t.Helper()
	ref := newHost(t, def, handle)
	ref.start(nil, nil)
	ref.runToEnd(nil)
	want, wantDispatched := mustJSON(t, ref.st), ref.dispatched()
	for seed := int64(0); seed < int64(seeds); seed++ {
		h := newHost(t, def, handle)
		h.start(nil, nil)
		h.runToEnd(rand.New(rand.NewSource(seed)))
		require.Equal(t, wantDispatched, h.dispatched(), "seed %d", seed)
		require.Equal(t, want, mustJSON(t, h.st), "seed %d", seed)
	}
	return ref
}

// nodesOf lists a step's nodes in the order they were entered.
func nodesOf(h *fakeHost, step string) []string {
	var out []string
	for _, c := range h.all {
		if c.Kind == CommandDispatchStep && c.Step == step {
			key, _, _, _ := c.StepID.parse()
			out = append(out, key)
		}
	}
	return out
}

func countDispatches(h *fakeHost, step string) int {
	n := 0
	for _, c := range h.all {
		if c.Kind == CommandDispatchStep && c.Step == step {
			n++
		}
	}
	return n
}

const twoJoinsDoc = `{"steps":[
 {"name":"start","task":"x","next":["a","b","c","d"]},
 {"name":"a","task":"x","next":"j1"},
 {"name":"b","task":"x","next":"j1"},
 {"name":"c","task":"x","next":"j2"},
 {"name":"d","task":"x","next":"j2"},
 {"name":"j1","join":["a","b"],"next":"fin"},
 {"name":"j2","join":["c","d"],"next":"fin"},
 {"name":"fin","task":"x"}
]}`

// Two joins that each merge part of one fork must not both continue on the
// fork's path: the second would reuse StepIDs the first already ran.
func TestPartialJoinsGetFreshPaths(t *testing.T) {
	def := mustLoad(t, twoJoinsDoc)
	orders := [][]StepID{
		{"main.a/a#1", "main.b/b#1", "main.j1/fin#1", "main.c/c#1", "main.d/d#1"},
		{"main.a/a#1", "main.b/b#1", "main.c/c#1", "main.d/d#1"},
		{"main.c/c#1", "main.a/a#1", "main.d/d#1", "main.b/b#1"},
	}
	var want string
	for _, order := range orders {
		h := newHost(t, def, func(c Command) result { return ok(string(c.StepID)) })
		h.start(nil, nil)
		h.deliver("main/start#1")
		for _, id := range order {
			h.deliver(id)
		}
		h.runToEnd(nil)
		require.Equal(t, StatusCompleted, h.st.Status)
		require.Equal(t, []StepID{
			"main.a/a#1", "main.b/b#1", "main.c/c#1", "main.d/d#1",
			"main.j1/fin#1", "main.j2/fin#1", "main/start#1",
		}, h.dispatched())
		if want == "" {
			want = mustJSON(t, h.st)
		}
		require.Equal(t, want, mustJSON(t, h.st))
	}
}

func TestPartialJoinsShuffled(t *testing.T) {
	def := mustLoad(t, twoJoinsDoc)
	ref := newHost(t, def, deterministicHandler)
	ref.start(nil, nil)
	ref.runToEnd(nil)
	for seed := int64(0); seed < 50; seed++ {
		h := newHost(t, def, deterministicHandler)
		h.start(nil, nil)
		h.runToEnd(rand.New(rand.NewSource(seed)))
		require.Equal(t, mustJSON(t, ref.st), mustJSON(t, h.st), "seed %d", seed)
	}
}

// After a loop, a join must see the latest visit of a step, not the first.
func TestMergedScopeKeepsLatestVisit(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"s","task":"x","max_visits":2,"next":["a","b"]},
	 {"name":"a","task":"x","next":"j"},
	 {"name":"b","task":"x","match":"first","max_visits":2,"next":[{"step":"s","when":"b.again"},{"step":"j"}]},
	 {"name":"j","join":[],"next":"out"},
	 {"name":"out","set":"${{ s.n }}"}
	]}`)
	sCount, bCount := 0, 0
	h := newHost(t, def, func(c Command) result {
		switch c.Step {
		case "s":
			sCount++
			return ok(map[string]any{"n": sCount})
		case "b":
			bCount++
			return ok(map[string]any{"again": bCount == 1})
		}
		return ok(nil)
	})
	h.start(nil, nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, 2, sCount)
	require.Equal(t, map[string]any{"out": 2.0}, h.st.Outputs)
}

// Joins nested inside a loop body must not count the loop's back edge as a
// way for one to reach the other.
func TestJoinsInsideLoop(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"start","task":"x","max_visits":3,"next":["a","b","c"]},
	 {"name":"a","task":"x","next":"j1"},
	 {"name":"b","task":"x","next":"j1"},
	 {"name":"c","task":"x","next":"k"},
	 {"name":"j1","join":["a","b"],"next":"m"},
	 {"name":"m","task":"x","next":"k"},
	 {"name":"k","join":["m","c"],"next":"check"},
	 {"name":"check","task":"x","next":[{"step":"start","when":"check.again"}]}
	]}`)
	for seed := int64(0); seed < 20; seed++ {
		checks := 0
		h := newHost(t, def, func(c Command) result {
			if c.Step == "check" {
				checks++
				return ok(map[string]any{"again": checks < 2})
			}
			return ok(nil)
		})
		h.start(nil, nil)
		h.runToEnd(rand.New(rand.NewSource(seed)))
		require.Equal(t, StatusCompleted, h.st.Status, "seed %d: %v", seed, h.st.Error)
		require.Equal(t, 2, checks)
	}
}

// A join may name a step that settled before an earlier join merged its
// path.
func TestJoinNamesStepMergedEarlier(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"start","task":"x","next":["a","b","c"]},
	 {"name":"a","task":"x","next":"j1"},
	 {"name":"b","task":"x","next":"j1"},
	 {"name":"c","task":"x","next":"j2"},
	 {"name":"j1","join":["a","b"],"next":"j2"},
	 {"name":"j2","join":["a","c"],"next":"done"},
	 {"name":"done","task":"x"}
	]}`)
	h := newHost(t, def, func(c Command) result { return ok(1) })
	h.start(nil, nil)
	h.runToEnd(nil)
	require.Equal(t, StatusCompleted, h.st.Status, "%v", h.st.Error)
	require.Equal(t, PathEnded, h.st.Paths["main"].Status)
	require.Equal(t, "main/done", h.st.Paths["main"].Node)
}

// Two joins that each take one branch of every inner fork must not both
// claim the outer fork's path: whichever fired first would get it.
func TestCrossedPartialJoinsDoNotRaceForAncestor(t *testing.T) {
	def := mustLoad(t, `{"steps":[
	 {"name":"start","task":"x","next":["c","d"]},
	 {"name":"c","task":"x","next":["cx","cy"]},
	 {"name":"d","task":"x","next":["dx","dy"]},
	 {"name":"cx","task":"x","next":"j1"},
	 {"name":"dx","task":"x","next":"j1"},
	 {"name":"cy","task":"x","next":"j2"},
	 {"name":"dy","task":"x","next":"j2"},
	 {"name":"j1","join":["cx","dx"],"next":"fin"},
	 {"name":"j2","join":["cy","dy"],"next":"fin"},
	 {"name":"fin","task":"x"}
	]}`)
	h := requireOrderIndependent(t, def, deterministicHandler, 40)
	require.Equal(t, StatusCompleted, h.st.Status)
	require.Equal(t, []string{"main.j1/fin", "main.j2/fin"}, sortedStrings(nodesOf(h, "fin")))
}

func sortedStrings(list []string) []string {
	out := append([]string(nil), list...)
	sort.Strings(out)
	return out
}
