package workflow

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

// graphGen builds random structured workflows: sequences, parallel forks
// with conditional edges, nested joins, each steps with concurrency caps,
// and bounded loops. Loops wrap any block, so joins sit inside loops,
// downstream of them and around them. A fork branch may also jump back to
// the enclosing loop's head or to the fork itself without passing the
// join, so back edges cross fork/join pairs and passes overlap.
//
// Loop conditions read `again`, which deterministicHandler sets on a
// step's first visit on a lineage only, so every loop ends.
type graphGen struct {
	rng   *rand.Rand
	steps []map[string]any
	n     int
	// loops weighs how often a block is a loop, and cross is one in how
	// many fork branches jump back.
	loops, cross int
	// forkBodies makes half of all loop bodies a fork and a join.
	forkBodies bool
	// crossed records heads a branch already jumps back to; one each keeps
	// the number of paths small.
	crossed map[string]bool
}

func (g *graphGen) name(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

func (g *graphGen) add(step map[string]any) map[string]any {
	g.steps = append(g.steps, step)
	return step
}

func link(from map[string]any, to string, when string) {
	edges, _ := from["next"].([]any)
	if when == "" {
		edges = append(edges, to)
	} else {
		edges = append(edges, map[string]any{"step": to, "when": when})
	}
	from["next"] = edges
}

// loopBound is max_visits for loop heads: enough for every pass the
// handler asks for.
const loopBound = 12

// block appends a sub-graph and returns its first and last steps. head is
// the innermost enclosing loop head, or "".
func (g *graphGen) block(depth int, head string) (first, last map[string]any) {
	switch r := g.rng.Intn(10 + g.loops); {
	case depth <= 0 || r < 3:
		s := g.add(map[string]any{"name": g.name("t"), "task": "x"})
		return s, s
	case r < 5:
		s := g.add(map[string]any{
			"name": g.name("e"), "task": "x",
			"each":        fmt.Sprintf("[%s]", joinInts(g.rng.Intn(5))),
			"with":        map[string]any{"item": "${{ item }}"},
			"concurrency": float64(g.rng.Intn(3)),
		})
		return s, s
	case r < 6:
		// One fork feeding two joins, which merge again: each join takes
		// part of the fork.
		fork := g.add(map[string]any{"name": g.name("f"), "task": "x"})
		outer := map[string]any{"name": g.name("j"), "join": []any{}}
		for half := 0; half < 2; half++ {
			inner := map[string]any{"name": g.name("j")}
			var named []any
			for i := 0; i < 2; i++ {
				f, l := g.block(depth-2, head)
				link(fork, f["name"].(string), "")
				link(l, inner["name"].(string), "")
				named = append(named, l["name"])
			}
			inner["join"] = named
			g.add(inner)
			link(inner, outer["name"].(string), "")
		}
		g.add(outer)
		return fork, outer
	case r < 7:
		a1, a2 := g.block(depth-1, head)
		b1, b2 := g.block(depth-1, head)
		link(a2, b1["name"].(string), "")
		return a1, b2
	case r < 7+g.loops:
		// A loop: head, body, and a tail that goes back while again.
		h := g.add(map[string]any{"name": g.name("h"), "task": "x", "max_visits": float64(loopBound)})
		var f, l map[string]any
		if g.forkBodies && g.rng.Intn(2) == 0 {
			f, l = g.forkBlock(depth-1, h["name"].(string))
		} else {
			f, l = g.block(depth-2, h["name"].(string))
		}
		link(h, f["name"].(string), "")
		tail := g.add(map[string]any{"name": g.name("c"), "task": "x", "match": "first"})
		link(l, tail["name"].(string), "")
		link(tail, h["name"].(string), tail["name"].(string)+".again")
		return h, tail
	default:
		return g.forkBlock(depth, head)
	}
}

// forkBlock appends a fork into 2 to 4 branches and a join after them.
func (g *graphGen) forkBlock(depth int, head string) (first, last map[string]any) {
	fork := g.add(map[string]any{"name": g.name("f"), "task": "x"})
	branches := 2 + g.rng.Intn(3)
	var lasts []string
	conditional := false
	type br struct{ first, last map[string]any }
	var bs []br
	for i := 0; i < branches; i++ {
		f, l := g.block(depth-1, head)
		bs = append(bs, br{f, l})
	}
	join := map[string]any{"name": g.name("j")}
	for i, b := range bs {
		target := head
		if target == "" || g.crossed[target] || g.rng.Intn(2) == 0 {
			target = fork["name"].(string)
		}
		if g.cross > 0 && !g.crossed[target] && g.rng.Intn(g.cross) == 0 {
			// This branch may go back without reaching the join:
			// to the enclosing loop's head, or to the fork.
			if g.crossed == nil {
				g.crossed = map[string]bool{}
			}
			g.crossed[target] = true
			if target == fork["name"] {
				fork["max_visits"] = float64(loopBound)
			}
			x := g.add(map[string]any{"name": g.name("x"), "task": "x", "match": "first"})
			link(b.last, x["name"].(string), "")
			link(x, target, x["name"].(string)+".again")
			b.last = x
			conditional = true
		}
		when := ""
		if i > 0 && g.rng.Intn(3) == 0 {
			// Outputs are deterministic per step name, so the
			// condition is too.
			when = fmt.Sprintf("%s.v %% 2 == %d", fork["name"], g.rng.Intn(2))
			conditional = true
		}
		link(fork, b.first["name"].(string), when)
		link(b.last, join["name"].(string), "")
		lasts = append(lasts, b.last["name"].(string))
	}
	if conditional {
		join["join"] = []any{}
	} else {
		named := make([]any, len(lasts))
		for i, l := range lasts {
			named[i] = l
		}
		join["join"] = named
	}
	g.add(join)
	return fork, join
}

func joinInts(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprint(i)
	}
	return out
}

