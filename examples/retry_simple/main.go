package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {

	attempt := 0

	myOperation := func(ctx workflow.Context, input map[string]any) (string, error) {
		attempt++
		if attempt < 3 { // Simulated failure
			return "", errors.New("service is temporarily unavailable")
		}
		return "SUCCESS", nil
	}

	w, err := workflow.New(workflow.Options{
		Name:    "demo",
		Outputs: []*workflow.Output{{Name: "result"}},
		Steps: []*workflow.Step{
			{
				Name:     "Call My Operation",
				Activity: "my_operation",
				Store:    "result",
				Retry: []*workflow.RetryConfig{{
					ErrorEquals: []string{workflow.ErrorTypeActivityFailed},
					MaxRetries:  2,
				}},
				Next: []*workflow.Edge{{Step: "Finish"}},
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

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(workflow.TypedActivityFunc("my_operation", myOperation))
	reg.MustRegister(activities.NewPrintActivity())

	execution, err := workflow.NewExecution(w, reg,
		workflow.WithLogger(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))),
	)
	if err != nil {
		log.Fatal(err)
	}

	result, err := execution.Execute(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if result.Failed() {
		log.Fatalf("execution failed: %v", result.Error)
	}

	outputs, err := json.MarshalIndent(result.Outputs, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Outputs:")
	fmt.Println(string(outputs))
}
