// Workflow integration: plug expr into the workflow engine as its
// ScriptCompiler. expr is expression-only — it evaluates edge
// conditions and ${...} parameter templates, but it cannot mutate
// state. Workflows that need state-mutating scripts must wrap an
// imperative engine (Risor, Starlark, etc.) behind script.Compiler
// themselves.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/deepnoodle-ai/expr"
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
	"github.com/deepnoodle-ai/workflow/script"
)

func main() {
	wf, err := workflow.New(workflow.Options{
		Name: "expr-demo",
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
					// ${...} interpolation is evaluated by expr.
					"message": "Order total: ${inputs.order_total} (VIP: ${state.vip})",
				},
				Next: []*workflow.Edge{
					// Every Condition below is an expr expression.
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

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(activities.NewPrintActivity())

	execution, err := workflow.NewExecution(wf, reg,
		// Use expr as the expression engine for conditions + templates.
		workflow.WithScriptCompiler(exprCompiler{}),
		workflow.WithInputs(map[string]any{
			"order_total": 120.0,
			"country":     "CA",
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := execution.Execute(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("status:", execution.Status())
}

// exprCompiler adapts github.com/deepnoodle-ai/expr to the workflow
// engine's script.Compiler interface. The workflow engine calls
// Compile for every edge condition and ${...} template fragment; each
// returns a script.Script that the engine evaluates against the
// current state + inputs.
type exprCompiler struct{}

func (exprCompiler) Compile(ctx context.Context, code string) (script.Script, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := expr.Compile(code, expr.WithBuiltins())
	if err != nil {
		return nil, err
	}
	return exprScript{program: p}, nil
}

type exprScript struct{ program *expr.Program }

func (s exprScript) Evaluate(ctx context.Context, globals map[string]any) (script.Value, error) {
	v, err := s.program.Run(ctx, globals)
	if err != nil {
		return nil, err
	}
	return exprValue{v: v}, nil
}

type exprValue struct{ v any }

func (v exprValue) Value() any            { return v.v }
func (v exprValue) IsTruthy() bool        { return script.IsTruthyValue(v.v) }
func (v exprValue) Items() ([]any, error) { return script.EachValue(v.v) }
func (v exprValue) String() string {
	if v.v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v.v)
}
