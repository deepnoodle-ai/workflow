package workflow

import (
	"fmt"
	"testing"
)

// Benchmarks of the work expressions add: Load types and compiles every
// expression, and Apply evaluates them. They use only what the engine's
// API offers, so they run on any expression language the engine uses.

// BenchmarkLoadPRReview loads the PR review definition: parsing, the graph
// checks, and typing every expression against the schemas.
func BenchmarkLoadPRReview(b *testing.B) {
	for b.Loop() {
		if _, err := Load([]byte(prReview), testKinds); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRunPRReview runs the PR review from start to end with Apply
// alone: no JSON round trips between events.
func BenchmarkRunPRReview(b *testing.B) {
	def, err := Load([]byte(prReview), testKinds)
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		benchRun(b, def, prInput, prHandler)
	}
}

// fanOutDoc fans out over the input items; each dispatch evaluates a with: of
// several templates, and a set step reduces the results.
const fanOutDoc = `{"input": {"type": "object", "required": ["items", "prefix"], "properties": {
	    "prefix": {"type": "string"},
	    "items": {"type": "array", "items": {"type": "object", "required": ["id", "score"],
	      "properties": {"id": {"type": "string"}, "score": {"type": "number"}}}}}},
	  "steps": [
	    {"name": "work", "action": "x", "each": "input.items",
	     "output": {"type": "object", "required": ["n"], "properties": {"n": {"type": "number"}}},
	     "with": {"id": "${{ item.id }}", "label": "${{ input.prefix }}-${{ item.id }}",
	              "double": "${{ item.score * 2 }}", "big": "${{ item.score > 10 }}"},
	     "next": ["total"]},
	    {"name": "total", "set": {"count": "${{ len(work) }}", "first": "${{ work[0].n }}"}}]}`

// BenchmarkRunEach100 runs a fan-out over 100 items: 400 template
// evaluations for with:, then a set step.
func BenchmarkRunEach100(b *testing.B) {
	def, err := Load([]byte(fanOutDoc), testKinds)
	if err != nil {
		b.Fatal(err)
	}
	items := make([]any, 100)
	for i := range items {
		items[i] = map[string]any{"id": fmt.Sprintf("item-%d", i), "score": float64(i)}
	}
	input := map[string]any{"prefix": "p", "items": items}
	handle := func(c Command) result { return ok(map[string]any{"n": 1}) }
	for b.Loop() {
		benchRun(b, def, input, handle)
	}
}

// benchRun drives a run to its end with Apply, completing every dispatch
// with handle in the order Apply emits them.
func benchRun(b *testing.B, def *Definition, input map[string]any, handle func(Command) result) {
	st, cmds, err := Apply(def, nil, Event{Kind: EventStart, Input: input, At: t0})
	if err != nil {
		b.Fatal(err)
	}
	var queue []Command
	for {
		for _, c := range cmds {
			switch c.Kind {
			case CommandDispatchStep:
				queue = append(queue, c)
			case CommandRunCompleted:
				return
			case CommandRunFailed, CommandRunCancelled:
				b.Fatalf("run ended: %+v", c)
			}
		}
		if len(queue) == 0 {
			b.Fatal("run stalled")
		}
		c := queue[0]
		queue = queue[1:]
		res := handle(c)
		ev := Event{Kind: EventStepCompleted, StepID: c.StepID, Output: res.output, At: t0}
		if res.err != nil {
			ev = Event{Kind: EventStepFailed, StepID: c.StepID, Error: res.err, At: t0}
		}
		if st, cmds, err = Apply(def, st, ev); err != nil {
			b.Fatal(err)
		}
	}
}
