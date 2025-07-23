package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

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
				Activity:    "script",
				Parameters: map[string]any{
					"code": `
						const failureRate = 0.6
						const value = rand.float()
						if (value < failureRate) {
							error("kaboom")
						}
						"GREAT SUCCESS!"
					`,
				},
				Store: "task_result",
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
				Activity:    "script",
				Parameters: map[string]any{
					"code": `state.task_result = "THERE WAS AN ERROR"`,
				},
			},
		},
	}

	wf, err := workflow.New(workflowDef)
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Activities: []workflow.Activity{
			activities.NewPrintActivity(),
			activities.NewScriptActivity(),
		},
	})
	if err != nil {
		log.Fatalf("Failed to create execution: %v", err)
	}

	// Run the workflow
	fmt.Println("Starting error handling demonstration...")
	fmt.Println()

	err = execution.Run(context.Background())
	if err != nil {
		fmt.Printf("Workflow failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Workflow completed successfully!\n")
	fmt.Printf("Status: %s\n", execution.Status())
	fmt.Printf("Final outputs: %+v\n", execution.GetOutputs())
}
