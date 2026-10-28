// Command apply drives a workflow by hand, with no runner: it calls
// workflow.Apply for each event and plays the host itself. Between events
// the state goes through JSON, as it would through a database row.
//
// It shows the whole engine: Start returns commands, results come back as
// events in any order, and a duplicate event changes nothing.
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

//go:embed workflow.json
var doc []byte

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil})
	if err != nil {
		log.Fatal(err)
	}

	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	tick := func() time.Time {
		now = now.Add(time.Second)
		return now
	}

	// The run starts. Apply returns the first state and the commands to
	// carry out: dispatch lookup.
	st, cmds, err := workflow.Apply(def, nil, workflow.Event{
		At:    tick(),
		Kind:  workflow.EventStart,
		Input: map[string]any{"user": "u_123"},
	})
	if err != nil {
		log.Fatal(err)
	}
	show("start", st, cmds)

	// lookup finished. Its output fans out to email and slack.
	st, cmds = deliver(def, st, workflow.Event{
		At:     tick(),
		Kind:   workflow.EventStepCompleted,
		StepID: "main/lookup#1",
		Output: map[string]any{"email": "ada@example.com", "name": "Ada"},
	})
	show("lookup completed", st, cmds)

	// slack finishes before email. Order does not matter: the join waits
	// for both.
	st, cmds = deliver(def, st, workflow.Event{
		At:     tick(),
		Kind:   workflow.EventStepCompleted,
		StepID: "main.slack/slack#1",
		Output: map[string]any{"ts": "1730106000.000100"},
	})
	show("slack completed", st, cmds)

	// The same result, delivered again (at-least-once delivery). A
	// duplicate returns the same state and no commands.
	again, cmds, err := workflow.Apply(def, st, workflow.Event{
		At:     tick(),
		Kind:   workflow.EventStepCompleted,
		StepID: "main.slack/slack#1",
		Output: map[string]any{"ts": "1730106000.000100"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("== slack completed again: unchanged=%v, %d commands\n\n", again == st, len(cmds))

	st, cmds = deliver(def, st, workflow.Event{
		At:     tick(),
		Kind:   workflow.EventStepCompleted,
		StepID: "main.email/email#1",
		Output: map[string]any{"id": "msg_9"},
	})
	show("email completed", st, cmds)
}

// deliver applies one event to a state that was stored as JSON and loaded
// back, the way a host reads a run row, applies the event and writes the
// row and the commands in one transaction.
func deliver(
	def *workflow.Definition, st *workflow.State, ev workflow.Event,
) (*workflow.State, []workflow.Command) {
	row, err := json.Marshal(st)
	if err != nil {
		log.Fatal(err)
	}
	var loaded *workflow.State
	if err := json.Unmarshal(row, &loaded); err != nil {
		log.Fatal(err)
	}
	next, cmds, err := workflow.Apply(def, loaded, ev)
	if err != nil {
		log.Fatal(err)
	}
	return next, cmds
}

func show(title string, st *workflow.State, cmds []workflow.Command) {
	fmt.Printf("== %s: run %s, in flight %v\n", title, st.Status, st.InFlight())
	for _, c := range cmds {
		switch c.Kind {
		case workflow.CommandDispatchStep:
			params, _ := json.Marshal(c.Params)
			fmt.Printf("   %s %s %s %s\n", c.Kind, c.StepID, c.Config["action"], params)
		case workflow.CommandRunCompleted:
			fmt.Printf("   %s outputs=%v\n", c.Kind, c.Outputs)
		default:
			fmt.Printf("   %s %s\n", c.Kind, c.StepID)
		}
	}
	fmt.Println()
}
