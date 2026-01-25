package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/client"
)

// This demo shows how to use the Client interface to interact with workflows.
// The Client interface provides a clean separation between workflow submission
// and execution, supporting both local and remote (HTTP) backends.

func main() {
	ctx := context.Background()

	// Create a registry to hold workflows and activities
	registry := workflow.NewRegistry()

	// Register activities
	registry.MustRegisterActivity(workflow.NewActivityFunction("greet", greetActivity))
	registry.MustRegisterActivity(workflow.NewActivityFunction("transform", transformActivity))

	// Register workflow
	wf := createWorkflow()
	registry.MustRegisterWorkflow(wf)

	// Create a local client (backed by in-process engine)
	// In production, you would use client.NewHTTPClient() to connect to a remote server
	c, err := client.NewLocalClient(client.LocalClientOptions{
		Registry: registry,
		Logger:   workflow.NewLogger(),
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Start the client (starts the backing engine)
	if err := c.Start(ctx); err != nil {
		log.Fatalf("Failed to start client: %v", err)
	}
	defer c.Stop(ctx)

	fmt.Println("=== Workflow Client Demo ===")
	fmt.Println()

	// Submit a workflow
	fmt.Println("Submitting workflow...")
	execID, err := c.Submit(ctx, wf, map[string]any{
		"name": "World",
	})
	if err != nil {
		log.Fatalf("Failed to submit workflow: %v", err)
	}
	fmt.Printf("Workflow submitted with ID: %s\n", execID)

	// Poll for status
	fmt.Println("Polling for completion...")
	for {
		status, err := c.Get(ctx, execID)
		if err != nil {
			log.Fatalf("Failed to get status: %v", err)
		}

		fmt.Printf("  Status: %s\n", status.State)

		if status.State == client.StateCompleted || status.State == client.StateFailed {
			if status.Error != "" {
				fmt.Printf("  Error: %s\n", status.Error)
			}
			break
		}

		time.Sleep(100 * time.Millisecond)
	}

	// Get final result using Wait
	fmt.Println()
	fmt.Println("Getting final result...")
	result, err := c.Wait(ctx, execID)
	if err != nil {
		log.Fatalf("Failed to wait for result: %v", err)
	}

	fmt.Printf("Workflow completed!\n")
	fmt.Printf("  State: %s\n", result.State)
	fmt.Printf("  Duration: %v\n", result.Duration)
	fmt.Printf("  Outputs: %v\n", result.Outputs)

	// List executions
	fmt.Println()
	fmt.Println("Listing all executions...")
	executions, err := c.List(ctx, client.ListFilter{})
	if err != nil {
		log.Fatalf("Failed to list executions: %v", err)
	}
	for _, exec := range executions {
		fmt.Printf("  - %s: %s (%s)\n", exec.ID, exec.WorkflowName, exec.State)
	}
}

func createWorkflow() *workflow.Workflow {
	wf, err := workflow.New(workflow.Options{
		Name: "greeting-workflow",
		Inputs: []*workflow.Input{
			{Name: "name", Type: "string"},
		},
		Outputs: []*workflow.Output{
			{Name: "greeting"},
			{Name: "transformed"},
		},
		Steps: []*workflow.Step{
			{
				Name:       "Generate Greeting",
				Activity:   "greet",
				Parameters: map[string]any{"name": "$(inputs.name)"},
				Store:      "greeting",
				Next:       []*workflow.Edge{{Step: "Transform"}},
			},
			{
				Name:       "Transform",
				Activity:   "transform",
				Parameters: map[string]any{"text": "$(state.greeting)"},
				Store:      "transformed",
			},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create workflow: %v", err)
	}
	return wf
}

func greetActivity(ctx workflow.Context, params map[string]any) (any, error) {
	name := params["name"].(string)
	greeting := fmt.Sprintf("Hello, %s!", name)
	fmt.Printf("  [greet] Generated: %s\n", greeting)
	return greeting, nil
}

func transformActivity(ctx workflow.Context, params map[string]any) (any, error) {
	text := params["text"].(string)
	transformed := fmt.Sprintf("*** %s ***", text)
	fmt.Printf("  [transform] Transformed: %s\n", transformed)
	return transformed, nil
}
