// Package expr provides an expr-lang (https://expr-lang.org) implementation
// of the workflow script.Compiler interface. Consumers wire it up via
// ExecutionOptions.ScriptCompiler to enable expr expressions in workflow
// conditions, templates, and parameter interpolation.
//
// Expr is expression-only: it cannot execute multi-statement scripts or
// mutate external state. As a result, this package does not provide an
// equivalent of the Risor "script" activity. If a workflow needs script-
// level state mutation, use scripts/risor instead.
package expr

import (
	"context"
	"errors"
	"fmt"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// ErrExprCompile wraps errors returned by the expr compiler so callers can
// detect compile failures via errors.Is.
var ErrExprCompile = errors.New("expr compile error")

// ErrExprEvaluate wraps errors returned by expr.Run so callers can detect
// evaluation failures via errors.Is.
var ErrExprEvaluate = errors.New("expr evaluate error")

// reservedBindings are names the workflow engine always populates in the
// expression environment. User-provided functions are not allowed to shadow
// them because the engine assumes they exist.
var reservedBindings = map[string]struct{}{
	"state":  {},
	"inputs": {},
}

// Engine is an expr-lang backed script.Compiler.
type Engine struct {
	// funcs are user-provided functions made available to every compiled
	// expression. They are merged into the per-evaluation environment.
	funcs map[string]any
	// options are additional expr.Option values appended to every compile.
	options []expr.Option
}

// Option configures a new Engine.
type Option func(*Engine)

// WithFunctions registers named Go functions that become callable from every
// compiled expression. Functions are evaluated in the context of expr's own
// calling convention — see the expr-lang documentation for details.
//
// Function names "state" and "inputs" are reserved by the workflow engine and
// will cause this option to panic if provided, since allowing them would
// silently overwrite the workflow state/inputs bindings.
func WithFunctions(funcs map[string]any) Option {
	return func(e *Engine) {
		if e.funcs == nil {
			e.funcs = make(map[string]any, len(funcs))
		}
		for name, fn := range funcs {
			if _, reserved := reservedBindings[name]; reserved {
				panic(fmt.Sprintf("expr: function name %q is reserved by the workflow engine", name))
			}
			e.funcs[name] = fn
		}
	}
}

// WithExprOptions appends raw expr.Option values to every compile call.
// Use this to enable expr features like operator overloads, custom types,
// or strict type checking that this package does not expose directly.
func WithExprOptions(opts ...expr.Option) Option {
	return func(e *Engine) {
		e.options = append(e.options, opts...)
	}
}

// NewEngine returns an expr-backed Engine configured with the given options.
func NewEngine(opts ...Option) *Engine {
	e := &Engine{}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Compile implements script.Compiler. Expressions are compiled against an
// environment containing "state", "inputs", and any Engine-provided
// functions, with AllowUndefinedVariables enabled so that templates that
// reference not-yet-set state keys do not fail until evaluation.
func (e *Engine) Compile(ctx context.Context, code string) (script.Script, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	env := e.baseEnv()
	options := []expr.Option{
		expr.Env(env),
		expr.AllowUndefinedVariables(),
	}
	options = append(options, e.options...)

	program, err := expr.Compile(code, options...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExprCompile, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &compiledScript{engine: e, program: program}, nil
}

func (e *Engine) baseEnv() map[string]any {
	env := make(map[string]any, len(e.funcs)+2)
	env["state"] = map[string]any{}
	env["inputs"] = map[string]any{}
	for name, fn := range e.funcs {
		env[name] = fn
	}
	return env
}

type compiledScript struct {
	engine  *Engine
	program *vm.Program
}

// Evaluate implements script.Script.
func (s *compiledScript) Evaluate(ctx context.Context, globals map[string]any) (script.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	env := make(map[string]any, len(s.engine.funcs)+len(globals)+2)
	env["state"] = map[string]any{}
	env["inputs"] = map[string]any{}
	for name, fn := range s.engine.funcs {
		env[name] = fn
	}
	for name, value := range globals {
		env[name] = value
	}
	out, err := expr.Run(s.program, env)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExprEvaluate, err)
	}
	return &exprValue{v: out}, nil
}

// exprValue wraps an expr evaluation result as a script.Value. Expr returns
// plain Go values, so this wrapper delegates to the engine-neutral helpers
// in script for truthiness, iteration, and string formatting.
type exprValue struct {
	v any
}

func (v *exprValue) Value() any            { return v.v }
func (v *exprValue) IsTruthy() bool        { return script.IsTruthyValue(v.v) }
func (v *exprValue) Items() ([]any, error) { return script.EachValue(v.v) }

func (v *exprValue) String() string {
	if v.v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v.v)
}