func randomWorkflow(t *testing.T, seed int64) *Definition {
	return generate(t, &graphGen{rng: rand.New(rand.NewSource(seed)), loops: 1, cross: 8}, 4)
}

// randomLoopWorkflow builds smaller graphs where loops and back edges
// across fork/join pairs are common.
func randomLoopWorkflow(t *testing.T, seed int64) *Definition {
	return generate(t, &graphGen{rng: rand.New(rand.NewSource(seed)), loops: 3, cross: 3, forkBodies: true}, 3)
}

func generate(t *testing.T, g *graphGen, depth int) *Definition {
	start := g.add(map[string]any{"name": "start", "task": "x"})
	first, _ := g.block(depth, "")
	link(start, first["name"].(string), "")
	steps := make([]any, len(g.steps))
	for i, s := range g.steps {
		steps[i] = s
	}
	data, err := json.Marshal(map[string]any{"steps": steps})
	require.NoError(t, err)
	def, err := Load(data, testKinds)
	require.NoError(t, err, string(data))
	return def
}

// ordersFor scales how many shuffled orders a graph gets to its size: the
// fake host round-trips the whole state on every event, so a run costs the
// square of its length. The largest graphs still get a few orders.
func ordersFor(ref *fakeHost, most int) int {
	n := len(ref.all)
	return min(most, max(3, 150000/(n*n)))
}

func deterministicHandler(c Command) result {
	h := fnv.New32a()
	h.Write([]byte(c.Step))
	fmt.Fprint(h, c.Index)
	return ok(map[string]any{"v": float64(h.Sum32() % 7), "step": c.Step, "again": !laterVisit(c)})
}

// TestShuffledCompletionsRandomGraphs applies completions in many orders
// over many random graphs and checks every order reaches the same state and
// the same set of dispatches.
func TestShuffledCompletionsRandomGraphs(t *testing.T) {
	for graph := int64(0); graph < 60; graph++ {
		def := randomWorkflow(t, graph)
		ref := newHost(t, def, deterministicHandler)
		ref.start(nil, nil)
		ref.runToEnd(nil)
		require.Equal(t, StatusCompleted, ref.st.Status, "graph %d", graph)
		want := mustJSON(t, ref.st)
		wantDispatched := ref.dispatched()

		for order := int64(0); order < int64(ordersFor(ref, 40)); order++ {
			h := newHost(t, def, deterministicHandler)
			h.start(nil, nil)
			h.runToEnd(rand.New(rand.NewSource(order*7919 + graph)))
			require.Equal(t, want, mustJSON(t, h.st), "graph %d order %d", graph, order)
			require.Equal(t, wantDispatched, h.dispatched(), "graph %d order %d", graph, order)
		}
	}
}

