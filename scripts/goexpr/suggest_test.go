package goexpr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuggest_UndefinedIdentDidYouMean(t *testing.T) {
	env := map[string]any{
		"username": "alice",
		"age":      30,
	}
	_, err := Eval(ctx, "usernmae", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `undefined identifier "usernmae"`)
	require.Contains(t, err.Error(), `did you mean "username"?`)
}

func TestSuggest_UndefinedIdentAvailableList(t *testing.T) {
	// Small candidate set with no close match: the hint falls back to
	// listing the available names. Here we pass WithoutBuiltins so
	// the candidate set is just the env entries — well under the
	// 8-name cap that formatHint uses to decide whether a list is
	// short enough to be useful.
	// With WithoutBuiltins, the only candidates are the env entries
	// plus the 6 higher-order form names, so two env entries keeps
	// the total under the 8-name cap.
	e := New(WithoutBuiltins())
	env := map[string]any{"alpha": 1, "beta": 2}
	_, err := e.Eval(ctx, "zzz", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `undefined identifier "zzz"`)
	require.Contains(t, err.Error(), "available:")
	require.Contains(t, err.Error(), "alpha")
	require.Contains(t, err.Error(), "beta")
}

func TestSuggest_MissingStructField(t *testing.T) {
	p := person{Name: "Alice", Age: 30}
	env := map[string]any{"p": p}
	_, err := Eval(ctx, "p.Nmae", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `field "Nmae" not found`)
	require.Contains(t, err.Error(), `did you mean "Name"?`)
}

func TestSuggest_MissingMapKey(t *testing.T) {
	env := map[string]any{
		"obj": map[string]any{"name": "Alice", "age": 30},
	}
	_, err := Eval(ctx, "obj.naem", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `key "naem" not found`)
	require.Contains(t, err.Error(), `did you mean "name"?`)
}

func TestSuggest_MissingMapKeyByIndex(t *testing.T) {
	env := map[string]any{
		"obj": map[string]any{"name": "Alice"},
	}
	_, err := Eval(ctx, `obj["naem"]`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `key "naem" not found`)
	require.Contains(t, err.Error(), `did you mean "name"?`)
}

func TestSuggest_UnknownFunction(t *testing.T) {
	// `lowre` should suggest `lower` (builtin).
	_, err := Eval(ctx, `lowre("FOO")`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `unknown function "lowre"`)
	require.Contains(t, err.Error(), `did you mean "lower"?`)
}

func TestSuggest_UnknownFunctionSuggestsForm(t *testing.T) {
	// `fliter` should suggest the `filter` higher-order form.
	_, err := Eval(ctx, `fliter([1, 2], it > 0)`, nil)
	// Composite literal [1,2] is unsupported, so the error will come
	// from the evaluator after parsing succeeds. What we actually want
	// to test is that a bad name `fliter` suggests `filter`. Use a
	// simpler form that doesn't require a composite literal.
	_ = err
	env := map[string]any{"xs": []any{int64(1), int64(2)}}
	_, err = Eval(ctx, "fliter(xs, it > 0)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `unknown function "fliter"`)
	require.Contains(t, err.Error(), `did you mean "filter"?`)
}

func TestSuggest_UnknownMethod(t *testing.T) {
	env := map[string]any{"p": &person{Name: "Alice"}}
	// No method `greet` exists — but also no field. Test the hint
	// format; struct only exports fields here, so the suggestion
	// should point at one of them.
	_, err := Eval(ctx, "p.Gree()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

// TestPreprocess_MethodCallUnaffected verifies that a method call
// `x.map(...)` is NOT rewritten by the source preprocessor — users
// with types that expose a method literally named `map` should still
// be able to invoke it.
func TestPreprocess_MethodCallUnaffected(t *testing.T) {
	type withMap struct{}
	env := map[string]any{
		"obj": map[string]any{
			"map": func(x int) int { return x * 10 },
		},
	}
	got, err := Eval(ctx, "obj.map(5)", env)
	require.NoError(t, err)
	require.Equal(t, int(50), got)
	_ = withMap{}
}

func TestPreprocess_FastPath(t *testing.T) {
	// Expressions without the literal substring "map" must not touch
	// the scanner. This test is really a behavior check: a normal
	// expression should compile and evaluate correctly.
	got, err := Eval(ctx, "1 + 2 * 3", nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), got)
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if len(n) == 0 {
			continue
		}
		if indexOf(haystack, n) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	// Tiny, dependency-free version of strings.Index so this test
	// file doesn't pull in strings just for one helper.
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
