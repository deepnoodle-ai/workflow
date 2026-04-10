package goexpr

import (
	"context"
	"fmt"
	"go/ast"
	"reflect"
)

// itEnv is a scope chain used by the higher-order special forms. It
// binds `it` (current element) and `index` (0-based position) while
// delegating every other identifier to the parent env. Scopes nest
// naturally: an itEnv whose parent is itself an itEnv resolves inner
// `it`/`index` to the innermost loop, matching lexical expectations
// for `map(users, map(it.friends, it.name))`.
type itEnv struct {
	parent any
	it     any
	index  int64
}

// higherOrderForm is the uniform signature for every built-in
// special form so evalCall can dispatch via a single map lookup.
type higherOrderForm func(*Program, context.Context, *ast.CallExpr, any, int) (any, error)

// higherOrderForms maps builtin names to their special-form
// evaluators. Unlike ordinary functions in the engine's funcs map,
// these receive the raw *ast.CallExpr so they can re-evaluate the
// predicate argument per element with `it`/`index` in scope.
//
// These names are active by default but are not reserved: a
// user-registered function or an env entry of the same name shadows
// the form, matching the identifier-resolution order used everywhere
// else in goexpr. See the dispatch in evalCall.
//
// Populated in init rather than as a var initializer because the
// function bodies transitively reach p.evalCall, which reads this
// map — Go's initialization-cycle check flags that as a direct cycle.
var higherOrderForms map[string]higherOrderForm

func init() {
	higherOrderForms = map[string]higherOrderForm{
		// map is rewritten by preprocessSource to the internal
		// identifier before parsing because `map` is a Go keyword.
		mapFormName: formMap,
		"filter":    formFilter,
		"any":       formAny,
		"all":       formAll,
		"find":      formFind,
		"count":     formCount,
	}
}

// higherOrderNames is the user-visible list of special-form builtins.
// It drives "did you mean" hints so users see the spelling they would
// actually type (`map`, not the internal rewritten name).
var higherOrderNames = []string{"map", "filter", "any", "all", "find", "count"}

// iterItems evaluates collExpr and converts the result to a []any for
// predicate iteration. nil is treated as an empty list so
// `map(nil, it)` / `filter(nil, it > 0)` return empty without error.
// Maps and other non-list shapes return a user-friendly error naming
// the form, so users do not have to guess which argument was wrong.
func (p *Program) iterItems(ctx context.Context, name string, collExpr ast.Expr, env any, depth int) ([]any, error) {
	coll, err := p.eval(ctx, collExpr, env, depth)
	if err != nil {
		return nil, err
	}
	if coll == nil {
		return nil, nil
	}
	if s, ok := coll.([]any); ok {
		return s, nil
	}
	rv := reflect.ValueOf(coll)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %s expects a list as its first argument, got %T",
		ErrEvaluate, name, coll)
}

// checkFormArity reports a consistent error across every form.
func checkFormArity(name string, got int) error {
	if got == 2 {
		return nil
	}
	return fmt.Errorf("%w: %s expects 2 arguments (collection, predicate), got %d",
		ErrEvaluate, name, got)
}

// forEach is the shared loop used by every higher-order form. The
// body closure observes `ctx.Err()` through `p.eval` automatically,
// so a cancelled context aborts the iteration at the next element.
func (p *Program) forEach(
	ctx context.Context,
	items []any,
	predicate ast.Expr,
	env any,
	depth int,
	body func(item any, result any) (stop bool, err error),
) error {
	scope := &itEnv{parent: env}
	for i, item := range items {
		scope.it = item
		scope.index = int64(i)
		v, err := p.eval(ctx, predicate, scope, depth)
		if err != nil {
			return err
		}
		stop, err := body(item, v)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

func formMap(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("map", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "map", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	err = p.forEach(ctx, items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
		out = append(out, v)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func formFilter(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("filter", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "filter", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	err = p.forEach(ctx, items, n.Args[1], env, depth, func(item any, v any) (bool, error) {
		if isTruthy(v) {
			out = append(out, item)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func formAny(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("any", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "any", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	found := false
	err = p.forEach(ctx, items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
		if isTruthy(v) {
			found = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func formAll(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("all", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "all", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	ok := true
	err = p.forEach(ctx, items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
		if !isTruthy(v) {
			ok = false
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return ok, nil
}

func formFind(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("find", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "find", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	var match any
	matched := false
	err = p.forEach(ctx, items, n.Args[1], env, depth, func(item any, v any) (bool, error) {
		if isTruthy(v) {
			match = item
			matched = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, nil
	}
	return match, nil
}

func formCount(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("count", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "count", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	var total int64
	err = p.forEach(ctx, items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
		if isTruthy(v) {
			total++
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return total, nil
}
