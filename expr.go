package workflow

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

// evalBudget bounds the work one expression may do, so a hostile or careless
// expression fails deterministically instead of stalling a transaction.
const evalBudget = 1_000_000

// Functions available to every expression, beyond expr's builtins.
var exprFunctions = map[string]any{
	// coalesce(a, b, ...) returns its first argument that is not nil:
	// coalesce(report, approve).verdict reads whichever ran.
	"coalesce": expr.Func(func(_ context.Context, args []any) (any, error) {
		for _, a := range args {
			if a != nil {
				return a, nil
			}
		}
		return nil, nil
	}),
	// concat(a, b, ...) joins lists. Nil arguments are skipped and scalars
	// are appended as single items.
	"concat": expr.Func(func(_ context.Context, args []any) (any, error) {
		out := []any{}
		for _, a := range args {
			if a == nil {
				continue
			}
			rv := reflect.ValueOf(a)
			if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
				for i := 0; i < rv.Len(); i++ {
					out = append(out, rv.Index(i).Interface())
				}
				continue
			}
			out = append(out, a)
		}
		return out, nil
	}),
}

// allFunctions is every function an expression can call: expr's builtins
// and its string, math and collection groups (all pure), plus ours.
var allFunctions = func() map[string]any {
	all := map[string]any{}
	for _, group := range []map[string]any{
		expr.Builtins(), expr.StringFuncs(), expr.MathFuncs(), expr.CollectionFuncs(), exprFunctions,
	} {
		maps.Copy(all, group)
	}
	return all
}()

var exprOptions = []expr.Option{
	expr.WithFunctions(allFunctions),
	expr.WithEvalBudget(evalBudget),
}

// reservedNames cannot be step names: they are globals or functions.
func reservedName(name string) bool {
	switch name {
	case "input", "item", "index", "error", "it", "true", "false", "nil",
		"map", "filter", "flatMap", "any", "all", "find", "count", "sortBy", "try", "if":
		return true
	}
	_, ok := allFunctions[name]
	return ok
}

// program is a compiled bare expression (each, when).
type program struct {
	src string
	p   *expr.Program
}

func compileProgram(src string) (*program, error) {
	p, err := expr.Compile(src, exprOptions...)
	if err != nil {
		return nil, err
	}
	return &program{src: src, p: p}, nil
}

func (p *program) eval(env map[string]any) (any, error) {
	return p.p.Run(context.Background(), env)
}

// Templates use GitHub Actions' delimiters, so shell `${HOME}` and
// JavaScript template literals in a with: value pass through as text.
// `$${{` writes a literal `${{`.
const (
	templateOpen  = "${{"
	templateClose = "}}"
)

// template is a compiled with:/set:/outputs: value. A string with no
// ${{ ... }} is a literal. A string that is exactly one ${{ ... }} keeps
// the expression's type. Any other string renders as text. Maps and lists
// are walked.
type template struct {
	lit    any
	single *expr.Program
	text   *expr.Template
	keys   []string
	obj    map[string]*template
	list   []*template
	kind   int
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
		opts := append([]expr.Option{expr.WithTemplateDelimiters(templateOpen, templateClose)}, exprOptions...)
		tm, err := expr.NewTemplate(t, opts...)
		if err != nil {
			return nil, err
		}
		// Only a string that is exactly one ${{ ... }} keeps the
		// expression's type. Any literal text, even whitespace, makes it
		// a string.
		if segs := tm.Segments(); len(segs) == 1 && segs[0].Program != nil {
			return &template{kind: tmplSingle, single: segs[0].Program}, nil
		}
		return &template{kind: tmplText, text: tm}, nil
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
		sort.Strings(out.keys)
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
	case tmplSingle:
		return t.single.Run(context.Background(), env)
	case tmplText:
		return t.text.Render(context.Background(), env)
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

// idents returns every top-level identifier the template's expressions read.
func (t *template) idents() []string {
	var out []string
	switch t.kind {
	case tmplSingle:
		out = t.single.Identifiers()
	case tmplText:
		for _, seg := range t.text.Segments() {
			if seg.Program != nil {
				out = append(out, seg.Program.Identifiers()...)
			}
		}
	case tmplObject:
		for _, k := range t.keys {
			out = append(out, t.obj[k].idents()...)
		}
	case tmplList:
		for _, c := range t.list {
			out = append(out, c.idents()...)
		}
	}
	return out
}
