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
//	result, err := goexpr.Eval(ctx, "user.age >= 18", map[string]any{"user": u})
//
// Basic use with a struct env (fields and methods become callable):
//
//	type Env struct{ Count int }
//	func (e Env) Double() int { return e.Count * 2 }
//
//	result, err := goexpr.Eval(ctx, "Double() > 5", Env{Count: 3})
//
// Compile once, evaluate many:
//
//	program, err := goexpr.Compile("state.count * inputs.multiplier")
//	v, err := program.Run(ctx, env)
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
//	v, err := e.Eval(ctx, "upper(state.name)", env)
package goexpr

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/deepnoodle-ai/workflow/script"
)

// ErrCompile wraps parse failures so callers can match with errors.Is.
var ErrCompile = errors.New("goexpr: compile error")

// ErrEvaluate wraps runtime failures so callers can match with errors.Is.
var ErrEvaluate = errors.New("goexpr: evaluate error")

// MaxSourceLength is the maximum number of bytes Compile will accept.
// Longer inputs return ErrCompile without invoking the Go parser, which
// protects against adversarial nesting depths that could exhaust the
// parser's own stack.
const MaxSourceLength = 64 * 1024

// MaxEvalDepth bounds the recursion depth of Program.Run. Expressions
// whose AST nests deeper return ErrEvaluate. 256 is enough for any
// hand-written expression and keeps the Go stack well under 1 MiB.
const MaxEvalDepth = 256

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
// Program is immutable and safe for concurrent use. Input longer than
// MaxSourceLength is rejected without calling the parser.
func (e *Engine) Compile(code string) (*Program, error) {
	if len(code) > MaxSourceLength {
		return nil, fmt.Errorf("%w: source length %d exceeds maximum %d",
			ErrCompile, len(code), MaxSourceLength)
	}
	parsed := preprocessSource(code)
	node, err := parser.ParseExpr(parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompile, err)
	}
	return &Program{source: code, root: node, funcs: e.funcs}, nil
}

// mapFormName is the internal identifier that source-level `map(`
// calls are rewritten to before parsing. Go's parser treats `map` as
// a keyword (the start of a map-type literal), so goexpr cannot
// accept `map(xs, pred)` as-is; preprocessSource rewrites the token
// to this name and higherOrderForms dispatches on it. The value is
// deliberately ugly so it cannot collide with a user-facing
// identifier.
const mapFormName = "__goexpr_map__"

// preprocessSource rewrites Go keyword tokens that goexpr wants to
// accept as ordinary identifiers. The only such token is `map`: Go's
// parser treats `map` as the start of a map-type literal everywhere
// it appears, so goexpr cannot accept `map(xs, pred)`, `obj.map(...)`,
// or any other construct that names `map` as an identifier.
//
// The rewrite replaces every `map` token with mapFormName *unless*
// the next token is `[`, which would indicate a Go map type literal
// like `map[string]int{}`. Composite literals are unsupported by
// goexpr anyway, so leaving that one case alone lets the parser emit
// its normal error for unsupported syntax. Selector, call, and
// method-call forms all carry the rewritten identifier through to
// the evaluator, which translates it back to "map" in lookups and
// error messages via mapFormDisplayName.
func preprocessSource(src string) string {
	// Fast path: most goexpr expressions do not contain `map` at all,
	// so the scanner pass is skipped. `strings.Contains` on a short
	// expression is much cheaper than spinning up go/scanner.
	if !strings.Contains(src, "map") {
		return src
	}
	fs := token.NewFileSet()
	file := fs.AddFile("goexpr", fs.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, 0)

	type tokInfo struct {
		pos token.Pos
		tok token.Token
	}
	var toks []tokInfo
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		toks = append(toks, tokInfo{pos, tok})
	}

	var out strings.Builder
	out.Grow(len(src) + 16)
	last := 0
	for i := 0; i < len(toks); i++ {
		if toks[i].tok != token.MAP {
			continue
		}
		// Leave `map[...]` alone so Go map type literals continue to
		// produce a normal "unsupported syntax" error at eval time.
		if i+1 < len(toks) && toks[i+1].tok == token.LBRACK {
			continue
		}
		off := file.Offset(toks[i].pos)
		out.WriteString(src[last:off])
		out.WriteString(mapFormName)
		last = off + len("map")
	}
	if last == 0 {
		return src
	}
	out.WriteString(src[last:])
	return out.String()
}

// displayIdent converts an internal rewritten identifier back to the
// name the user originally typed, for use in error messages and
// field/method lookups. Currently this only matters for `map`.
func displayIdent(name string) string {
	if name == mapFormName {
		return "map"
	}
	return name
}

// Eval compiles and runs code in one step. Prefer Compile+Run when the
// same expression will be evaluated multiple times.
//
// env may be a map[string]any, a struct, or a pointer to a struct — see
// Program.Run for details. ctx is threaded into evaluation and auto-
// injected into registered functions whose first parameter is
// context.Context.
func (e *Engine) Eval(ctx context.Context, code string, env any) (any, error) {
	p, err := e.Compile(code)
	if err != nil {
		return nil, err
	}
	return p.Run(ctx, env)
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
// goexpr.New().Eval(ctx, code, env). env may be a map[string]any, a
// struct, or a pointer to a struct.
func Eval(ctx context.Context, code string, env any) (any, error) {
	return defaultEngine.Eval(ctx, code, env)
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
	v, err := s.program.Run(ctx, globals)
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
