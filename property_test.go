package workflow

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strings"
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
		if _, set := from["match"]; !set {
			// Edges may mix in any order; follow every match.
			from["match"] = "all"
		}
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
		s := g.add(map[string]any{"name": g.name("t"), "action": "x"})
		return s, s
	case r < 5:
		s := g.add(map[string]any{
			"name": g.name("e"), "action": "x",
			"each":        fmt.Sprintf("[%s]", joinInts(g.rng.Intn(5))),
			"with":        map[string]any{"item": "${{ item }}"},
			"concurrency": float64(g.rng.Intn(3)),
		})
		return s, s
	case r < 6:
		// One fork feeding two joins, which merge again: each join takes
		// part of the fork.
		fork := g.add(map[string]any{"name": g.name("f"), "action": "x"})
		outer := map[string]any{"name": g.name("j"), "join": []any{}}
		for range 2 {
			inner := map[string]any{"name": g.name("j")}
			var named []any
			for range 2 {
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
		h := g.add(map[string]any{"name": g.name("h"), "action": "x", "max_visits": float64(loopBound)})
		var f, l map[string]any
		if g.forkBodies && g.rng.Intn(2) == 0 {
			f, l = g.forkBlock(depth-1, h["name"].(string))
		} else {
			f, l = g.block(depth-2, h["name"].(string))
		}
		link(h, f["name"].(string), "")
		tail := g.add(map[string]any{"name": g.name("c"), "action": "x", "match": "first"})
		link(l, tail["name"].(string), "")
		link(tail, h["name"].(string), tail["name"].(string)+".again")
		return h, tail
	default:
		return g.forkBlock(depth, head)
	}
}

// forkBlock appends a fork into 2 to 4 branches and a join after them.
func (g *graphGen) forkBlock(depth int, head string) (first, last map[string]any) {
	fork := g.add(map[string]any{"name": g.name("f"), "action": "x"})
	branches := 2 + g.rng.Intn(3)
	var lasts []string
	conditional := false
	type br struct{ first, last map[string]any }
	var bs []br
	for range branches {
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
			x := g.add(map[string]any{"name": g.name("x"), "action": "x", "match": "first"})
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
	for i := range n {
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
	return generate(t, &graphGen{
		rng:        rand.New(rand.NewSource(seed)),
		loops:      3,
		cross:      3,
		forkBodies: true,
	}, 3)
}

func generate(t *testing.T, g *graphGen, depth int) *Definition {
	return loadDoc(t, generateDoc(g, depth))
}

func generateDoc(g *graphGen, depth int) map[string]any {
	start := g.add(map[string]any{"name": "start", "action": "x"})
	first, _ := g.block(depth, "")
	link(start, first["name"].(string), "")
	steps := make([]any, len(g.steps))
	for i, s := range g.steps {
		steps[i] = s
	}
	return map[string]any{"steps": steps}
}

func loadDoc(t *testing.T, doc map[string]any) *Definition {
	data, err := json.Marshal(doc)
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
	t.Parallel()
	for graph := range int64(60) {
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
			def := randomWorkflow(t, graph)
			ref := newHost(t, def, deterministicHandler)
			ref.start(nil)
			ref.runToEnd(nil)
			require.Equal(t, StatusCompleted, ref.st.Status, "graph %d", graph)
			want := mustJSON(t, ref.st)
			wantDispatched := ref.dispatched()

			for order := int64(0); order < int64(ordersFor(ref, 40)); order++ {
				h := newHost(t, def, deterministicHandler)
				h.start(nil)
				h.runToEnd(rand.New(rand.NewSource(order*7919 + graph)))
				require.Equal(t, want, mustJSON(t, h.st), "graph %d order %d", graph, order)
				require.Equal(t, wantDispatched, h.dispatched(), "graph %d order %d", graph, order)
			}
		})
	}
}

