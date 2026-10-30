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
	//go:embed models.json
	modelsDoc []byte
)

var start = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

// A model is its answer for each subject: how likely the ticket needs a
// reply within an hour. models.json holds model A, in production, and
// model B, the candidate, for last week's tickets.
type model map[string]float64

func main() {
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil, workflow.KindDecision: nil})
	if err != nil {
		log.Fatal(err)
	}
	var models map[string]model
	if err := json.Unmarshal(modelsDoc, &models); err != nil {
		log.Fatal(err)
	}
	tickets := slices.Sorted(maps.Keys(models["model_a"]))

	// Record each run with model A, replay it with model B, and group the
	// runs that diverge by their old and new route.
	same, changed := 0, map[string][]string{}
	for _, body := range tickets {
		saved, _, _, _ := run(def, body, models["model_a"], nil)
		_, at, was, now := run(def, body, models["model_b"], saved)
		if at == "" {
			same++
			continue
		}
		route := fmt.Sprintf("would go to %s, not %s", now, was)
		if now == "" {
			route = fmt.Sprintf("would end, not go to %s", was)
		}
		if was == "page_oncall" {
			route += "   <- look at these"
		}
		changed[route] = append(changed[route], fmt.Sprintf("at %s: %q", at, body))
	}

	fmt.Printf("%d runs checked against model B\n%4d would take the same path\n", len(tickets), same)
	for _, route := range slices.Sorted(maps.Keys(changed)) {
		lines := strings.Join(changed[route], "\n       ")
		fmt.Printf("%4d %s\n       %s\n", len(changed[route]), route, lines)
	}
}

// run plays the host for one ticket and answers each decision with
// model m. It returns the run's events. Given a saved run, it replays it
// instead: each output comes from the saved events, and at the first
// decision where m sends the run elsewhere, it stops and returns the
// deciding step, the steps the saved run went to and the steps m picks.
func run(
	def *workflow.Definition, body string, m model, saved []workflow.Event,
) (events []workflow.Event, at, was, now string) {
	events = []workflow.Event{{At: start, Kind: workflow.EventStart}}
	st, cmds, err := workflow.Apply(def, nil, events[0])
	for ; len(cmds) > 0 && err == nil; cmds = cmds[1:] {
		c := cmds[0]
		if c.Kind != workflow.CommandDispatchStep {
			continue
		}
		t := start.Add(time.Duration(len(events)) * time.Second)
		ev := workflow.Event{At: t, Kind: workflow.EventStepCompleted, StepID: c.StepID}
		// Check the kind, not the step: a decision job's Step is "intake"
		// too, the step whose next block asks the question.
		if c.StepKind != workflow.KindDecision {
			ev.Output = map[string]any{"body": body}
		} else {
			// The job's params hold each question and its subject.
			answers := map[string]any{}
			for _, q := range c.Params["questions"].([]any) {
				q := q.(map[string]any)
				answers[q["id"].(string)] = m[q["subject"].(string)]
			}
			ev.Output = answers
		}
		if saved != nil {
			old := saved[len(events)]
			// Apply does not change st, so both events can apply to it.
			was, now = dispatched(def, st, old), dispatched(def, st, ev)
			if was != now {
				return events, c.Step, was, now
			}
			ev = old
		}
		events = append(events, ev)
		var more []workflow.Command
		st, more, err = workflow.Apply(def, st, ev)
		cmds = append(cmds, more...)
	}
	if err != nil {
		log.Fatal(err)
	}
	return events, "", "", ""
}

// dispatched returns the steps that applying ev to st dispatches.
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
