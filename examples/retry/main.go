package main

import (
	"context"
	"log"
	"log/slog"
	"math/rand"
	"os"
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
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

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
				Store:    "service_data",
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

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(activities.NewPrintActivity())
	reg.MustRegister(workflow.TypedActivityFunc("unreliable_service", unreliableService))

	execution, err := workflow.NewExecution(wf, reg,
		workflow.WithActivityLogger(workflow.NewFileActivityLogger("logs")),
		workflow.WithCheckpointer(checkpointer),
		workflow.WithLogger(logger),
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := execution.Execute(ctx); err != nil {
		log.Fatal(err)
	}
	if execution.Status() != workflow.ExecutionStatusCompleted {
		log.Fatal("execution failed")
	}
}
