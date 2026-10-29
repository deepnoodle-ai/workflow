package durable_test

import (
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
)

// push sends each job a result wrote to a task queue. It names package
// durable, not a backend, so it takes the result of any store.
func push(res *durable.Result) {
	for _, ref := range res.Jobs() {
		body, _ := json.Marshal(ref)
		fmt.Println(string(body))
	}
}

func ExampleResult_Jobs() {
	push(&durable.Result{
		RunID: "order-42",
		Commands: []workflow.Command{
			{Kind: workflow.CommandDispatchStep, StepID: "main/charge#1", StepKind: "action"},
			{Kind: workflow.CommandScheduleTimer, StepID: "main/charge#1", StepKind: "action"},
			{Kind: workflow.CommandRunCompleted},
		},
	})
	// Output:
	// {"run_id":"order-42","step_id":"main/charge#1","step_kind":"action"}
	// {"run_id":"order-42","step_id":"main/charge#1","timer":true,"step_kind":"action"}
}
