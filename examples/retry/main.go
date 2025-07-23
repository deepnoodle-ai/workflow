package main

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

type UnreliableServiceInput struct{}

// Simulates an unreliable service that fails randomly
func unreliableService(ctx workflow.Context, input UnreliableServiceInput) (string, error) {
	failureRate := 0.7
	// Simulate random failure
	if rand.Float64() < failureRate {
		// Return a timeout error to trigger retries
		return "", workflow.NewWorkflowError(workflow.ErrorTypeTimeout, "service is temporarily unavailable")
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
				Retry: []*workflow.RetryConfig{{
					ErrorEquals: []string{workflow.ErrorTypeTimeout},
					MaxRetries:  3,
					BaseDelay:   1 * time.Second,
					BackoffRate: 2.0,
				}},
				Next: []*workflow.Edge{{Step: "Service Success"}},
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
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Logger:         logger,
		Activities: []workflow.Activity{
			activities.NewPrintActivity(),
			workflow.TypedActivityFunction("unreliable_service", unreliableService),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		log.Fatal(err)
	}
	if execution.Status() != workflow.ExecutionStatusCompleted {
		log.Fatal("execution failed")
	}
}