// TestNoStepRunsTwice checks across random graphs that no StepID is ever
// dispatched twice.
func TestNoStepRunsTwice(t *testing.T) {
	t.Parallel()
	for graph := int64(100); graph < 160; graph++ {
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
			def := randomWorkflow(t, graph)
			h := newHost(t, def, deterministicHandler)
			h.start(nil)
			h.runToEnd(rand.New(rand.NewSource(graph)))
			seen := map[StepID]bool{}
			for _, id := range h.dispatched() {
				require.False(t, seen[id], "graph %d dispatched %s twice", graph, id)
				seen[id] = true
			}
		})
	}
}

// TestShuffledCompletionsLoopGraphs is the order-independence check for
// loops: joins inside, downstream of and around bounded loops, and branches
// that go back to a loop head without passing their join.
func TestShuffledCompletionsLoopGraphs(t *testing.T) {
	t.Parallel()
	loopy, joinInLoop := 0, 0
	const graphs = 40
	for graph := range int64(graphs) {
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
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
			ref := newHost(t, def, deterministicHandler)
			ref.start(nil)
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
				h.start(nil)
				h.runToEnd(rand.New(rand.NewSource(order*7919 + graph)))
				require.Equal(t, wantDispatched, h.dispatched(), "graph %d order %d", graph, order)
				require.Equal(t, want, mustJSON(t, h.st), "graph %d order %d", graph, order)
			}
		})
	}
	t.Logf("%d of %d graphs have loops, %d a join inside one", loopy, graphs, joinInLoop)
	require.True(t, loopy >= graphs/2 && joinInLoop >= graphs/4,
		"too few loops: %d, joins in loops: %d", loopy, joinInLoop)
}

// TestLoopGraphsIgnoreStepOrder lists the steps of random loop graphs in
// another order (the start step stays first) and checks the run is the
// same: loops, joins and passes depend only on the graph.
func TestLoopGraphsIgnoreStepOrder(t *testing.T) {
	t.Parallel()
	for graph := range int64(40) {
		t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
			t.Parallel()
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
				h.start(nil)
				h.runToEnd(nil)
				st := *h.st
				st.DefinitionHash = ""
				return mustJSON(t, &st)
			}
			require.Equal(t, run(def), run(other), "graph %d", graph)
		})
	}
}