// TestNoStepRunsTwice checks across random graphs that no StepID is ever
// dispatched twice.
func TestNoStepRunsTwice(t *testing.T) {
	for graph := int64(100); graph < 160; graph++ {
		def := randomWorkflow(t, graph)
		h := newHost(t, def, deterministicHandler)
		h.start(nil, nil)
		h.runToEnd(rand.New(rand.NewSource(graph)))
		seen := map[StepID]bool{}
		for _, id := range h.dispatched() {
			require.False(t, seen[id], "graph %d dispatched %s twice", graph, id)
			seen[id] = true
		}
	}
}

// TestShuffledCompletionsLoopGraphs is the order-independence check for
// loops: joins inside, downstream of and around bounded loops, and branches
// that go back to a loop head without passing their join.
func TestShuffledCompletionsLoopGraphs(t *testing.T) {
	loopy, joinInLoop := 0, 0
	const graphs = 40
	for graph := int64(0); graph < graphs; graph++ {
		def := randomLoopWorkflow(t, graph)
		if len(def.back) > 0 {
			loopy++
		}
		for _, s := range def.Steps {
			if s.Kind == KindJoin && len(def.loopsOf[s.Name]) > 0 {
				joinInLoop++
				break
			}
		}
		ref := newHost(t, def, deterministicHandler)
		ref.start(nil, nil)
		ref.runToEnd(nil)
		require.Equal(t, StatusCompleted, ref.st.Status, "graph %d: %v", graph, ref.st.Error)
		want := mustJSON(t, ref.st)
		wantDispatched := ref.dispatched()
		seen := map[StepID]bool{}
		for _, id := range wantDispatched {
			require.False(t, seen[id], "graph %d dispatched %s twice", graph, id)
			seen[id] = true
		}
		for order := int64(0); order < int64(ordersFor(ref, 20)); order++ {
			h := newHost(t, def, deterministicHandler)
			h.start(nil, nil)
			h.runToEnd(rand.New(rand.NewSource(order*7919 + graph)))
			require.Equal(t, wantDispatched, h.dispatched(), "graph %d order %d", graph, order)
			require.Equal(t, want, mustJSON(t, h.st), "graph %d order %d", graph, order)
		}
	}
	t.Logf("%d of %d graphs have loops, %d a join inside one", loopy, graphs, joinInLoop)
	require.True(t, loopy >= graphs/2 && joinInLoop >= graphs/4, "too few loops: %d, joins in loops: %d", loopy, joinInLoop)
}

// TestLoopGraphsIgnoreStepOrder lists the steps of random loop graphs in
// another order (the start step stays first) and checks the run is the
// same: loops, joins and passes depend only on the graph.
func TestLoopGraphsIgnoreStepOrder(t *testing.T) {
	for graph := int64(0); graph < 40; graph++ {
		def := randomLoopWorkflow(t, graph)
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(mustJSON(t, def)), &doc))
		steps := doc["steps"].([]any)
		rng := rand.New(rand.NewSource(graph))
		rng.Shuffle(len(steps)-1, func(i, j int) { steps[i+1], steps[j+1] = steps[j+1], steps[i+1] })
		data, err := json.Marshal(doc)
		require.NoError(t, err)
		other, err := Load(data, testKinds)
		require.NoError(t, err)
		require.Equal(t, def.back, other.back, "graph %d", graph)

		run := func(d *Definition) string {
			h := newHost(t, d, deterministicHandler)
			h.start(nil, nil)
			h.runToEnd(nil)
			st := *h.st
			st.Definition = ""
			return mustJSON(t, &st)
		}
		require.Equal(t, run(def), run(other), "graph %d", graph)
	}
}
