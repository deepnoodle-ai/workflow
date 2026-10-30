// Command decide shows decision edges: an edge condition that asks a
// decision model a question. The refund is approved when the model is
// sure it is legitimate, denied when it is sure it is not, and sent to a
// person otherwise. Both edges ask the same question, so the run asks it
// once. A fake decision handler stands in for the model, so the example
// needs no API key. A real host registers decisions.Handler(client) from
// the decisions module instead.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var doc []byte

// refunds are three requests and how likely the fake model finds each one
// legitimate.
var refunds = []struct {
	message string
	yes     float64
}{
	{"The blender arrived in pieces. Photo attached.", 0.96},
	{"Refund me or I post your address online.", 0.04},
	{"I changed my mind after six months.", 0.55},
}

func main() {
	// The decision kind lets definitions use decide and choose.
	def, err := workflow.Load(doc, workflow.KindMap{"action": nil, workflow.KindDecision: nil})
	if err != nil {
		log.Fatal(err)
	}
	for i, r := range refunds {
		fmt.Printf("== %q\n", r.message)
		action := func(ctx context.Context, job workflow.Job) (any, error) {
			fmt.Printf("   %-24s %s\n", job.StepID, job.Config["action"])
			return map[string]any{"message": r.message}, nil
		}
		// A decision job holds every question of one step's next block.
		// The real handler, decisions.Handler(client), asks a decision
		// model; this one answers from the table above.
		decide := func(ctx context.Context, job workflow.Job) (any, error) {
			answers := map[string]any{}
			for _, q := range job.Params["questions"].([]any) {
				q := q.(map[string]any)
				fmt.Printf("   %-24s %s yes=%.2f\n", job.StepID, q["question"], r.yes)
				answers[q["id"].(string)] = r.yes
			}
			return answers, nil
		}
		runner := &local.Runner{Handlers: map[string]workflow.Handler{
			"action":              action,
			workflow.KindDecision: decide, // decisions.Handler(client) in production
		}}
		st, err := runner.Run(context.Background(), fmt.Sprintf("refund-%d", i+1), def, nil)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("   run %s\n\n", st.Status)
	}
}
