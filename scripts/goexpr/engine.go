// Package goexpr is a zero-dependency expression evaluator for the
// workflow engine, built on top of go/parser. It accepts the subset of Go
// expression syntax useful for workflow conditions, templates, and
// parameter interpolation — identifiers, selectors, index expressions,
// arithmetic, comparisons, logical operators, and calls to registered
// functions.
//
// goexpr is intentionally small: it adds no external dependencies to the
// workflow module and implements script.Compiler so it can plug directly
// into workflow.ExecutionOptions.ScriptCompiler.
//
// Basic use with a map env:
//
//	result, err := goexpr.Eval("user.age >= 18", map[string]any{"user": u})
//
// Basic use with a struct env (fields and methods become callable):
//
//	type Env struct{ Count int }
//	func (e Env) Double() int { return e.Count * 2 }
//
//	result, err := goexpr.Eval("Double() > 5", Env{Count: 3})
//
// Compile once, evaluate many:
//
//	program, err := goexpr.Compile("state.count * inputs.multiplier")
//	v, err := program.Run(env)
//
// Workflow integration:
//
//	exec, err := workflow.NewExecution(workflow.ExecutionOptions{
//	    Workflow:       wf,
//	    ScriptCompiler: goexpr.New().Compiler(),
//	})
//
// Custom functions:
//
//	e := goexpr.New(goexpr.WithFunctions(map[string]any{
//	    "upper": strings.ToUpper,
//	}))
//	v, err := e.Eval("upper(state.name)", env)
package goexpr

import (
	"context"
	"errors"
	"fmt"
	"go/parser"

	"github.com/deepnoodle-ai/workflow/script"
)

// ErrCompile wraps parse failures so callers can match with errors.Is.
var ErrCompile = errors.New("goexpr: compile error")

// ErrEvaluate wraps runtime failures so callers can match with errors.Is.
var ErrEvaluate = errors.New("goexpr: evaluate error")

// Engine compiles expressions into reusable programs. An Engine is safe
// for concurrent use once configured. Create one with New.
type Engine struct {
	funcs map[string]any
}

// Option configures an Engine.
type Option func(*Engine)

// New returns an Engine pre-configured with the standard builtin functions
// (see Builtins). Additional options can extend or replace them.
func New(opts ...Option) *Engine {
	e := &Engine{funcs: Builtins()}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WithFunctions registers the given functions as callable identifiers in
// every compiled expression. Entries merge into (and override) whatever is
// already registered — including the default builtins.
//
// Functions may take any Go types as arguments; goexpr converts evaluated
// values to the declared parameter types at call time. Return signatures of
// `T`, `(T, error)`, and `()` are supported. Variadic functions are also
// supported.
//
// The names "state" and "inputs" are reserved by the workflow engine and
// registering them will panic.
func WithFunctions(funcs map[string]any) Option {
	return func(e *Engine) {
		for name, fn := range funcs {
			if _, reserved := reservedBindings[name]; reserved {
				panic(fmt.Sprintf("goexpr: function name %q is reserved by the workflow engine", name))
			}
			e.funcs[name] = fn
		}
	}
}

// WithoutBuiltins starts the engine with an empty function set instead of
// the defaults. Use it when you need strict control over the call surface,
// then layer on your own functions via WithFunctions.
func WithoutBuiltins() Option {
	return func(e *Engine) {
		e.funcs = map[string]any{}
	}
}

var reservedBindings = map[string]struct{}{
	"state":  {},
	"inputs": {},
}

// Compile parses an expression once for repeated evaluation. The returned
// Program is immutable and safe for concurrent use.
func (e *Engine) Compile(code string) (*Program, error) {
	node, err := parser.ParseExpr(code)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompile, err)
	}
	return &Program{source: code, root: node, funcs: e.funcs}, nil
}

// Eval compiles and runs code in one step. Prefer Compile+Run when the
// same expression will be evaluated multiple times.
//
// env may be a map[string]any, a struct, or a pointer to a struct — see
// Program.Run for details.
func (e *Engine) Eval(code string, env any) (any, error) {
	p, err := e.Compile(code)
	if err != nil {
		return nil, err
	}
	return p.Run(env)
}

// Compiler returns a script.Compiler view of this Engine for use with
// workflow.ExecutionOptions.ScriptCompiler.
func (e *Engine) Compiler() script.Compiler {
	return scriptCompiler{engine: e}
}

// --- package-level convenience over a default engine ---

var defaultEngine = New()

// Compile is shorthand for a default engine's Compile. Equivalent to
// goexpr.New().Compile(code).
func Compile(code string) (*Program, error) {
	return defaultEngine.Compile(code)
}

// Eval is shorthand for a default engine's Eval. Equivalent to
// goexpr.New().Eval(code, env). env may be a map[string]any, a struct, or
// a pointer to a struct.
func Eval(code string, env any) (any, error) {
	return defaultEngine.Eval(code, env)
}

// --- script.Compiler adapter ---

type scriptCompiler struct{ engine *Engine }

func (s scriptCompiler) Compile(ctx context.Context, code string) (script.Script, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := s.engine.Compile(code)
	if err != nil {
		return nil, err
	}
	return &scriptAdapter{program: p}, nil
}

type scriptAdapter struct{ program *Program }

func (s *scriptAdapter) Evaluate(ctx context.Context, globals map[string]any) (script.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v, err := s.program.Run(globals)
	if err != nil {
		return nil, err
	}
	return &scriptValue{v: v}, nil
}

// scriptValue wraps a goexpr result as a script.Value, delegating to the
// engine-neutral helpers in script so conventions match the other engines.
type scriptValue struct{ v any }

func (v *scriptValue) Value() any            { return v.v }
func (v *scriptValue) IsTruthy() bool        { return script.IsTruthyValue(v.v) }
func (v *scriptValue) Items() ([]any, error) { return script.EachValue(v.v) }
func (v *scriptValue) String() string {
	if v.v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v.v)
}
