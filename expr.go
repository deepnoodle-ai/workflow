package workflow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	risor "github.com/deepnoodle-ai/risor/v3"
)

// Expressions are Risor v3 programs. Load parses each one, types it against
// the names its step can read (typecheck.go) and compiles it; Apply runs it.

// The limits of one expression run, so a hostile or careless expression
// fails deterministically instead of stalling a transaction. They are
// Risor's defaults, written out: steps and allocation units are counted
// exactly, the same in every Risor implementation.
var exprLimits = risor.Limits{Steps: 1_000_000, Alloc: 1_000_000, Depth: 200}

// concat(a, b, ...) joins lists. Null arguments are skipped.
var concatFunction = risor.Function{
	Name: "concat",
	Type: "(...lists: (T[] | null)[]) => T[]",
	Impl: func(c *risor.Call, args []any) (any, error) {
		out := []any{}
		for _, a := range args {
			if a == nil {
				continue
			}
			list, ok := a.([]any)
			if !ok {
				return nil, fmt.Errorf("concat: want lists, got %s", describe(a))
			}
			if err := c.Allocate(int64(len(list))); err != nil {
				return nil, err
			}
			out = append(out, list...)
		}
		return out, nil
	},
}

// baseEnv declares what every expression may call besides Risor's library.
// edgeEnv adds decide and choose, which only next: when and switch may use.
// runEnv is for compiling: it has every function, and Load adds the names
// of a definition's steps.
var (
	baseEnv = mustEnv(nil, exprLimits, concatFunction)
	edgeEnv = mustEnv(baseEnv, decisionFunctions...)
)

func mustEnv(parent *risor.Env, decls ...risor.Decl) *risor.Env {
	var env *risor.Env
	var err error
	if parent == nil {
		env, err = risor.NewEnv(decls...)
	} else {
		env, err = parent.With(decls...)
	}
	if err != nil {
		panic(err)
	}
	return env
}

// libraryNames are the functions and namespaces expressions can call:
// Risor's library, as the environment lists it, and the engine's own. A
// step named like one would hide it in every expression.
var libraryNames = func() map[string]bool {
	names := map[string]bool{"concat": true, fnDecide: true, fnChoose: true}
	lib := edgeEnv.LibraryNames()
	for _, n := range append(lib.Functions, lib.Namespaces...) {
		names[n] = true
	}
	return names
}()

// reservedName reports whether name cannot be a step name: a global, a
// function, or a word Risor reserves.
func reservedName(name string) bool {
	return reservedGlobal(name) || libraryNames[name]
}

// reservedGlobal reports whether name is a global Apply binds or a word
// Risor reserves (Risor's 01-language.md 2.4).
func reservedGlobal(name string) bool {
	switch name {
	case "input", "item", "index", "error", "run",
		"true", "false", "null", "const", "if", "else", "in", "not", "try", "catch", "match",
		"as", "is", "nil", "let", "var", "for", "while", "do", "break", "continue", "return",
		"function", "throw", "finally", "import":
		return true
	}
	return false
}

// program is a bare expression (each, when, switch) or one program of a
// template. x is the parsed program; p is compiled when Load succeeds.
type program struct {
	src string // the text that spans refer to: the expression or template
	x   *risor.Expr
	p   *risor.Program
}

func compileProgram(src string) (*program, error) {
	x, err := risor.Parse(src)
	if err != nil {
		return nil, exprErr(src, err)
	}
	return &program{src: src, x: x}, nil
}

func (p *program) eval(env map[string]any) (any, error) {
	return p.evalCtx(context.Background(), env)
}

// evalCtx is eval with a context, which carries a decision's answers.
func (p *program) evalCtx(ctx context.Context, env map[string]any) (any, error) {
	if p.p == nil {
		// Not compiled by Load: a program compiled on its own, in tests.
		var decls []risor.Decl
		for _, name := range p.names() {
			decls = append(decls, risor.Var(name, "any"))
		}
		renv, err := edgeEnv.With(decls...)
		if err == nil {
			err = p.compile(renv)
		}
		if err != nil {
			return nil, err
		}
	}
	v, err := p.p.Run(ctx, risor.Map(env))
	if err != nil {
		return nil, exprErr(p.src, err)
	}
	return v, nil
}

// names are the names the program reads.
func (p *program) names() []string { return p.x.Names() }

// compile compiles the program against env. Load has checked it.
func (p *program) compile(env *risor.Env) error {
	prog, err := env.Compile(p.x, risor.NoCheck)
	if err != nil {
		return exprErr(p.src, err)
	}
	p.p = prog
	return nil
}

// Templates use GitHub Actions' delimiters, which are also Risor's template
// delimiters (F2), so shell `${HOME}` and JavaScript template literals in a
// with: value pass through as text. `$${{` writes a literal `${{`.
const templateOpen = "${{"

// template is a compiled with:/set:/outputs: value. A string with no
// ${{ ... }} is a literal. A string that is exactly one ${{ ... }} keeps
// the expression's type (Risor's mode value), and so does one followed by
// a single "\n", the newline a YAML | block adds. Any other string renders
// as text (mode string). Maps and lists are walked.
type template struct {
	lit  any
	prog *program // tmplSingle and tmplText
	keys []string
	obj  map[string]*template
	list []*template
	kind int
}

const (
	tmplLiteral = iota
	tmplSingle
	tmplText
	tmplObject
	tmplList
)

