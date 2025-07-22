package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/retry"
)

type UnreliableServiceInput struct{}

// Simulates an unreliable service that fails randomly
func unreliableService(ctx context.Context, input UnreliableServiceInput) (string, error) {
	failureRate := 0.7
	// Simulate random failure
	if rand.Float64() < failureRate {
		// Return a recoverable error to trigger retries
		return "", retry.NewRecoverableError(fmt.Errorf("service is temporarily unavailable"))
	}
	return "Success! Service responded correctly", nil
}

func main() {
	logger := workflow.NewLogger()

	wf, err := workflow.New(workflow.Options{
		Name: "retry-demo",
		Steps: []*workflow.Step{
			{
				Name:     "Initialize",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🚀 Starting retry demonstration workflow...",
				},
				Next: []*workflow.Edge{{Step: "Call Unreliable Service"}},
			},
			{
				Name:     "Call Unreliable Service",
				Activity: "unreliable_service",
				Store:    "state.service_data",
				Retry:    &workflow.RetryConfig{MaxRetries: 3},
				Next:     []*workflow.Edge{{Step: "Service Success"}},
			},
			{
				Name:     "Service Success",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ Service call succeeded: ${state.service_data}",
				},
				Next: []*workflow.Edge{{Step: "Final Success"}},
			},
			{
				Name:     "Final Success",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🎉 Workflow completed successfully after handling retries!",
				},
				End: true,
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	checkpointer, err := workflow.NewFileCheckpointer("executions")
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		Inputs:         map[string]any{},
		Logger:         logger,
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Activities: []workflow.Activity{
			workflow.TypedActivityFunction("unreliable_service", unreliableService),
			activities.NewPrintActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("This example demonstrates:")
	fmt.Println("1. Retry logic with exponential backoff")
	fmt.Println("2. Error handling in workflow steps")
	fmt.Println("3. State management across retries")
	fmt.Println("4. Multiple retry configurations")
	fmt.Println()

	if err := execution.Run(ctx); err != nil {
		log.Printf("Execution failed: %v", err)
		log.Printf("Final status: %s", execution.Status())
	} else {
		log.Println("Execution completed successfully!")
	}
}
