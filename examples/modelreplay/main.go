// Command modelreplay tests a new decision model against saved runs
// before you ship it. It routes a batch of support tickets with model A
// and records each run's events. Then it replays each recording through
// Apply, asks model B each decision again, and reports the runs that
// would take a different path.
//
// This works because a decision is a recorded value at a known point in
// the run, and Apply is pure: the same events give the same run, so a
// changed answer gives the new route. The replay stops at the first
// divergence, because the steps after it never ran.
//
//	go run ./examples/modelreplay
package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

var (
	//go:embed workflow.json
	doc []byte
	//go:embed tickets.json
	ticketsDoc []byte
)

var start = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// A ticket is a support request, and how likely each model finds that it
// needs a reply within an hour.
type ticket struct {
	Body   string  `json:"body"`
	ModelA float64 `json:"model_a"`
	ModelB float64 `json:"model_b"`
}

var tickets = map[string]ticket{}

// A model answers a decide question about a subject with the chance of yes.
type model func(question, subject string) float64

func modelA(question, subject string) float64 { return tickets[subject].ModelA }

func modelB(question, subject string) float64 { return tickets[subject].ModelB }

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil, workflow.KindDecision: nil})
	if err != nil {
		log.Fatal(err)
	}
	var batch []ticket
	if err := json.Unmarshal(ticketsDoc, &batch); err != nil {
		log.Fatal(err)
	}

	// Record each run with model A, replay it with model B, and group the
	// runs that diverge by their old and new route.
	same, changed := 0, map[string][]string{}
	for i, t := range batch {
		tickets[t.Body] = t
		at, was, now := replay(def, record(def, t.Body), modelB)
		if at == "" {
			same++
			continue
		}
		route := fmt.Sprintf("would go to %s, not %s", now, was)
		line := fmt.Sprintf("ticket-%02d diverges at %s: %q", i+1, at, t.Body)
		changed[route] = append(changed[route], line)
	}

	fmt.Printf("%d runs checked against model B\n", len(batch))
	fmt.Printf("%4d would take the same path\n", same)
	for _, route := range slices.Sorted(maps.Keys(changed)) {
		fmt.Printf("%4d %s\n", len(changed[route]), route)
		for _, line := range changed[route] {
			fmt.Printf("       %s\n", line)
		}
	}
}

// answer is a decision job's output: model m's answer to each question.
// The job's params hold each question and its subject.
func answer(m model, params map[string]any) map[string]any {
	answers := map[string]any{}
	for _, q := range params["questions"].([]any) {
		q := q.(map[string]any)
		answers[q["id"].(string)] = m(q["question"].(string), q["subject"].(string))
	}
	return answers
}

// record routes one ticket with model A and returns the run's events.
func record(def *workflow.Definition, body string) []workflow.Event {
	events := []workflow.Event{{At: start, Kind: workflow.EventStart}}
	st, cmds, err := workflow.Apply(def, nil, events[0])
	for len(cmds) > 0 && err == nil {
		c := cmds[0]
		cmds = cmds[1:]
		if c.Kind != workflow.CommandDispatchStep {
			continue
		}
		var output any = map[string]any{"ok": true}
		switch {
		case c.StepKind == workflow.KindDecision:
			output = answer(modelA, c.Params)
		case c.Step == "intake":
			output = map[string]any{"body": body}
		}
		ev := workflow.Event{
			At:     start.Add(time.Duration(len(events)) * time.Second),
			Kind:   workflow.EventStepCompleted,
			StepID: c.StepID,
			Output: output,
		}
		events = append(events, ev)
		var more []workflow.Command
		st, more, err = workflow.Apply(def, st, ev)
		cmds = append(cmds, more...)
	}
	if err != nil {
		log.Fatal(err)
	}
	return events
}

// replay applies a recorded run's events in order and asks model m each
// decision again. At the first decision whose new answers send the run
// to other steps, it returns the deciding step, the steps the run went to
// and the steps it would go to now. It returns "" if the path is the same.
func replay(def *workflow.Definition, events []workflow.Event, m model) (at, was, now string) {
	var st *workflow.State
	asked := map[workflow.StepID]workflow.Command{}
	for _, ev := range events {
		if c, ok := asked[ev.StepID]; ok && ev.Kind == workflow.EventStepCompleted {
			alt := ev
			alt.Output = answer(m, c.Params)
			// Apply does not change st, so both events can apply to it.
			was, now = dispatched(def, st, ev), dispatched(def, st, alt)
			if was != now {
				return c.Step, was, now
			}
		}
		var cmds []workflow.Command
		var err error
		st, cmds, err = workflow.Apply(def, st, ev)
		if err != nil {
			log.Fatal(err)
		}
		for _, c := range cmds {
			if c.Kind == workflow.CommandDispatchStep && c.StepKind == workflow.KindDecision {
				asked[c.StepID] = c
			}
		}
	}
	return "", "", ""
}

// dispatched applies ev to st and returns the steps Apply dispatches.
func dispatched(def *workflow.Definition, st *workflow.State, ev workflow.Event) string {
	_, cmds, err := workflow.Apply(def, st, ev)
	if err != nil {
		log.Fatal(err)
	}
	var steps []string
	for _, c := range cmds {
		if c.Kind == workflow.CommandDispatchStep {
			steps = append(steps, c.Step)
		}
	}
	return strings.Join(steps, ", ")
}
