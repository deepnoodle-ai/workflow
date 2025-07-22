package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

func main() {
	w, err := workflow.LoadFile("example.yaml")
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: w,
		Logger:   workflow.NewLogger(),
		Activities: []workflow.Activity{
			activities.NewScriptActivity(),
			activities.NewPrintActivity(),
			activities.NewWaitActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := execution.Run(context.Background()); err != nil {
		log.Fatal(err)
	}

	outputs := execution.GetOutputs()
	if len(outputs) > 0 {
		outputs, err := json.MarshalIndent(outputs, "", "  ")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(string(outputs))
	}
}
