package main

import (
	"context"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

// incrementActivity demonstrates state mutation via a typed activity.
// The root workflow module no longer ships a state-mutating script
// engine, so values that need to change are computed by Go activities
// and written back to state via the Step's Store field.
type incrementActivity struct{}

type incrementParams struct {
	Value int `json:"value"`
}

func (incrementActivity) Name() string { return "increment" }

func (incrementActivity) Execute(ctx workflow.Context, p incrementParams) (int, error) {
	return p.Value + 1, nil
}

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
				Store:    "state.current_time",
				Next:     []*workflow.Edge{{Step: "Print Current Time"}},
			},
			{
				Name:     "Print Current Time",
				Activity: "print",
				Parameters: map[string]any{
					"message": "It is now ${state.current_time}. The counter is ${state.counter}. The max count is ${inputs.max_count}.",
				},
				Next: []*workflow.Edge{{Step: "Increment"}},
			},
			{
				Name:     "Increment",
				Activity: "increment",
				Parameters: map[string]any{
					"value": "$(state.counter)",
				},
				Store: "state.counter",
				Next:  []*workflow.Edge{{Step: "Wait Then Loop"}},
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

	checkpointer, err := workflow.NewFileCheckpointer("executions")
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       wf,
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Inputs:         map[string]any{"max_count": 5},
		Activities: []workflow.Activity{
			activities.NewTimeActivity(),
			activities.NewWaitActivity(),
			activities.NewPrintActivity(),
			workflow.NewTypedActivity(incrementActivity{}),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		log.Fatal(err)
	}
	if execution.Status() != workflow.ExecutionStatusCompleted {
		log.Fatal("execution failed")
	}
}
