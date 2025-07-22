package main

import (
	"context"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
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
				Store:    "state.current_time",
				Next:     []*workflow.Edge{{Step: "Print Current Time"}},
			},
			{
				Name:     "Print Current Time",
				Activity: "print",
				Parameters: map[string]any{
					"message": "It is now ${state.current_time}. The counter is ${state.counter}. The max count is ${inputs.max_count}.",
				},
				Next: []*workflow.Edge{{Step: "Script"}},
			},
			{
				Name:     "Script",
				Activity: "script",
				Parameters: map[string]any{
					"code": "state.counter+=1",
				},
				Next: []*workflow.Edge{{Step: "Wait Then Loop"}},
			},
			{
				Name:     "Wait Then Loop",
				Activity: "wait",
				Parameters: map[string]any{
					"duration": 1,
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
			activities.NewScriptActivity(),
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
