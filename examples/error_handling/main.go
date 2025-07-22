package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {
	// Create a logger
	logger := workflow.NewLogger()

	// Define workflow with comprehensive error handling
	workflowDef := &workflow.Options{
		Name:        "error-handling-demo",
		Description: "Demonstrates retry and catch error handling",
		Steps: []*workflow.Step{
			{
				Name:        "unreliable-task",
				Description: "A task that might fail",
				Activity:    "script",
				Parameters: map[string]any{
					"code": `
						const failureRate = 0.6
						const value = rand.float()
						if (value < 0.2) {
							error("timeout")  // Will be classified as States.Timeout
						} else if (value < 0.4) {
							error("PermissionDenied: Access forbidden")  // Custom error type
						} else if (value < failureRate) {
							error("TaskFailure: Generic task failure")  // Will be classified as States.TaskFailed
						}
						"Task completed successfully!"
					`,
				},
				Store: "task_result",
				Retry: []*workflow.RetryConfig{
					{
						// Retry timeout errors with exponential backoff
						ErrorEquals:    []string{workflow.ErrorTypeTimeout},
						MaxRetries:     3,
						BaseDelay:      time.Second * 2,
						BackoffRate:    2.0,
						MaxDelay:       time.Second * 10,
						JitterStrategy: workflow.JitterFull,
					},
					{
						// Retry generic task failures with different settings
						ErrorEquals:    []string{workflow.ErrorTypeActivityFailed},
						MaxRetries:     2,
						BaseDelay:      time.Second * 1,
						BackoffRate:    1.5,
						JitterStrategy: workflow.JitterNone,
					},
				},
				Catch: []*workflow.CatchConfig{
					{
						// Catch permission errors and redirect to permission handler
						ErrorEquals: []string{"PermissionDenied"},
						Next:        "handle-permission-error",
						Store:       "error_info",
					},
					{
						// Catch all other errors and redirect to general error handler
						ErrorEquals: []string{workflow.ErrorTypeAll},
						Next:        "handle-general-error",
						Store:       "error_info",
					},
				},
				Next: []*workflow.Edge{
					{Step: "success-step"},
				},
			},
			{
				Name:        "success-step",
				Description: "Step executed on successful completion",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "Task completed successfully: ${state.task_result}",
				},
				End: true,
			},
			{
				Name:        "handle-permission-error",
				Description: "Handle permission-specific errors",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "Permission error occurred: ${state.error_info.Cause}",
				},
				Next: []*workflow.Edge{
					{Step: "recovery-step"},
				},
			},
			{
				Name:        "handle-general-error",
				Description: "Handle general errors",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "General error occurred: ${state.error_info.Error} - ${state.error_info.Cause}",
				},
				Next: []*workflow.Edge{
					{Step: "recovery-step"},
				},
			},
			{
				Name:        "recovery-step",
				Description: "Recovery actions after error handling",
				Activity:    "print",
				Parameters: map[string]any{
					"message": "Executing recovery procedures...",
				},
				End: true,
			},
		},
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
	}

	// Create workflow
	wf, err := workflow.New(*workflowDef)
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}

	// Create checkpointer
	checkpointer, err := workflow.NewFileCheckpointer("executions")
	if err != nil {
		log.Fatalf("Failed to create checkpointer: %v", err)
	}

	// Create execution
	ctx := context.Background()
	inputs := map[string]any{
		"max_attempts": 5,
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		Inputs:         inputs,
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Logger:         logger,
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
	fmt.Println("The workflow will attempt an unreliable task with retry and catch handling.")
	fmt.Println()

	err = execution.Run(ctx)
	if err != nil {
		fmt.Printf("Workflow failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Workflow completed successfully!\n")
	fmt.Printf("Status: %s\n", execution.Status())
	fmt.Printf("Final outputs: %+v\n", execution.GetOutputs())
}
