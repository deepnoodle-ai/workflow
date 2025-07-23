package main

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"

	"github.com/deepnoodle-ai/workflow"
)

// PrintActivity prints a message
type PrintActivity struct{}

func (a *PrintActivity) Execute(ctx workflow.Context, params map[string]any) (any, error) {
	message, ok := params["message"].(string)
	if !ok {
		return nil, fmt.Errorf("message parameter is required and must be a string")
	}

	fmt.Println(message)
	return message, nil
}

func (a *PrintActivity) Name() string {
	return "print"
}

// GenerateNumberActivity generates a random number
type GenerateNumberActivity struct{}

func (a *GenerateNumberActivity) Execute(ctx workflow.Context, params map[string]any) (any, error) {
	number := rand.IntN(100) + 1 // 1-100
	fmt.Printf("Generated number: %d\n", number)
	return number, nil
}

func (a *GenerateNumberActivity) Name() string {
	return "generate_number"
}

func main() {
	fmt.Println("=== Edge Matching Strategy Demo ===")
	fmt.Println()

	// Register activities
	activities := []workflow.Activity{
		&PrintActivity{},
		&GenerateNumberActivity{},
	}

	// Demo 1: "all" strategy (default) - multiple paths
	fmt.Println("Demo 1: EdgeMatchingAll Strategy")
	fmt.Println("This will follow ALL matching edges, creating multiple parallel paths")
	fmt.Println("Using fixed number 50 which matches BOTH conditions: > 30 AND < 70")

	allStrategyWorkflow := createAllStrategyWorkflow()
	runWorkflowDemo(allStrategyWorkflow, activities)

	fmt.Println("\n" + strings.Repeat("=", 60) + "\n")

	// Demo 2: "first" strategy - single path
	fmt.Println("Demo 2: EdgeMatchingFirst Strategy")
	fmt.Println("This will follow ONLY the FIRST matching edge")
	fmt.Println("Using fixed number 50 which matches > 30 first, ignoring < 70")

	firstStrategyWorkflow := createFirstStrategyWorkflow()
	runWorkflowDemo(firstStrategyWorkflow, activities)
}

func createAllStrategyWorkflow() *workflow.Workflow {
	w, err := workflow.New(workflow.Options{
		Name: "EdgeMatchingAll Demo",
		Steps: []*workflow.Step{
			{
				Name:                 "Decision Point",
				Activity:             "print",
				EdgeMatchingStrategy: workflow.EdgeMatchingAll, // Explicit "all" strategy
				Parameters: map[string]any{
					"message": "Evaluating conditions with ALL matching strategy (50 > 30 AND 50 < 70)...",
				},
				Next: []*workflow.Edge{
					{Step: "Handle Large", Condition: "50 > 30"},  // Will match
					{Step: "Handle Medium", Condition: "50 < 70"}, // Will also match
					{Step: "Handle Small", Condition: "50 < 20"},  // Won't match
				},
			},
			{
				Name:     "Handle Large",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ Path A: Number is large (> 30)",
				},
				End: true,
			},
			{
				Name:     "Handle Medium",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ Path B: Number is medium (< 70)",
				},
				End: true,
			},
			{
				Name:     "Handle Small",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Path C: Number is small (< 20)",
				},
				End: true,
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return w
}

func createFirstStrategyWorkflow() *workflow.Workflow {
	w, err := workflow.New(workflow.Options{
		Name: "EdgeMatchingFirst Demo",
		Steps: []*workflow.Step{
			{
				Name:                 "Decision Point",
				Activity:             "print",
				EdgeMatchingStrategy: workflow.EdgeMatchingFirst, // "first" strategy
				Parameters: map[string]any{
					"message": "Evaluating conditions with FIRST matching strategy (50 > 30 is first match)...",
				},
				Next: []*workflow.Edge{
					{Step: "Handle Large", Condition: "50 > 30"},  // Will match first
					{Step: "Handle Medium", Condition: "50 < 70"}, // Would match but skipped
					{Step: "Handle Small", Condition: "50 < 20"},  // Won't match
				},
			},
			{
				Name:     "Handle Large",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✅ Only Path: Number is large (> 30) - first match wins!",
				},
				End: true,
			},
			{
				Name:     "Handle Medium",
				Activity: "print",
				Parameters: map[string]any{
					"message": "❌ This should not execute with first-match strategy",
				},
				End: true,
			},
			{
				Name:     "Handle Small",
				Activity: "print",
				Parameters: map[string]any{
					"message": "❌ This should not execute",
				},
				End: true,
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	return w
}

func runWorkflowDemo(w *workflow.Workflow, activities []workflow.Activity) {
	ctx := context.Background()

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:   w,
		Inputs:     map[string]any{},
		Activities: activities,
	})
	if err != nil {
		log.Fatalf("Failed to create execution: %v", err)
	}

	// Run the workflow
	err = execution.Run(ctx)
	if err != nil {
		log.Fatalf("Workflow execution failed: %v", err)
	}
}
