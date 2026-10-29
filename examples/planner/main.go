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
	"embed"
	"encoding/json"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/local"
)

//go:embed workflow.json
var parentDoc []byte

// plans holds the fake model's workflows, one per round. The first has a
// done that is a string and no summary.
//
//go:embed plans
var plans embed.FS

var kinds = workflow.KindMap{"agent": nil, "action": nil}

// round counts the plans. The plan steps run one at a time.
var round int

func main() {
	parent, err := workflow.Load(parentDoc, kinds)
	if err != nil {
		log.Fatal(err)
	}

	var runner *local.Runner
	runner = &local.Runner{
		Handlers: map[string]workflow.Handler{
			"agent": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "plan" {
					return plan(job)
				}
				return work(job)
			},
			"action": func(ctx context.Context, job workflow.Job) (any, error) {
				if job.Step == "execute" {
					return runPlan(ctx, runner, job)
				}
				return work(job)
			},
		},
	}

	input := map[string]any{"goal": "Fix the flaky checkout test"}
	st, err := runner.Run(context.Background(), "bug-7", parent, input)
	if err != nil {
		log.Fatal(err)
	}

	out, _ := json.Marshal(st.Outputs)
	fmt.Printf("== run %s: %s\n", st.Status, out)
}

// plan is the fake model. It reads the last round's result, which is nil,
// the last child's outputs, or the error that rejected the last plan.
func plan(job workflow.Job) (any, error) {
	round++
	last, _ := json.Marshal(job.Params["last"])
	fmt.Printf("== round %d: plan reads %s\n", round, last)

	data, err := plans.ReadFile(fmt.Sprintf("plans/%d.json", round))
	if err != nil {
		return nil, err
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return map[string]any{"workflow": doc}, nil
}

// runPlan loads the plan against the execute step's output schema and runs
// it as a child run.
func runPlan(ctx context.Context, runner *local.Runner, job workflow.Job) (any, error) {
	doc, _ := job.Params["workflow"].(map[string]any)
	child, err := workflow.LoadMap(doc, kinds, workflow.RequireOutputs(job.OutputSchema))
	if err != nil {
		fmt.Printf("   rejected: %v\n", err)
		return nil, &workflow.StepError{Type: "invalid_workflow", Message: err.Error()}
	}

	// The child's ID is the same for every retry of this step. A durable
	// host ignores a second Start of it, so a retry finds the child it
	// started; local.Runner runs it again.
	childID := job.RunID + "/" + job.StepID.Unit()
	input, _ := job.Params["input"].(map[string]any)
	st, err := runner.Run(ctx, childID, child, input)
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

// work is every step a plan runs.
func work(job workflow.Job) (any, error) {
	fmt.Printf("   %-20s %s\n", job.RunID, job.StepID)

	switch job.Step {
	case "reproduce":
		return map[string]any{"cause": "two requests race on the cart cache"}, nil
	case "verify":
		return map[string]any{"passed": true}, nil
	}
	return map[string]any{}, nil
}
