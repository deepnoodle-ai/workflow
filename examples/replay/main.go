// Command replay is a time-travel debugger built on Apply's determinism:
// the same events in the same order give the same states and commands.
// It drives a run by hand, recording every event, then replays the
// recording one event at a time and prints the run after each.
//
// Then it swaps the completions of tests and lint, which were in flight
// together, and replays again: the final state and the dispatched steps
// are the same. That holds for successful completions only. A failure
// that arrives before a sibling's success fails the run before that
// success can dispatch anything, so moving a failure can change what ran.
//
// It is pure: no runner, no goroutines, no clock but the events' At.
//
//	go run ./examples/replay
package main

import (
	_ "embed"
	"fmt"
	"log"
	"reflect"
	"slices"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

//go:embed workflow.json
var doc []byte

var start = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}

	// Record: play the host, completing dispatched steps first in, first out.
	events := []workflow.Event{{At: start, Kind: workflow.EventStart}}
	st, cmds, err := workflow.Apply(def, nil, events[0])
	for len(cmds) > 0 && err == nil {
		c := cmds[0]
		cmds = cmds[1:]
		if c.Kind != workflow.CommandDispatchStep {
			continue
		}
		output := map[string]any{"ok": true}
		if c.Step == "merge" {
			output = map[string]any{"sha": "abc123"}
		}
		ev := workflow.Event{At: start.Add(time.Duration(len(events)) * time.Second),
			Kind: workflow.EventStepCompleted, StepID: c.StepID, Output: output}
		events = append(events, ev)
		var more []workflow.Command
		st, more, err = workflow.Apply(def, st, ev)
		cmds = append(cmds, more...)
	}
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("== replay")
	final, dispatched := replay(def, events, true)

	// Swap the completions of tests and lint, the two steps in flight
	// together after checkout.
	i := slices.IndexFunc(events, func(e workflow.Event) bool { return e.StepID.Step() == "tests" })
	j := slices.IndexFunc(events, func(e workflow.Event) bool { return e.StepID.Step() == "lint" })
	swapped := slices.Clone(events)
	swapped[i], swapped[j] = swapped[j], swapped[i]
	fmt.Printf("\n== replay with %s and %s swapped\n", events[i].StepID, events[j].StepID)
	final2, dispatched2 := replay(def, swapped, false)
	fmt.Printf("   status %s = %s, outputs %v = %v\n", final.Status, final2.Status, final.Outputs, final2.Outputs)
	fmt.Printf("   same steps dispatched: %v\n", reflect.DeepEqual(dispatched, dispatched2))
}

// replay applies events in order from nothing and returns the final state
// and the StepIDs dispatched, sorted.
func replay(def *workflow.Definition, events []workflow.Event, show bool) (*workflow.State, []workflow.StepID) {
	var st *workflow.State
	var dispatched []workflow.StepID
	for _, ev := range events {
		var cmds []workflow.Command
		var err error
		st, cmds, err = workflow.Apply(def, st, ev)
		if err != nil {
			log.Fatal(err)
		}
		if show {
			fmt.Printf("   %-14s %-22s -> %s, in flight %v\n", ev.Kind, ev.StepID, st.Status, st.InFlight())
		}
		for _, c := range cmds {
			if c.Kind == workflow.CommandDispatchStep {
				dispatched = append(dispatched, c.StepID)
			}
			if show {
				fmt.Printf("      %s %s\n", c.Kind, c.StepID)
			}
		}
	}
	slices.Sort(dispatched)
	return st, dispatched
}
