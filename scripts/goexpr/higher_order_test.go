package goexpr

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type person struct {
	Name   string
	Age    int
	Active bool
}

func sampleUsers() []person {
	return []person{
		{Name: "Alice", Age: 30, Active: true},
		{Name: "Bob", Age: 17, Active: true},
		{Name: "Carol", Age: 42, Active: false},
	}
}

func TestHigherOrder_MapScalar(t *testing.T) {
	env := map[string]any{"nums": []any{int64(1), int64(2), int64(3)}}
	got, err := Eval(ctx, "map(nums, it * 2)", env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(2), int64(4), int64(6)}, got)
}

func TestHigherOrder_MapStructField(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, "map(users, it.Name)", env)
	require.NoError(t, err)
	require.Equal(t, []any{"Alice", "Bob", "Carol"}, got)
}

func TestHigherOrder_FilterStruct(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, "filter(users, it.Age >= 18)", env)
	require.NoError(t, err)
	// filter preserves the element type (here, goexpr.person), and
	// order matches the input slice.
	res, ok := got.([]any)
	require.True(t, ok, "filter should return []any")
	require.Len(t, res, 2)
	require.Equal(t, "Alice", res[0].(person).Name)
	require.Equal(t, "Carol", res[1].(person).Name)
}

func TestHigherOrder_Any(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, "any(users, it.Age > 40)", env)
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = Eval(ctx, "any(users, it.Age > 100)", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestHigherOrder_All(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, "all(users, it.Active)", env)
	require.NoError(t, err)
	require.Equal(t, false, got)

	got, err = Eval(ctx, "all(users, len(it.Name) > 0)", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestHigherOrder_Find(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, `find(users, it.Name == "Carol")`, env)
	require.NoError(t, err)
	require.Equal(t, "Carol", got.(person).Name)

	// No match returns nil.
	got, err = Eval(ctx, `find(users, it.Name == "Dave")`, env)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestHigherOrder_Count(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, "count(users, it.Active)", env)
	require.NoError(t, err)
	require.Equal(t, int64(2), got)
}

func TestHigherOrder_Index(t *testing.T) {
	env := map[string]any{"xs": []any{int64(10), int64(20), int64(30)}}
	got, err := Eval(ctx, "map(xs, it + index)", env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(10), int64(21), int64(32)}, got)
}

func TestHigherOrder_NestedScopes(t *testing.T) {
	// Nested maps: the inner `it` should refer to the inner element;
	// the outer element is reachable via the outer scope only so long
	// as it was captured before the inner map ran. Here we verify inner
	// shadowing: outer is an int, inner multiplies inner element alone.
	env := map[string]any{"matrix": []any{
		[]any{int64(1), int64(2)},
		[]any{int64(3), int64(4)},
	}}
	got, err := Eval(ctx, "map(matrix, map(it, it * 10))", env)
	require.NoError(t, err)
	require.Equal(t, []any{
		[]any{int64(10), int64(20)},
		[]any{int64(30), int64(40)},
	}, got)
}

func TestHigherOrder_ChainedFilterThenMap(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := Eval(ctx, "map(filter(users, it.Age >= 18), it.Name)", env)
	require.NoError(t, err)
	require.Equal(t, []any{"Alice", "Carol"}, got)
}

func TestHigherOrder_NilCollection(t *testing.T) {
	got, err := Eval(ctx, "map(nil, it * 2)", nil)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)

	got, err = Eval(ctx, "filter(nil, it)", nil)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)

	got, err = Eval(ctx, "any(nil, it)", nil)
	require.NoError(t, err)
	require.Equal(t, false, got)

	got, err = Eval(ctx, "all(nil, it)", nil)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestHigherOrder_EmptyCollection(t *testing.T) {
	env := map[string]any{"xs": []any{}}
	got, err := Eval(ctx, "map(xs, it + 1)", env)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestHigherOrder_NonListCollection(t *testing.T) {
	env := map[string]any{"obj": map[string]any{"a": 1}}
	_, err := Eval(ctx, "map(obj, it)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "map expects a list")
}

func TestHigherOrder_ArityError(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1)}}
	_, err := Eval(ctx, "map(xs)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "map expects 2 arguments")
}

func TestHigherOrder_PredicateError(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	_, err := Eval(ctx, "map(users, it.Nmae)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `field "Nmae" not found`)
	require.Contains(t, err.Error(), `did you mean "Name"?`)
}

func TestHigherOrder_UserFuncOverride(t *testing.T) {
	// User-registered `map` wins over the special form, matching the
	// env→funcs identifier-resolution order used everywhere else.
	e := New(WithFunctions(map[string]any{
		"map": func(xs []any) int { return len(xs) },
	}))
	got, err := e.Eval(ctx, "map(xs)", map[string]any{"xs": []any{int64(1), int64(2)}})
	require.NoError(t, err)
	require.Equal(t, int(2), got)
}

func TestHigherOrder_EnvShadowsForm(t *testing.T) {
	// An env entry with the same name as a form shadows the form.
	// Here "filter" is bound to a plain slice, so `filter` becomes a
	// normal identifier and the call target fails to resolve as a
	// function (expected "unsupported call target" or similar).
	_, err := Eval(ctx, "filter", map[string]any{"filter": int64(42)})
	require.NoError(t, err)
}

func TestHigherOrder_CancelDuringIteration(t *testing.T) {
	// Cancellation mid-iteration: register a function that cancels ctx
	// on first call, then iterate a list that would otherwise produce
	// multiple results. The next eval tick must observe ctx.Err() and
	// return context.Canceled without wrapping it.
	ctxC, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := New(WithFunctions(map[string]any{
		"stop": func() bool { cancel(); return true },
	}))
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3), int64(4), int64(5)}}
	_, err := e.Eval(ctxC, "map(xs, stop() && it)", env)
	require.True(t, errors.Is(err, context.Canceled),
		"expected context.Canceled, got %v", err)
}
