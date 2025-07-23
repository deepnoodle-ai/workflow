package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

func main() {
	logger := workflow.NewLogger()

	// Create a workflow that demonstrates join functionality
	wf, err := workflow.New(workflow.Options{
		Name: "join-paths-example",
		Steps: []*workflow.Step{
			{
				Name:     "start",
				Activity: "setup_data",
				Store:    "initial_value",
				Next: []*workflow.Edge{
					{Step: "process_a", Path: "a"},
					{Step: "process_b", Path: "b"},
					{Step: "join", Path: "final"},
				},
			},
			{
				Name:     "process_a",
				Activity: "work_a",
				Store:    "result_a",
			},
			{
				Name:     "process_b",
				Activity: "work_b",
				Store:    "result_b",
			},
			{
				Name: "join",
				Join: &workflow.JoinConfig{
					Paths: []string{"a", "b"},
					PathMappings: map[string]string{
						// Extract specific variables from each path
						"a.result_a": "valueA",
						"b.result_b": "valueB",
					},
				},
				Next: []*workflow.Edge{{Step: "finalize"}},
			},
			{
				Name:     "finalize",
				Activity: "combine_results",
				Store:    "final_result",
			},
		},
		Outputs: []*workflow.Output{
			{Name: "final_result", Variable: "final_result", Path: "final"},
			{Name: "value_a", Variable: "valueA", Path: "final"},
			{Name: "value_b", Variable: "valueB", Path: "final"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create execution with activities
	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: wf,
		Logger:   logger,
		Activities: []workflow.Activity{
			workflow.NewActivityFunction("setup_data", func(ctx workflow.Context, params map[string]any) (any, error) {
				fmt.Println("🚀 Setting up initial data...")
				return 100, nil
			}),
			workflow.NewActivityFunction("work_a", func(ctx workflow.Context, params map[string]any) (any, error) {
				fmt.Println("⚙️  Path A: Processing...")
				time.Sleep(100 * time.Millisecond)

				initialValue, _ := ctx.GetVariable("initial_value")
				result := initialValue.(int) * 2
				fmt.Printf("   Path A result: %d\n", result)
				return result, nil
			}),
			workflow.NewActivityFunction("work_b", func(ctx workflow.Context, params map[string]any) (any, error) {
				fmt.Println("⚙️  Path B: Processing...")
				time.Sleep(150 * time.Millisecond)

				initialValue, _ := ctx.GetVariable("initial_value")
				result := initialValue.(int) * 3
				fmt.Printf("   Path B result: %d\n", result)
				return result, nil
			}),
			workflow.NewActivityFunction("combine_results", func(ctx workflow.Context, params map[string]any) (any, error) {
				fmt.Println("🔗 Combining results...")

				// Access the extracted values directly
				valueA, _ := ctx.GetVariable("valueA")
				valueB, _ := ctx.GetVariable("valueB")

				resultA := valueA.(int)
				resultB := valueB.(int)

				fmt.Printf("   Value A: %d\n", resultA)
				fmt.Printf("   Value B: %d\n", resultB)

				total := resultA + resultB
				fmt.Printf("   Combined result: %d\n", total)
				return total, nil
			}),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Run the workflow
	fmt.Println("Starting join paths example...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	err = execution.Run(ctx)
	duration := time.Since(start)

	if err != nil {
		log.Fatal(err)
	}

	// Print results
	fmt.Printf("\n✅ Workflow completed in %v\n", duration)

	outputs := execution.GetOutputs()
	fmt.Printf("Final result: %v\n", outputs["final_result"])
	fmt.Printf("Value A: %v\n", outputs["value_a"])
	fmt.Printf("Value B: %v\n", outputs["value_b"])
}