func compileTemplate(v any) (*template, error) {
	switch t := v.(type) {
	case string:
		if !strings.Contains(t, templateOpen) {
			return &template{kind: tmplLiteral, lit: t}, nil
		}
		tm, err := risor.ParseTemplate(t)
		if err != nil {
			return nil, exprErr(t, err)
		}
		// Only a string that is exactly one ${{ ... }} keeps the
		// expression's type. Any literal text, even whitespace, makes it
		// a string, except one trailing "\n": a YAML | block adds it, and
		// a list must not quietly become text. "\r\n" and "\n\n" stay text.
		if x := tm.Single(); x != nil {
			return &template{kind: tmplSingle, prog: &program{src: t, x: x}}, nil
		}
		if body, ok := strings.CutSuffix(t, "\n"); ok && !strings.HasSuffix(body, "\n") &&
			!strings.HasSuffix(body, "\r") {
			if bt, err := risor.ParseTemplate(body); err == nil {
				if x := bt.Single(); x != nil {
					return &template{kind: tmplSingle, prog: &program{src: body, x: x}}, nil
				}
			}
		}
		return &template{kind: tmplText, prog: &program{src: t, x: tm.Expr()}}, nil
	case map[string]any:
		out := &template{kind: tmplObject, obj: make(map[string]*template, len(t))}
		for k, v := range t {
			c, err := compileTemplate(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out.keys = append(out.keys, k)
			out.obj[k] = c
		}
		slices.Sort(out.keys)
		return out, nil
	case []any:
		out := &template{kind: tmplList, list: make([]*template, len(t))}
		for i, v := range t {
			c, err := compileTemplate(v)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out.list[i] = c
		}
		return out, nil
	default:
		return &template{kind: tmplLiteral, lit: v}, nil
	}
}

func (t *template) eval(env map[string]any) (any, error) {
	switch t.kind {
	case tmplSingle, tmplText:
		return t.prog.eval(env)
	case tmplObject:
		out := make(map[string]any, len(t.obj))
		for _, k := range t.keys {
			v, err := t.obj[k].eval(env)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = v
		}
		return out, nil
	case tmplList:
		out := make([]any, len(t.list))
		for i, c := range t.list {
			v, err := c.eval(env)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = v
		}
		return out, nil
	default:
		return t.lit, nil
	}
}

// programs lists the template's programs in key order.
func (t *template) programs() []*program {
	var out []*program
	switch t.kind {
	case tmplSingle, tmplText:
		out = append(out, t.prog)
	case tmplObject:
		for _, k := range t.keys {
			out = append(out, t.obj[k].programs()...)
		}
	case tmplList:
		for _, c := range t.list {
			out = append(out, c.programs()...)
		}
	}
	return out
}

// idents returns every top-level identifier the template's expressions read.
func (t *template) idents() []string {
	var out []string
	for _, p := range t.programs() {
		out = append(out, p.names()...)
	}
	return out
}

// compilePrograms compiles any expression Load's check did not compile.
// The check compiles each program in the scope it types it in, so this
// finds none; it is a safety net, with one environment that declares each
// name an expression can read.
func (d *Definition) compilePrograms(p *problems) {
	var env *risor.Env
	compile := func(where string, progs ...*program) {
		for _, prog := range progs {
			if prog == nil || prog.p != nil {
				continue
			}
			if env == nil {
				decls := []risor.Decl{
					risor.Var("input", "any"), risor.Var("item", "any"), risor.Var("index", "any"),
					risor.Var("error", "any"), risor.Var("run", "any"),
				}
				for _, name := range sortedKeys(d.byName) {
					decls = append(decls, risor.Var(name, "any"))
				}
				var err error
				if env, err = edgeEnv.With(decls...); err != nil {
					p.add("", "expressions: %v", err)
					return
				}
			}
			if err := prog.compile(env); err != nil {
				p.add(where, "%v", err)
			}
		}
	}
	tmpl := func(where string, t *template) {
		if t != nil {
			compile(where, t.programs()...)
		}
	}
	all := append(append(append([]*Step{}, d.Steps...), d.OnFailure...), d.Finally...)
	for _, s := range all {
		compile(s.Name, s.each, s.swtch)
		compile(s.Name, s.edges...)
		tmpl(s.Name, s.with)
		tmpl(s.Name, s.value)
		for _, c := range s.decisions {
			compile(s.Name, c.subject)
		}
	}
	tmpl("", d.outputs)
}

// exprErr turns a Risor failure into an error whose text a workflow author
// can act on: each diagnostic, with the text it points at.
func exprErr(src string, err error) error {
	re, ok := err.(*risor.Error)
	if !ok {
		return err
	}
	msgs := make([]string, len(re.Diagnostics))
	for i, d := range re.Diagnostics {
		msgs[i] = diagText(src, d)
	}
	return &exprFailure{diags: re.Diagnostics, msg: strings.Join(msgs, "; ")}
}

// exprFailure is a failed parse, check or run of an expression.
type exprFailure struct {
	diags []risor.Diagnostic
	msg   string
}

func (e *exprFailure) Error() string { return e.msg }

// diagText is one diagnostic as a sentence: Risor's message, after the
// text the diagnostic points at unless the message starts with it.
func diagText(src string, d risor.Diagnostic) string {
	if d.Span != nil {
		if s := spanText(src, *d.Span); s != "" && !strings.HasPrefix(d.Message, s) {
			return "`" + s + "`: " + d.Message
		}
	}
	return d.Message
}

// spanText is the text a span covers. Columns count code points.
func spanText(src string, sp risor.Span) string {
	line, col := 1, 1
	start, end := -1, -1
	for i, c := range src {
		if line == sp.Start.Line && col == sp.Start.Column {
			start = i
		}
		if line == sp.End.Line && col == sp.End.Column {
			end = i
			break
		}
		if c == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	if end < 0 && line == sp.End.Line && col == sp.End.Column {
		end = len(src)
	}
	if start < 0 || end < start {
		return ""
	}
	return src[start:end]
}
