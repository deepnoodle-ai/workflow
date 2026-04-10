package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	risorengine "github.com/deepnoodle-ai/workflow/scriptengines/risor"
)

func main() {
	w, err := workflow.LoadFile("example.yaml")
	if err != nil {
		log.Fatal(err)
	}

	// Define a custom "print" builtin for use in Risor scripts.
	// This matches Risor's own print implementation from the CLI.
	printBuiltin := object.NewBuiltin("print",
		func(ctx context.Context, args ...object.Object) (object.Object, error) {
			values := make([]any, len(args))
			for i, arg := range args {
				values[i] = object.PrintableValue(arg)
			}
			fmt.Println(values...)
			return object.Nil, nil
		},
	)

	// Pass custom builtins via DefaultGlobals extras, so they are
	// available to both template evaluation and script activity execution.
	globals := risorengine.DefaultGlobals(map[string]any{
		"print": printBuiltin,
	})
	compiler := risorengine.NewEngine(globals)

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:       w,
		Logger:         workflow.NewLogger(),
		ScriptCompiler: compiler,
		Activities: []workflow.Activity{
			risorengine.NewScriptActivity(),
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
