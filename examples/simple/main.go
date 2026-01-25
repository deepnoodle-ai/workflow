package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/stores"
)

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "data-processing",
		Inputs: []*workflow.Input{
			{
				Name:        "max_count",
				Type:        "number",
				Description: "The maximum number of times to loop",
				Default:     3,
			},
		},
		State: map[string]any{"counter": 1},
		Steps: []*workflow.Step{
			{
				Name:     "Get Current Time",
				Activity: "time",
				Store:    "current_time",
				Next:     []*workflow.Edge{{Step: "Print Current Time"}},
			},
			{
				Name:     "Print Current Time",
				Activity: "print",
				Parameters: map[string]any{
					"message": "It is now ${state.current_time}. The counter is ${state.counter}. The max count is ${inputs.max_count}.",
				},
				Next: []*workflow.Edge{{Step: "Increment Counter"}},
			},
			{
				Name:     "Increment Counter",
				Activity: "increment",
				Store:    "counter",
				Next:     []*workflow.Edge{{Step: "Wait Then Loop"}},
			},
			{
				Name:     "Wait Then Loop",
				Activity: "wait",
				Parameters: map[string]any{
					"seconds": 1,
				},
				Next: []*workflow.Edge{
					{Step: "Get Current Time", Condition: "state.counter <= inputs.max_count"},
					{Step: "Finish", Condition: "state.counter > inputs.max_count"},
				},
			},
			{
				Name:       "Finish",
				Activity:   "print",
				Parameters: map[string]any{"message": "Finished!"},
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
		Inputs:         map[string]any{"max_count": 5},
		Activities: []workflow.Activity{
			activities.NewTimeActivity(),
			activities.NewWaitActivity(),
			activities.NewPrintActivity(),
			// Custom activity to increment counter
			workflow.NewActivityFunction("increment", func(ctx workflow.Context, params map[string]any) (any, error) {
				counter, ok := ctx.GetVariable("counter")
				if !ok {
					counter = 0
				}
				// Handle both int and float64 (JSON serialization converts int to float64)
				var current int
				switch v := counter.(type) {
				case int:
					current = v
				case float64:
					current = int(v)
				default:
					current = 0
				}
				newValue := current + 1
				fmt.Printf("Incrementing counter: %d -> %d\n", current, newValue)
				return newValue, nil
			}),
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
	if execution.Status() != domain.ExecutionStatusCompleted {
		log.Fatal("execution failed")
	}
	fmt.Println("Workflow completed successfully!")
}
