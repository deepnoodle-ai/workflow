package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/retry"
)

// Simulates an unreliable service that fails randomly
func unreliableService(ctx context.Context, params map[string]any) (any, error) {
	// Get failure rate from parameters, default to 60%
	failureRate := 0.6
	if rate, ok := params["failure_rate"].(float64); ok {
		failureRate = rate
	}

	serviceName := "default-service"
	if name, ok := params["service_name"].(string); ok {
		serviceName = name
	}

	// Simulate random failure
	if rand.Float64() < failureRate {
		// Return a recoverable error to trigger retries
		return nil, retry.NewRecoverableError(fmt.Errorf("service '%s' is temporarily unavailable", serviceName))
	}

	return fmt.Sprintf("Success! Service '%s' responded correctly", serviceName), nil
}

// Simulates a data validation function
func validateData(ctx context.Context, params map[string]any) (any, error) {
	data, ok := params["data"]
	if !ok {
		return nil, errors.New("no data provided for validation")
	}

	dataStr, ok := data.(string)
	if !ok {
		return nil, errors.New("data must be a string")
	}

	if len(dataStr) < 5 {
		// Return a recoverable error for validation failures
		return nil, retry.NewRecoverableError(errors.New("data too short - must be at least 5 characters"))
	}

	return fmt.Sprintf("Data validated successfully: %s", dataStr), nil
}

func print(ctx context.Context, params map[string]any) (any, error) {
	message, ok := params["message"]
	if !ok {
		return nil, errors.New("print activity requires 'message' parameter")
	}
	fmt.Println(message)
	return nil, nil
}

func main() {
	// Set random seed for reproducible demo
	rand.Seed(time.Now().UnixNano())

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	wf, err := workflow.New(workflow.Options{
		Name: "retry-demo",
		State: map[string]any{
			"attempts":      0,
			"max_attempts":  3,
			"service_data":  "",
			"validation_ok": false,
		},
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
				Parameters: map[string]any{
					"service_name": "external-api",
					"failure_rate": 0.7, // 70% failure rate
				},
				Store: "state.service_data",
				Retry: &workflow.RetryConfig{
					MaxRetries: 3,
					BaseDelay:  500 * time.Millisecond,
					MaxDelay:   2 * time.Second,
					Timeout:    5 * time.Second,
				},
				Next: []*workflow.Edge{{Step: "Service Success"}},
			},
			{
				Name:     "Service Success",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ Service call succeeded: ${state.service_data}",
				},
				Next: []*workflow.Edge{{Step: "Validate Response"}},
			},
			{
				Name:     "Validate Response",
				Activity: "validate_data",
				Parameters: map[string]any{
					"data": "${state.service_data}",
				},
				Store: "state.validation_result",
				Retry: &workflow.RetryConfig{
					MaxRetries: 2,
					BaseDelay:  200 * time.Millisecond,
				},
				Next: []*workflow.Edge{{Step: "Validation Success"}},
			},
			{
				Name:     "Validation Success",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ ${state.validation_result}",
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
			workflow.NewActivityFunction("unreliable_service", unreliableService),
			workflow.NewActivityFunction("validate_data", validateData),
			workflow.NewActivityFunction("print", print),
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