// TestShuffledCompletionsWithFinally checks that the finally phase is
// independent of completion order over random graphs, for each outcome.
// A completed run reaches the same state in every order. A failed or
// cancelled run cancels whatever was in flight, which depends on the
// order, but its finally steps see the same outcome and do the same
// things: the same dispatches with the same params, the same failures, and
// the same terminal command.
func TestShuffledCompletionsWithFinally(t *testing.T) {
	t.Parallel()
	const graphs = 30
	outcomes := map[RunStatus]int{}
	graphOutcomes := make([]map[RunStatus]int, graphs)
	// Wait for every graph before checking coverage of the three outcomes.
	t.Run("graphs", func(t *testing.T) {
		for graph := range int64(graphs) {
			t.Run(fmt.Sprintf("graph_%d", graph), func(t *testing.T) {
				t.Parallel()
				outcomes := map[RunStatus]int{}
				graphOutcomes[graph] = outcomes
				doc := generateDoc(&graphGen{
					rng:        rand.New(rand.NewSource(graph)),
					loops:      2,
					cross:      6,
					forkBodies: true,
				}, 3)
				doc["on_failure"] = []any{map[string]any{"name": "report", "action": "x",
					"with": map[string]any{"step": "${{ error.step }}", "type": "${{ error.type }}"}}}
				doc["finally"] = []any{
					map[string]any{"name": "release", "action": "x", "with": map[string]any{
						"status": "${{ run.status }}", "failed": "${{ run.error?.step }}", "start": "${{ start?.v }}",
						"report": "${{ report?.step }}"}},
					map[string]any{"name": "notify", "action": "x", "with": map[string]any{
						"released": "${{ release?.error?.type }}", "status": "${{ run.status }}"}},
				}
				def := loadDoc(t, doc)
				var tasks []string
				for _, s := range def.Steps[1:] {
					if s.isHostKind() {
						tasks = append(tasks, s.Name)
					}
				}
				failing := tasks[int(graph)%len(tasks)]

				for _, mode := range []string{"complete", "fail", "cancel"} {
					handler := func(c Command) result {
						switch {
						case c.Step == "release":
							return fail("cleanup", "release broke", false)
						case mode == "fail" && c.Step == failing:
							return fail("boom", c.Step+" broke", false)
						}
						return deterministicHandler(c)
					}
					run := func(rng *rand.Rand) *fakeHost {
						h := newHost(t, def, handler)
						h.start(nil)
						if mode == "cancel" {
							// Cancel after a few deliveries, as many as the order
							// picks; the first is always start, which the finally
							// steps read.
							n := 1
							if rng != nil {
								n = 1 + rng.Intn(6)
							}
							for i := 0; i < n && len(h.pending) > 0 && h.st.Status == StatusRunning; i++ {
								ids := h.pendingIDs()
								pick := ids[0]
								if rng != nil {
									pick = ids[rng.Intn(len(ids))]
								}
								h.deliver(pick)
							}
							if h.st.Status != StatusRunning {
								return nil // finished before the cancel
							}
							h.apply(Event{Kind: EventCancel, Reason: "stop"})
						}
						h.runToEnd(rng)
						return h
					}
					ref := run(nil)
					if ref == nil {
						continue
					}
					outcomes[ref.st.Status]++
					want, wantTrace := mustJSON(t, ref.st), finallyTrace(t, ref)
					for order := int64(0); order < int64(ordersFor(ref, 12)); order++ {
						h := run(rand.New(rand.NewSource(order*7919 + graph)))
						if h == nil {
							continue
						}
						require.Equal(t, ref.st.Status, h.st.Status, "graph %d %s order %d", graph, mode, order)
						require.Equal(t, wantTrace, finallyTrace(t, h), "graph %d %s order %d", graph, mode, order)
						if mode == "complete" {
							require.Equal(t, want, mustJSON(t, h.st), "graph %d order %d", graph, order)
						}
					}
				}
			})
		}
	})
	ran := 0
	for _, counts := range graphOutcomes {
		if counts != nil {
			ran++
		}
		for status, n := range counts {
			outcomes[status] += n
		}
	}
	t.Logf("outcomes: %v", outcomes)
	// A -run filter can select one graph without covering every outcome.
	if ran == graphs {
		require.True(t, outcomes[StatusCompleted] > 0 &&
			outcomes[StatusFailed] > 0 &&
			outcomes[StatusCancelled] > 0,
			"want every outcome: %v", outcomes)
	}
}

// finallyTrace is what a run's finally phase did: its dispatches with
// their params, the terminal command, and the finally failures.
func finallyTrace(t *testing.T, h *fakeHost) string {
	type entry struct {
		Kind   CommandKind
		Step   string
		StepID StepID
		Params map[string]any
	}
	var trace []entry
	for _, c := range h.all {
		if c.Kind == CommandDispatchStep && strings.HasPrefix(string(c.StepID), finallyPath+"/") {
			trace = append(trace, entry{c.Kind, c.Step, c.StepID, c.Params})
		}
	}
	require.NotNil(t, h.final)
	end := map[string]any{"kind": h.final.Kind, "outputs": h.final.Outputs, "reason": h.final.Reason}
	if e := h.final.Error; e != nil {
		end["error"] = []string{e.Step, e.Type}
	}
	var fes [][]string
	for _, fe := range h.final.FinallyErrors {
		fes = append(fes, []string{fe.Step, string(fe.StepID), fe.Type, fe.Message})
	}
	end["finally_errors"] = fes
	return mustJSON(t, map[string]any{"dispatched": trace, "end": end})
}
