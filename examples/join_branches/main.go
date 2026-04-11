package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Create a workflow that demonstrates join functionality
	wf, err := workflow.New(workflow.Options{
		Name: "join-branches-example",
		Steps: []*workflow.Step{
			{
				Name:     "start",
				Activity: "setup_data",
				Store:    "initial_value",
				Next: []*workflow.Edge{
					{Step: "process_a", BranchName: "a"},
					{Step: "process_b", BranchName: "b"},
					{Step: "join", BranchName: "final"},
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
					Branches: []string{"a", "b"},
					BranchMappings: map[string]string{
						// Extract specific variables from each branch
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
			{Name: "final_result", Variable: "final_result", Branch: "final"},
			{Name: "value_a", Variable: "valueA", Branch: "final"},
			{Name: "value_b", Variable: "valueB", Branch: "final"},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create execution with activities
	reg := workflow.NewActivityRegistry()
	reg.MustRegister(workflow.ActivityFunc("setup_data", func(ctx workflow.Context, params map[string]any) (any, error) {
		fmt.Println("🚀 Setting up initial data...")
		return 100, nil
	}))
	reg.MustRegister(workflow.ActivityFunc("work_a", func(ctx workflow.Context, params map[string]any) (any, error) {
		fmt.Println("⚙️  branch A: Processing...")
		time.Sleep(100 * time.Millisecond)

		initialValue, _ := ctx.GetVariable("initial_value")
		result := initialValue.(int) * 2
		fmt.Printf("   branch A result: %d\n", result)
		return result, nil
	}))
	reg.MustRegister(workflow.ActivityFunc("work_b", func(ctx workflow.Context, params map[string]any) (any, error) {
		fmt.Println("⚙️  branch B: Processing...")
		time.Sleep(150 * time.Millisecond)

		initialValue, _ := ctx.GetVariable("initial_value")
		result := initialValue.(int) * 3
		fmt.Printf("   branch B result: %d\n", result)
		return result, nil
	}))
	reg.MustRegister(workflow.ActivityFunc("combine_results", func(ctx workflow.Context, params map[string]any) (any, error) {
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
	}))

	execution, err := workflow.NewExecution(wf, reg,
		workflow.WithLogger(logger),
	)
	if err != nil {
		log.Fatal(err)
	}

	// Run the workflow
	fmt.Println("Starting join branches example...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	_, err = execution.Execute(ctx)
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
