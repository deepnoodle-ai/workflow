// Command planner shows an agent that extends a run with work it plans as
// it goes. The plan step's output is a workflow document; the execute step
// loads it and runs it as a child run. The parent graph never changes, so
// everything Load proved about it still holds.
//
// The child is loaded with workflow.RequireOutputs(job.OutputSchema), so
// a child whose outputs cannot fill the execute step fails to load. That
// failure is caught and routed back to plan, which reads it as feedback:
// look at round 1. Each later round reads the last child's summary, and
// the loop ends when a child reports done.
//
//	go run ./examples/planner
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var parentDoc []byte

// plans are the fake model's workflows, one per round. The first declares
// no summary and a done that is a string.
var plans = []string{
	`{"steps": [{"name": "look", "agent": "investigator"}],
	  "outputs": {"done": "not yet"}}`,
	`{"steps": [{"name": "reproduce", "action": "run_tests"}],
	  "outputs": {"done": false, "summary": "${{ reproduce.cause }}"}}`,
	`{"steps": [
	   {"name": "fix", "agent": "coder", "next": "verify",
	    "with": {"cause": "two requests race on the cart cache"}},
	   {"name": "verify", "action": "run_tests", "output": {"type": "object",
	    "required": ["passed"], "properties": {"passed": {"type": "boolean"}}}}],
	  "outputs": {"done": "${{ verify.passed }}", "summary": "Fixed the race in the cart cache."}}`,
}

func main() {
	kinds := workflow.KindMap{"agent": nil, "action": nil}
	parent, err := workflow.Load(parentDoc, kinds)
	if err != nil {
		log.Fatal(err)
	}

	round := 0
	planner := func(ctx context.Context, job workflow.Job) (any, error) {
		last, _ := json.Marshal(job.Params["last"])
		fmt.Printf("== round %d: plan reads %s\n", round+1, last)
		var doc map[string]any
		if err := json.Unmarshal([]byte(plans[round]), &doc); err != nil {
			return nil, err
		}
		round++
		return map[string]any{"workflow": doc}, nil
	}

	var runner *local.Runner
	runWorkflow := func(ctx context.Context, job workflow.Job) (any, error) {
		doc, _ := job.Params["workflow"].(map[string]any)
		child, err := workflow.LoadMap(doc, kinds, workflow.RequireOutputs(job.OutputSchema))
		if err != nil {
			fmt.Printf("   rejected: %v\n", err)
			return nil, &workflow.StepError{Type: "invalid_workflow", Message: err.Error()}
		}
		// The child's ID is stable across retries of this step.
		input, _ := job.Params["input"].(map[string]any)
		st, err := runner.Run(ctx, job.RunID+"/"+job.StepID.Unit(), child, input)
		if err != nil {
			return nil, err
		}
		if st.Status != workflow.StatusCompleted {
			return nil, &workflow.StepError{
				Type:    "child_" + string(st.Status),
				Message: fmt.Sprint(st.Error),
			}
		}
		out, _ := json.Marshal(st.Outputs)
		fmt.Printf("   child outputs %s\n", out)
		return st.Outputs, nil
	}

	work := func(ctx context.Context, job workflow.Job) (any, error) {
		fmt.Printf("   %-20s %s\n", job.RunID, job.StepID)
		switch job.Step {
		case "reproduce":
			return map[string]any{"cause": "two requests race on the cart cache"}, nil
		case "verify":
			return map[string]any{"passed": true}, nil
		}
		return map[string]any{}, nil
	}
	runner = &local.Runner{Handlers: map[string]workflow.Handler{
		"agent": func(ctx context.Context, job workflow.Job) (any, error) {
			if job.Step == "plan" {
				return planner(ctx, job)
			}
			return work(ctx, job)
		},
		"action": func(ctx context.Context, job workflow.Job) (any, error) {
			if job.Step == "execute" {
				return runWorkflow(ctx, job)
			}
			return work(ctx, job)
		},
	}}

	st, err := runner.Run(context.Background(), "bug-7", parent,
		map[string]any{"goal": "Fix the flaky checkout test"})
	if err != nil {
		log.Fatal(err)
	}
	out, _ := json.Marshal(st.Outputs)
	fmt.Printf("== run %s: %s\n", st.Status, out)
}
