package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func getTime(ctx context.Context, params map[string]any) (any, error) {
	return time.Now().Format(time.RFC3339), nil
}

func sleep(ctx context.Context, params map[string]any) (any, error) {
	duration, ok := params["duration"]
	if !ok {
		return nil, errors.New("sleep activity requires 'duration' parameter")
	}
	time.Sleep(duration.(time.Duration))
	return nil, nil
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
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	wf, err := workflow.New(workflow.Options{
		Name:  "data-processing",
		State: map[string]any{"counter": 0},
		Steps: []*workflow.Step{
			{
				Name:     "Get Current Time",
				Activity: "time.now",
				Store:    "state.current_time",
				Next:     []*workflow.Edge{{Step: "Print Current Time"}},
			},
			{
				Name:     "Print Current Time",
				Activity: "print",
				Parameters: map[string]any{
					"message": "It is now ${state.current_time}",
				},
				Next: []*workflow.Edge{{Step: "Script"}},
			},
			{
				Name:     "Script",
				Activity: "script",
				Parameters: map[string]any{
					"code": "state.counter+=1",
				},
				Next: []*workflow.Edge{{Step: "Sleep Then Loop"}},
			},
			{
				Name:     "Sleep Then Loop",
				Activity: "sleep",
				Parameters: map[string]any{
					"duration": 1 * time.Second,
				},
				Next: []*workflow.Edge{
					{Step: "Get Current Time", Condition: "state.counter < 2"},
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
		Logger:         logger,
		ActivityLogger: workflow.NewFileActivityLogger("logs"),
		Checkpointer:   checkpointer,
		Activities: []workflow.Activity{
			workflow.NewActivityFunction("time.now", getTime),
			workflow.NewActivityFunction("sleep", sleep),
			workflow.NewActivityFunction("print", print),
			activities.NewScriptActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		log.Fatal(err)
	}
	if execution.Status() != workflow.ExecutionStatusCompleted {
		log.Fatal("execution failed")
	}
}
