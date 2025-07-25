package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

type RandomNumberInput struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

func generateNumber(ctx workflow.Context, input RandomNumberInput) (int, error) {
	num := rand.Intn(input.Max-input.Min+1) + input.Min
	return num, nil
}

type NumberInput struct {
	Number int `json:"number"`
}

func checkPrime(ctx workflow.Context, input NumberInput) (bool, error) {
	if input.Number < 2 {
		return false, nil
	}
	for i := 2; i*i <= input.Number; i++ {
		if input.Number%i == 0 {
			return false, nil
		}
	}
	return true, nil
}

func categorizeNumber(ctx workflow.Context, input NumberInput) (string, error) {
	if input.Number < 10 {
		return "small", nil
	} else if input.Number < 50 {
		return "medium", nil
	} else {
		return "large", nil
	}
}

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "branching-demo",
		State: map[string]any{
			"random_number": 0,
			"is_prime":      false,
			"category":      "",
			"processed":     false,
		},
		Steps: []*workflow.Step{
			{
				Name:     "Start",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🎲 Starting branching workflow demonstration...",
				},
				Next: []*workflow.Edge{{Step: "Generate Random Number"}},
			},
			{
				Name:     "Generate Random Number",
				Activity: "generate_number",
				Parameters: map[string]any{
					"min": 1,
					"max": 100,
				},
				Store: "state.random_number",
				Next:  []*workflow.Edge{{Step: "Display Number"}},
			},
			{
				Name:     "Display Number",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🔢 Generated number: ${state.random_number}",
				},
				Next: []*workflow.Edge{{Step: "Check Prime"}},
			},
			{
				Name:     "Check Prime",
				Activity: "check_prime",
				Parameters: map[string]any{
					"number": "$(state.random_number)",
				},
				Store: "state.is_prime",
				Next:  []*workflow.Edge{{Step: "Categorize Number"}},
			},
			{
				Name:     "Categorize Number",
				Activity: "categorize_number",
				Parameters: map[string]any{
					"number": "$(state.random_number)",
				},
				Store: "state.category",
				Next: []*workflow.Edge{
					{Step: "Handle Prime Small", Condition: "state.is_prime == true && state.category == 'small'"},
					{Step: "Handle Prime Medium", Condition: "state.is_prime == true && state.category == 'medium'"},
					{Step: "Handle Prime Large", Condition: "state.is_prime == true && state.category == 'large'"},
					{Step: "Handle Composite Small", Condition: "state.is_prime == false && state.category == 'small'"},
					{Step: "Handle Composite Medium", Condition: "state.is_prime == false && state.category == 'medium'"},
					{Step: "Handle Composite Large", Condition: "state.is_prime == false && state.category == 'large'"},
				},
			},
			// Prime number paths
			{
				Name:     "Handle Prime Small",
				Activity: "print",
				Parameters: map[string]any{
					"message": "✨ Small prime number (1-9): ${state.random_number} - These are rare and special!",
				},
				Next: []*workflow.Edge{{Step: "Final Summary"}},
			},
			{
				Name:     "Handle Prime Medium",
				Activity: "print",
				Parameters: map[string]any{
					"message": "⭐ Medium prime number (10-49): ${state.random_number} - A good building block!",
				},
				Next: []*workflow.Edge{{Step: "Final Summary"}},
			},
			{
				Name:     "Handle Prime Large",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🌟 Large prime number (50+): ${state.random_number} - Excellent for cryptography!",
				},
				Next: []*workflow.Edge{{Step: "Final Summary"}},
			},
			// Composite number paths
			{
				Name:     "Handle Composite Small",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🔸 Small composite number: ${state.random_number} - Easy to factor!",
				},
				Next: []*workflow.Edge{{Step: "Calculate Factors"}},
			},
			{
				Name:     "Handle Composite Medium",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🔹 Medium composite number: ${state.random_number} - Moderately complex!",
				},
				Next: []*workflow.Edge{{Step: "Calculate Factors"}},
			},
			{
				Name:     "Handle Composite Large",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🔷 Large composite number: ${state.random_number} - Many possible factors!",
				},
				Next: []*workflow.Edge{{Step: "Calculate Factors"}},
			},
			{
				Name:     "Calculate Factors",
				Activity: "script",
				Parameters: map[string]any{
					"code": `state.factors = "calculated using prime factorization"`,
				},
				Next: []*workflow.Edge{{Step: "Display Factors"}},
			},
			{
				Name:     "Display Factors",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🧮 Factors of ${state.random_number}: ${state.factors}",
				},
				Next: []*workflow.Edge{{Step: "Final Summary"}},
			},
			{
				Name:     "Final Summary",
				Activity: "script",
				Parameters: map[string]any{
					"code": "state.processed = true",
				},
				Next: []*workflow.Edge{{Step: "Conclusion"}},
			},
			{
				Name:     "Conclusion",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🎉 Analysis complete! Number ${state.random_number} is ${state.is_prime ? 'prime' : 'composite'} and ${state.category}-sized.",
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

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		Inputs:         map[string]any{},
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Activities: []workflow.Activity{
			workflow.NewTypedActivityFunction("generate_number", generateNumber),
			workflow.NewTypedActivityFunction("check_prime", checkPrime),
			workflow.NewTypedActivityFunction("categorize_number", categorizeNumber),
			activities.NewPrintActivity(),
			activities.NewScriptActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fmt.Println("This example demonstrates:")
	fmt.Println("1. Conditional branching with multiple conditions")
	fmt.Println("2. Complex decision trees")
	fmt.Println("3. State-based routing")
	fmt.Println("4. Script activities for calculations")
	fmt.Println("5. Different execution paths based on data")
	fmt.Println()

	if err := execution.Run(ctx); err != nil {
		log.Fatal(err)
	}
	if execution.Status() != workflow.ExecutionStatusCompleted {
		log.Fatal("execution failed")
	}
}
