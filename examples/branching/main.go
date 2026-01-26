package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/stores"
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
	fmt.Println(`This example demonstrates:
1. Conditional branching with multiple conditions
2. Complex decision trees
3. State-based routing
4. Different execution paths based on data`)
	fmt.Println()

	wf, err := workflow.New(workflow.Options{
		Name: "branching-demo",
		State: map[string]any{
			"random_number": 0,
			"is_prime":      false,
			"category":      "",
		},
		Steps: []*workflow.Step{
			{
				Name:     "Start",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Starting branching workflow demonstration...",
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
				Store: "random_number",
				Next:  []*workflow.Edge{{Step: "Display Number"}},
			},
			{
				Name:     "Display Number",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Generated number: $(state.random_number)",
				},
				Next: []*workflow.Edge{{Step: "Check Prime"}},
			},
			{
				Name:     "Check Prime",
				Activity: "check_prime",
				Parameters: map[string]any{
					"number": "$(state.random_number)",
				},
				Store: "is_prime",
				Next:  []*workflow.Edge{{Step: "Categorize Number"}},
			},
			{
				Name:     "Categorize Number",
				Activity: "categorize_number",
				Parameters: map[string]any{
					"number": "$(state.random_number)",
				},
				Store: "category",
				Next: []*workflow.Edge{
					{Step: "Handle Prime", Condition: "state.is_prime == true"},
					{Step: "Handle Composite", Condition: "state.is_prime == false"},
				},
			},
			{
				Name:     "Handle Prime",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Prime number found: $(state.random_number) ($(state.category) size)",
				},
				Next: []*workflow.Edge{{Step: "Conclusion"}},
			},
			{
				Name:     "Handle Composite",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Composite number found: $(state.random_number) ($(state.category) size)",
				},
				Next: []*workflow.Edge{{Step: "Conclusion"}},
			},
			{
				Name:     "Conclusion",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Analysis complete!",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	checkpointer, err := stores.NewFileCheckpointer("executions")
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		ActivityLogger: stores.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Activities: []workflow.Activity{
			activities.NewPrintActivity(),
			workflow.NewTypedActivityFunction("generate_number", generateNumber),
			workflow.NewTypedActivityFunction("check_prime", checkPrime),
			workflow.NewTypedActivityFunction("categorize_number", categorizeNumber),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Workflow completed successfully!")
}
