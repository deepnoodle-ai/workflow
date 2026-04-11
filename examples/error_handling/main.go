package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

// unreliableTask randomly fails so the example can demonstrate retry +
// catch. It replaces the Risor script the previous version used.
func unreliableTask(ctx workflow.Context, _ struct{}) (string, error) {
	const failureRate = 0.6
	if rand.Float64() < failureRate {
		return "", errors.New("kaboom")
	}
	return "GREAT SUCCESS!", nil
}

// errorResult returns the string used to mark a failed task, replacing
// `state.task_result = "THERE WAS AN ERROR"`.
func errorResult(ctx workflow.Context, _ struct{}) (string, error) {
	return "THERE WAS AN ERROR", nil
}

func main() {
	workflowDef := workflow.Options{
		Name:        "error-handling-demo",
		Description: "Demonstrates retry and catch error handling",
		State:       map[string]any{"task_result": nil},
		Inputs: []*workflow.Input{
			{
				Name:        "max_attempts",
				Type:        "int",
				Description: "Maximum number of attempts",
				Default:     3,
			},
		},
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
				Retry: []*workflow.RetryConfig{
					{ // Retry on timeout will not match, since it's not a timeout error
						ErrorEquals: []string{workflow.ErrorTypeTimeout},
						MaxRetries:  1,
					},
				},
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
					"message": "Task completed successfully: ${state.task_result}",
				},
			},
			{
				Name:        "recovery-step",
				Description: "Recovery actions after error handling",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "Error caught!",
				},
				Next: []*workflow.Edge{{Step: "set-error-result"}},
			},
			{
				Name:        "set-error-result",
				Description: "Set the error result",
				Activity:    "error_result",
				Store:       "task_result",
			},
		},
	}

	wf, err := workflow.New(workflowDef)
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(activities.NewPrintActivity())
	reg.MustRegister(workflow.TypedActivityFunc("unreliable_task", unreliableTask))
	reg.MustRegister(workflow.TypedActivityFunc("error_result", errorResult))

	execution, err := workflow.NewExecution(wf, reg,
		workflow.WithActivityLogger(workflow.NewFileActivityLogger("logs")),
	)
	if err != nil {
		log.Fatalf("Failed to create execution: %v", err)
	}

	// Run the workflow
	fmt.Println("Starting error handling demonstration...")
	fmt.Println()

	_, err = execution.Execute(context.Background())
	if err != nil {
		fmt.Printf("Workflow failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Workflow completed successfully!\n")
	fmt.Printf("Status: %s\n", execution.Status())
	fmt.Printf("Final outputs: %+v\n", execution.GetOutputs())
}
