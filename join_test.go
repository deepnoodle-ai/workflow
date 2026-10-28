package workflow

import (
	"math/rand"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// Regression tests for join merging, found in review.

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
	 {"name":"out","set":"${s.n}"}
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
