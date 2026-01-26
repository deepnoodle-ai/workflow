package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/stores"
)

// unreliableTask is an activity that sometimes fails
func unreliableTask(ctx workflow.Context, params map[string]any) (string, error) {
	if rand.Float64() < 0.6 {
		return "", fmt.Errorf("kaboom")
	}
	return "GREAT SUCCESS!", nil
}

// setErrorResult is an activity that sets an error result
func setErrorResult(ctx workflow.Context, params map[string]any) (string, error) {
	return "THERE WAS AN ERROR", nil
}

func main() {
	rand.Seed(time.Now().UnixNano())

	workflowDef := workflow.Options{
		Name:        "error-handling-demo",
		Description: "Demonstrates retry and catch error handling",
		State:       map[string]any{"task_result": ""},
		Outputs: []*workflow.Output{
			{
				Name:        "final_result",
				Variable:    "task_result",
				Description: "Final result of the workflow execution",
			},
		},
		Steps: []*workflow.Step{
			{
				Name:        "unreliable-task",
				Description: "A task that sometimes fails",
				Activity:    "unreliable_task",
				Store:       "task_result",
				Catch: []*workflow.CatchConfig{
					{ // Catch all non-fatal errors
						ErrorEquals: []string{workflow.ErrorTypeAll},
						Next:        "recovery-step",
					},
				},
				Next: []*workflow.Edge{{Step: "success-step"}},
			},
			{
				Name:        "success-step",
				Description: "Step executed on successful completion",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "Task completed successfully: $(state.task_result)",
				},
			},
			{
				Name:        "recovery-step",
				Description: "Recovery actions after error handling",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "Error caught! Proceeding to set error result...",
				},
				Next: []*workflow.Edge{{Step: "set-error-result"}},
			},
			{
				Name:        "set-error-result",
				Description: "Set the error result",
				Activity:    "set_error_result",
				Store:       "task_result",
			},
		},
	}

	wf, err := workflow.New(workflowDef)
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		ActivityLogger: stores.NewFileActivityLogger("logs"),
		Activities: []workflow.Activity{
			activities.NewPrintActivity(),
			workflow.NewTypedActivityFunction("unreliable_task", unreliableTask),
			workflow.NewTypedActivityFunction("set_error_result", setErrorResult),
		},
	})
	if err != nil {
		log.Fatalf("Failed to create execution: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		log.Fatalf("Workflow execution failed: %v", err)
	}

	outputs := execution.GetOutputs()
	fmt.Printf("Workflow completed with result: %v\n", outputs["final_result"])
	os.Exit(0)
}
