// Workflow integration: plug goexpr into the workflow engine as its
// ScriptCompiler. goexpr is expression-only — it evaluates edge
// conditions and ${...} parameter templates, but it cannot mutate
// state, so the `script` activity is unavailable here. Use the Risor
// engine (scripts/risor) if you need state-mutating scripts.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/scripts/goexpr"
)

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "goexpr-demo",
		Inputs: []*workflow.Input{
			{Name: "order_total", Type: "number", Default: 42.0},
			{Name: "country", Type: "string", Default: "US"},
		},
		State: map[string]any{
			"vip": true,
		},
		Steps: []*workflow.Step{
			{
				Name:     "Classify",
				Activity: "print",
				Parameters: map[string]any{
					// ${...} interpolation is evaluated by goexpr.
					"message": "Order total: ${inputs.order_total} (VIP: ${state.vip})",
				},
				Next: []*workflow.Edge{
					// Every Condition below is a goexpr expression.
					{Step: "Ship Free", Condition: "inputs.order_total >= 100 || state.vip"},
					{Step: "Charge Shipping", Condition: "inputs.order_total < 100 && !state.vip"},
				},
			},
			{
				Name:     "Ship Free",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Free shipping to ${inputs.country}.",
				},
				Next: []*workflow.Edge{{Step: "Done"}},
			},
			{
				Name:     "Charge Shipping",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Shipping to ${inputs.country} costs extra.",
				},
				Next: []*workflow.Edge{{Step: "Done"}},
			},
			{
				Name:       "Done",
				Activity:   "print",
				Parameters: map[string]any{"message": "Finished."},
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: wf,
		// Use goexpr as the expression engine for conditions + templates.
		ScriptCompiler: goexpr.New().Compiler(),
		Inputs: map[string]any{
			"order_total": 120.0,
			"country":     "CA",
		},
		Activities: []workflow.Activity{
			activities.NewPrintActivity(),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("status:", execution.Status())
}
