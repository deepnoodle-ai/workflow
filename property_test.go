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
// with conditional edges, nested joins and each steps with concurrency caps.
type graphGen struct {
	rng   *rand.Rand
	steps []map[string]any
	n     int
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

// block appends a sub-graph and returns its first and last steps.
func (g *graphGen) block(depth int) (first, last map[string]any) {
	switch r := g.rng.Intn(10); {
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
				f, l := g.block(depth - 2)
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
		a1, a2 := g.block(depth - 1)
		b1, b2 := g.block(depth - 1)
		link(a2, b1["name"].(string), "")
		return a1, b2
	default:
		fork := g.add(map[string]any{"name": g.name("f"), "task": "x"})
		branches := 2 + g.rng.Intn(3)
		var lasts []string
		conditional := false
		type br struct{ first, last map[string]any }
		var bs []br
		for i := 0; i < branches; i++ {
			f, l := g.block(depth - 1)
			bs = append(bs, br{f, l})
		}
		join := map[string]any{"name": g.name("j")}
		for i, b := range bs {
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
	g := &graphGen{rng: rand.New(rand.NewSource(seed))}
	start := g.add(map[string]any{"name": "start", "task": "x"})
	first, _ := g.block(4)
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

func deterministicHandler(c Command) result {
	h := fnv.New32a()
	h.Write([]byte(c.Step))
	fmt.Fprint(h, c.Index)
	return ok(map[string]any{"v": float64(h.Sum32() % 7), "step": c.Step})
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

		for order := int64(0); order < 40; order++ {
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
