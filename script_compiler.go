package workflow

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
	"github.com/deepnoodle-ai/workflow/script"
)

// DefaultScriptCompiler returns a script.Compiler backed by
// github.com/deepnoodle-ai/expr with the standard builtin function set
// enabled. This is the compiler used by NewExecution when
// ExecutionOptions.ScriptCompiler is nil — it handles edge conditions
// and ${...} parameter templates out of the box.
//
// expr is expression-only: it cannot mutate state, so workflows that
// need state-mutating scripts must provide their own script.Compiler
// (for example, by wrapping a language like Risor behind the
// script.Compiler interface).
func DefaultScriptCompiler() script.Compiler {
	return exprCompiler{}
}

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
