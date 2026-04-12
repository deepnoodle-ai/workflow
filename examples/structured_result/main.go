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

type calcTotalInput struct {
	Quantity float64 `json:"quantity"`
}

func calcTotal(ctx workflow.Context, in calcTotalInput) (float64, error) {
	return in.Quantity * 9.99, nil
}

type summaryInput struct {
	Item     string  `json:"item"`
	Quantity float64 `json:"quantity"`
	Total    float64 `json:"total"`
}

func buildSummary(ctx workflow.Context, in summaryInput) (string, error) {
	return fmt.Sprintf("Processed %vx %s for $%v", in.Quantity, in.Item, in.Total), nil
}

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
				Activity: "calc_total",
				Parameters: map[string]any{
					"quantity": "${inputs.quantity}",
				},
				Store: "total",
				Next:  []*workflow.Edge{{Step: "Generate Summary"}},
			},
			{
				Name:     "Generate Summary",
				Activity: "build_summary",
				Parameters: map[string]any{
					"item":     "${inputs.item}",
					"quantity": "${inputs.quantity}",
					"total":    "${state.total}",
				},
				Store: "summary",
				Next:  []*workflow.Edge{{Step: "Print Result"}},
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

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(activities.NewPrintActivity())
	reg.MustRegister(workflow.TypedActivityFunc("calc_total", calcTotal))
	reg.MustRegister(workflow.TypedActivityFunc("build_summary", buildSummary))

	exec, err := workflow.NewExecution(wf, reg)
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
