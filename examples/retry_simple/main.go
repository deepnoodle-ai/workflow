package main

import (
	"context"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/retry"
)

func main() {

	attempt := 0

	myOperation := func(ctx context.Context, input map[string]any) (string, error) {
		attempt++
		if attempt < 3 { // Simulated failure
			return "", retry.NewRecoverableError(fmt.Errorf("service is temporarily unavailable"))
		}
		return "SUCCESS", nil
	}

	w, err := workflow.New(workflow.Options{
		Name: "demo",
		Steps: []*workflow.Step{
			{
				Name:     "Call My Operation",
				Activity: "my_operation",
				Store:    "result",
				Retry:    &workflow.RetryConfig{MaxRetries: 2},
				Next:     []*workflow.Edge{{Step: "Finish"}},
			},
			{
				Name:     "Finish",
				Activity: "print",
				Parameters: map[string]any{
					"message": "🎉 Workflow completed successfully! Result: ${state.result}",
				},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: w,
		Logger:   workflow.NewLogger(),
		Activities: []workflow.Activity{
			workflow.TypedActivityFunction("my_operation", myOperation),
			activities.NewPrintActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := execution.Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
