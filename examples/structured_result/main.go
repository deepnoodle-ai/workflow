// Example: structured_result
//
// Demonstrates using Execute() to get a structured ExecutionResult
// instead of a plain error. The result includes status, outputs,
// timing, and classified errors.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "order-processing",
		Inputs: []*workflow.Input{
			{Name: "item", Type: "string", Default: "Widget"},
			{Name: "quantity", Type: "number", Default: 5},
		},
		State: map[string]any{},
		Outputs: []*workflow.Output{
			{Name: "summary", Variable: "summary"},
		},
		Steps: []*workflow.Step{
			{
				Name:     "Calculate Total",
				Activity: "script",
				Parameters: map[string]any{
					"code": `state.total = inputs.quantity * 9.99`,
				},
				Next: []*workflow.Edge{{Step: "Generate Summary"}},
			},
			{
				Name:     "Generate Summary",
				Activity: "script",
				Parameters: map[string]any{
					"code": `state.summary = "Processed " + string(inputs.quantity) + "x " + inputs.item + " for $" + string(state.total)`,
				},
				Next: []*workflow.Edge{{Step: "Print Result"}},
			},
			{
				Name:     "Print Result",
				Activity: "print",
				Parameters: map[string]any{
					"message": "${state.summary}",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	exec, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: wf,
		Activities: []workflow.Activity{
			activities.NewPrintActivity(),
			activities.NewScriptActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Execute() returns a structured result instead of just an error.
	// - error means infrastructure failure (couldn't run at all)
	// - result contains the execution outcome, including workflow failures
	result, err := exec.Execute(context.Background())
	if err != nil {
		log.Fatalf("Infrastructure error: %v", err)
	}

	// Inspect the result
	fmt.Printf("Workflow:  %s\n", result.WorkflowName)
	fmt.Printf("Status:    %s\n", result.Status)
	fmt.Printf("Duration:  %s\n", result.Timing.Duration)

	if result.Completed() {
		fmt.Printf("Output:    %s\n", result.Outputs["summary"])
	}
	if result.Failed() {
		fmt.Printf("Error:     %s\n", result.Error.Cause)
	}
}
