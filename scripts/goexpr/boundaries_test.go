package goexpr

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests fill coverage gaps and pin down the sharp edges of goexpr's
// evaluation semantics. When a behavior is ambiguous or surprising, the
// test documents the current behavior with a comment so intentional
// changes are easy to spot in a diff.

// --- Unary operators ---

func TestUnary_NegateFloat(t *testing.T) {
	got, err := Eval("-3.14", nil)
	require.NoError(t, err)
	require.Equal(t, -3.14, got)
}

func TestUnary_NegateUnsupported(t *testing.T) {
	_, err := Eval(`-"hi"`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "cannot negate")
}

// Unary `+` requires a numeric operand. `+42` is a no-op, `+"abc"` errors.
func TestUnary_PlusRequiresNumeric(t *testing.T) {
	got, err := Eval("+42", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = Eval("+3.14", nil)
	require.NoError(t, err)
	require.Equal(t, 3.14, got)

	_, err = Eval(`+"abc"`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unary +")
}

// `!x` uses truthiness semantics, not strict bool. Documenting the
// behavior — this is consistent with isTruthy across the engine.
func TestUnary_NotUsesTruthiness(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"!0", true},
		{`!""`, true},
		{`!"x"`, false},
		{"!1", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// --- Division, modulo, and numeric edge cases ---

func TestBinary_DivideByZero(t *testing.T) {
	cases := []string{"1 / 0", "1 % 0", "1.0 / 0.0"}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := Eval(expr, nil)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestBinary_FloatModulo(t *testing.T) {
	got, err := Eval("10.5 % 2", nil)
	require.NoError(t, err)
	require.InDelta(t, 0.5, got, 1e-9)

	got, err = Eval("10 % 3.5", nil)
	require.NoError(t, err)
	require.InDelta(t, 3.0, got, 1e-9)

	_, err = Eval("1.0 % 0.0", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "modulo by zero")
}

func TestBinary_MixedTypesReportError(t *testing.T) {
	_, err := Eval(`"a" + 1`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not supported")
}

// --- Literals ---

func TestLiteral_HexAndOctal(t *testing.T) {
	got, err := Eval("0xff", nil)
	require.NoError(t, err)
	require.Equal(t, int64(255), got)

	got, err = Eval("0o17", nil)
	require.NoError(t, err)
	require.Equal(t, int64(15), got)
}

func TestLiteral_Char(t *testing.T) {
	// CHAR literals evaluate to their rune value as an int64, matching Go.
	got, err := Eval("'a'", nil)
	require.NoError(t, err)
	require.Equal(t, int64('a'), got)

	got, err = Eval(`'\n'`, nil)
	require.NoError(t, err)
	require.Equal(t, int64('\n'), got)
}

func TestLiteral_ImagUnsupported(t *testing.T) {
	// Imaginary literals parse but goexpr doesn't model complex numbers.
	_, err := Eval("1i", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLiteral_IntOverflow(t *testing.T) {
	_, err := Eval("99999999999999999999", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Equality edge cases ---

// Typed nils (nil slice, nil map, nil pointer) compare equal to the nil
// literal — goexpr checks nilability via reflect so interface-level
// wrapping does not hide the nil.
func TestEquality_TypedNilEqualsNil(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{
		"slice": []any(nil),
		"dict":  map[string]any(nil),
		"ptr":   (*S)(nil),
	}
	for _, expr := range []string{"slice == nil", "dict == nil", "ptr == nil"} {
		got, err := Eval(expr, env)
		require.NoError(t, err, expr)
		require.Equal(t, true, got, expr)
	}

	// Non-nil values of nilable types still compare false against nil.
	env = map[string]any{"slice": []any{1}}
	got, err := Eval("slice == nil", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestEquality_NilEqualsNil(t *testing.T) {
	got, err := Eval("nil == nil", nil)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestEquality_CrossNumericTypes(t *testing.T) {
	// int via reflection coerces to int64/float64 in looseEqual.
	env := map[string]any{"a": int32(7), "b": int64(7), "c": float64(7)}
	for _, expr := range []string{"a == b", "a == c", "b == c"} {
		got, err := Eval(expr, env)
		require.NoError(t, err, expr)
		require.Equal(t, true, got, expr)
	}
}

func TestEquality_DifferentTypesNotEqual(t *testing.T) {
	// Comparable but different types: returns false, no error.
	env := map[string]any{"a": "1", "b": 1}
	got, err := Eval("a == b", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// --- Indexing ---

// String indexing is rune-based: s[i] returns the i-th Unicode code
// point as a one-character string. Matches scripting-language
// expectations and is round-trip safe for non-ASCII text.
func TestIndex_StringByRune(t *testing.T) {
	got, err := Eval(`"abc"[1]`, nil)
	require.NoError(t, err)
	require.Equal(t, "b", got)

	got, err = Eval(`"héllo"[1]`, nil)
	require.NoError(t, err)
	require.Equal(t, "é", got)

	got, err = Eval(`"héllo"[4]`, nil)
	require.NoError(t, err)
	require.Equal(t, "o", got)
}

func TestIndex_StringRuneOutOfRange(t *testing.T) {
	// "héllo" is 5 runes — index 5 is out of range even though the
	// underlying byte length is 6.
	_, err := Eval(`"héllo"[5]`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "out of range")
}

func TestIndex_NegativeAndOutOfRange(t *testing.T) {
	env := map[string]any{"s": []any{1, 2, 3}}
	_, err := Eval("s[-1]", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "out of range")

	_, err = Eval("s[10]", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_TypedMap(t *testing.T) {
	env := map[string]any{"m": map[int]string{1: "one", 2: "two"}}
	got, err := Eval("m[1]", env)
	require.NoError(t, err)
	require.Equal(t, "one", got)

	// Missing key surfaces as an error, not a zero value.
	_, err = Eval("m[99]", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_MapWithNonStringKeyViaSelector(t *testing.T) {
	env := map[string]any{"m": map[int]string{1: "one"}}
	_, err := Eval(`m["x"]`, env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_OnUnsupportedType(t *testing.T) {
	env := map[string]any{"x": 42}
	_, err := Eval("x[0]", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_NilReceiver(t *testing.T) {
	_, err := Eval("nil[0]", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_MapKeyWrongType(t *testing.T) {
	env := map[string]any{"m": map[string]any{"k": 1}}
	_, err := Eval("m[1]", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "must be string")
}

// --- Selector edge cases ---

func TestSelector_NilReceiver(t *testing.T) {
	_, err := Eval("nil.x", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestSelector_StructFieldMissing(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{"s": S{A: 1}}
	_, err := Eval("s.B", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "field")
}

func TestSelector_PointerToStruct(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{"s": &S{A: 7}}
	got, err := Eval("s.A", env)
	require.NoError(t, err)
	require.Equal(t, 7, got)
}

func TestSelector_NilPointer(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{"s": (*S)(nil)}
	_, err := Eval("s.A", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestSelector_MapKeyMissing(t *testing.T) {
	env := map[string]any{"m": map[string]any{"a": 1}}
	_, err := Eval("m.b", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

func TestSelector_TypedMapWithStringKey(t *testing.T) {
	env := map[string]any{"m": map[string]int{"a": 1, "b": 2}}
	got, err := Eval("m.a", env)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// --- Calls ---

// Methods on struct and pointer receivers are callable through selectors.
func TestCall_MethodOnStructSelector(t *testing.T) {
	env := map[string]any{"u": testEnv{Count: 21}}

	got, err := Eval("u.Double()", env)
	require.NoError(t, err)
	require.Equal(t, 42, got)

	got, err = Eval(`u.Greet("world")`, env)
	require.NoError(t, err)
	require.Equal(t, "Hello, world", got)
}

func TestCall_MethodOnPointerSelector(t *testing.T) {
	env := map[string]any{"p": &ptrEnv{Value: 7}}
	got, err := Eval("p.Triple()", env)
	require.NoError(t, err)
	require.Equal(t, 21, got)
}

// Functions stored as values inside a map[string]any are callable via the
// same selector path — `state.fns.double(3)` style.
func TestCall_MapStoredFunctionViaSelector(t *testing.T) {
	env := map[string]any{
		"util": map[string]any{
			"double": func(n int64) int64 { return n * 2 },
		},
	}
	got, err := Eval("util.double(21)", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestCall_MethodNotFoundOnSelector(t *testing.T) {
	env := map[string]any{"u": testEnv{}}
	_, err := Eval("u.Nope()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "method")
}

func TestCall_MethodOnNilPointerSelector(t *testing.T) {
	env := map[string]any{"p": (*ptrEnv)(nil)}
	_, err := Eval("p.Triple()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil pointer")
}

// A struct field that happens to hold a function value is callable via
// selector — covers the Struct fallback inside resolveMethod.
func TestCall_StructFieldFunctionSelector(t *testing.T) {
	type Env struct {
		Double func(int64) int64
	}
	env := map[string]any{
		"x": Env{Double: func(n int64) int64 { return n * 2 }},
	}
	got, err := Eval("x.Double(21)", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

// A typed map with string keys stored beneath a selector should be able
// to resolve a function-valued entry.
func TestCall_TypedStringMapFunctionSelector(t *testing.T) {
	env := map[string]any{
		"fns": map[string]func(int64) int64{
			"double": func(n int64) int64 { return n * 2 },
		},
	}
	got, err := Eval("fns.double(21)", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestCall_MapStoredFunctionMissing(t *testing.T) {
	env := map[string]any{"m": map[string]any{"x": 1}}
	_, err := Eval("m.nope()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

func TestCall_OnNilSelectorReceiver(t *testing.T) {
	_, err := Eval("nil.f()", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil")
}

func TestCall_UnsupportedCallTarget(t *testing.T) {
	// Call target is an index expression, which goexpr does not support
	// as a callable (only identifiers and selectors are).
	env := map[string]any{
		"fns": []any{func() int64 { return 1 }},
	}
	_, err := Eval("fns[0]()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "call target")
}

func TestCall_WrongArgCount(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"two": func(a, b int) int { return a + b },
	}))
	_, err := e.Eval("two(1)", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "expects 2 args")
}

func TestCall_VariadicMinimumArgs(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"join": func(sep string, parts ...string) string {
			out := ""
			for i, p := range parts {
				if i > 0 {
					out += sep
				}
				out += p
			}
			return out
		},
	}))
	// Zero variadic args is allowed.
	got, err := e.Eval(`join(",")`, nil)
	require.NoError(t, err)
	require.Equal(t, "", got)

	got, err = e.Eval(`join(",", "a", "b", "c")`, nil)
	require.NoError(t, err)
	require.Equal(t, "a,b,c", got)

	// Fewer than the fixed count errors.
	_, err = e.Eval(`join()`, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least")
}

func TestCall_UnknownFunction(t *testing.T) {
	_, err := Eval("nope()", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unknown function")
}

func TestCall_TooManyReturns(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"three": func() (int, int, int) { return 1, 2, 3 },
	}))
	_, err := e.Eval("three()", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "returns")
}

func TestCall_SecondReturnNotError(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"bad": func() (int, string) { return 1, "x" },
	}))
	_, err := e.Eval("bad()", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "second return must be error")
}

func TestCall_ZeroReturn(t *testing.T) {
	called := false
	e := New(WithFunctions(map[string]any{
		"noop": func() { called = true },
	}))
	got, err := e.Eval("noop()", nil)
	require.NoError(t, err)
	require.Nil(t, got)
	require.True(t, called)
}

func TestCall_NonFunction(t *testing.T) {
	// Identifier that resolves to a non-function value via env, then called.
	env := map[string]any{"notfn": 42}
	_, err := Eval("notfn()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not a function")
}

// --- Argument conversion ---

func TestCall_NumericCoercion(t *testing.T) {
	// goexpr stores ints as int64; functions declared with int/int32/etc.
	// should still accept them via convertArg's numeric coercion.
	e := New(WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n + 1 },
		"f32": func(f float32) float32 { return f * 2 },
	}))

	got, err := e.Eval("i8(10)", nil)
	require.NoError(t, err)
	require.Equal(t, int8(11), got)

	got, err = e.Eval("f32(1.5)", nil)
	require.NoError(t, err)
	require.Equal(t, float32(3.0), got)
}

func TestCall_NilToNilableParam(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"takesSlice": func(s []int) int { return len(s) },
		"takesMap":   func(m map[string]int) int { return len(m) },
	}))

	got, err := e.Eval("takesSlice(nil)", nil)
	require.NoError(t, err)
	require.Equal(t, 0, got)

	got, err = e.Eval("takesMap(nil)", nil)
	require.NoError(t, err)
	require.Equal(t, 0, got)
}

func TestCall_NilToNonNilableParamFails(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"takesInt": func(n int) int { return n },
	}))
	_, err := e.Eval("takesInt(nil)", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "cannot pass nil")
}

func TestCall_WrongArgType(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"takesMap": func(m map[string]int) int { return len(m) },
	}))
	_, err := e.Eval(`takesMap("not-a-map")`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestCall_InterfaceParam(t *testing.T) {
	// A param of type `any` should accept anything.
	e := New(WithFunctions(map[string]any{
		"dump": func(v any) string {
			if v == nil {
				return "nil"
			}
			return "ok"
		},
	}))
	got, err := e.Eval("dump(42)", nil)
	require.NoError(t, err)
	require.Equal(t, "ok", got)

	got, err = e.Eval("dump(nil)", nil)
	require.NoError(t, err)
	require.Equal(t, "nil", got)
}

// --- Builtins: fill coverage and document sharp edges ---

func TestBuiltin_Len(t *testing.T) {
	cases := []struct {
		expr string
		env  map[string]any
		want any
	}{
		{`len(nil)`, nil, 0},
		{`len("")`, nil, 0},
		{`len(s)`, map[string]any{"s": []int{1, 2, 3}}, 3},
		{`len(m)`, map[string]any{"m": map[string]int{"a": 1}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, tc.env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_LenUnsupported(t *testing.T) {
	_, err := Eval("len(n)", map[string]any{"n": 42})
	require.Error(t, err)
}

// `len` counts runes (Unicode code points) for strings, not raw bytes, so
// indexing and length are consistent for non-ASCII strings.
func TestBuiltin_LenCountsRunes(t *testing.T) {
	got, err := Eval(`len("héllo")`, nil)
	require.NoError(t, err)
	require.Equal(t, 5, got)
}

func TestBuiltin_String(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{`string(nil)`, ""},
		{`string("already")`, "already"},
		{`string(42)`, "42"},
		{`string(3.14)`, "3.14"},
		{`string(true)`, "true"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_Int(t *testing.T) {
	cases := []struct {
		expr string
		want int64
	}{
		{"int(nil)", 0},
		{"int(42)", 42},
		{"int(3.9)", 3}, // truncation toward zero
		{`int("42")`, 42},
		{`int("  -7 ")`, -7}, // surrounding whitespace is tolerated
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// `int` uses strict base-10 parsing, so trailing garbage and non-integer
// forms error instead of silently producing a truncated value.
func TestBuiltin_IntStringStrict(t *testing.T) {
	for _, expr := range []string{`int("42abc")`, `int("3.14")`, `int("0xff")`} {
		_, err := Eval(expr, nil)
		require.Error(t, err, expr)
		require.Contains(t, err.Error(), "cannot parse", expr)
	}
}

func TestBuiltin_IntStringInvalid(t *testing.T) {
	_, err := Eval(`int("nope")`, nil)
	require.Error(t, err)
}

func TestBuiltin_IntUnsupported(t *testing.T) {
	_, err := Eval("int(x)", map[string]any{"x": []any{1}})
	require.Error(t, err)
}

func TestBuiltin_Float(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"float(nil)", 0},
		{"float(1)", 1.0},
		{"float(2.5)", 2.5},
		{`float("3.14")`, 3.14},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// `float` uses strict parsing so trailing garbage fails loudly.
func TestBuiltin_FloatStringStrict(t *testing.T) {
	for _, expr := range []string{`float("3.14xyz")`, `float("")`} {
		_, err := Eval(expr, nil)
		require.Error(t, err, expr)
		require.Contains(t, err.Error(), "cannot parse", expr)
	}
}

func TestBuiltin_FloatStringInvalid(t *testing.T) {
	_, err := Eval(`float("nope")`, nil)
	require.Error(t, err)
}

func TestBuiltin_FloatUnsupported(t *testing.T) {
	_, err := Eval("float(x)", map[string]any{"x": []any{1}})
	require.Error(t, err)
}

func TestBuiltin_Bool(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"bool(nil)", false},
		{"bool(0)", false},
		{"bool(1)", true},
		{`bool("")`, false},
		{`bool("x")`, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_Contains_Nil(t *testing.T) {
	got, err := Eval("contains(nil, 1)", nil)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// Documents that `contains` uses looseEqual, so a float needle matches an
// int element and vice versa. Probably a feature — but worth pinning.
func TestBuiltin_Contains_NumericLoose(t *testing.T) {
	env := map[string]any{"xs": []any{1, 2, 3}}
	got, err := Eval("contains(xs, 1.0)", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestBuiltin_Contains_StringNeedleWrongType(t *testing.T) {
	_, err := Eval(`contains("hello", 1)`, nil)
	require.Error(t, err)
}

func TestBuiltin_Contains_UnsupportedHaystack(t *testing.T) {
	env := map[string]any{"x": 42}
	_, err := Eval("contains(x, 1)", env)
	require.Error(t, err)
}

func TestBuiltin_Contains_MapNonStringKeyErrors(t *testing.T) {
	env := map[string]any{"m": map[int]int{1: 1}}
	_, err := Eval("contains(m, 1)", env)
	require.Error(t, err)
}

// `has` is narrowed to maps with string keys — checking slice membership
// is `contains`'s job. This keeps the two functions clearly distinct.
func TestBuiltin_Has_MapsOnly(t *testing.T) {
	env := map[string]any{
		"tags": map[string]any{"red": true, "blue": false},
	}
	got, err := Eval(`has(tags, "red")`, env)
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = Eval(`has(tags, "green")`, env)
	require.NoError(t, err)
	require.Equal(t, false, got)

	// Slices are not a valid haystack for has.
	env = map[string]any{"xs": []any{"a", "b"}}
	_, err = Eval(`has(xs, "a")`, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected map")

	// Nil is permitted and returns false without erroring.
	got, err = Eval("has(nil, \"k\")", nil)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestBuiltin_Keys_Nil(t *testing.T) {
	got, err := Eval("keys(nil)", nil)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestBuiltin_Keys_NonMapErrors(t *testing.T) {
	_, err := Eval("keys(x)", map[string]any{"x": 42})
	require.Error(t, err)
}

func TestBuiltin_Keys_TypedMap(t *testing.T) {
	env := map[string]any{"m": map[string]int{"b": 2, "a": 1}}
	got, err := Eval("keys(m)", env)
	require.NoError(t, err)
	require.Equal(t, []any{"a", "b"}, got)
}

// --- Script adapter coverage (Items + String on wrapped value) ---

func TestScriptAdapter_ItemsOnSlice(t *testing.T) {
	ctx := context.Background()
	c := New().Compiler()
	prog, err := c.Compile(ctx, `state.items`)
	require.NoError(t, err)
	v, err := prog.Evaluate(ctx, map[string]any{
		"state": map[string]any{"items": []any{1, 2, 3}},
	})
	require.NoError(t, err)
	items, err := v.Items()
	require.NoError(t, err)
	require.Equal(t, []any{1, 2, 3}, items)
}

func TestScriptAdapter_StringOnNilValue(t *testing.T) {
	ctx := context.Background()
	c := New().Compiler()
	prog, err := c.Compile(ctx, `nil`)
	require.NoError(t, err)
	v, err := prog.Evaluate(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "", v.String())
}

func TestScriptAdapter_StringOnValue(t *testing.T) {
	ctx := context.Background()
	c := New().Compiler()
	prog, err := c.Compile(ctx, `42`)
	require.NoError(t, err)
	v, err := prog.Evaluate(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "42", v.String())
}

func TestScriptAdapter_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New().Compiler()
	_, err := c.Compile(ctx, `1`)
	require.ErrorIs(t, err, context.Canceled)
}

func TestScriptAdapter_EvaluateContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := New().Compiler()
	prog, err := c.Compile(ctx, `1`)
	require.NoError(t, err)
	cancel()
	_, err = prog.Evaluate(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestScriptAdapter_CompileError(t *testing.T) {
	c := New().Compiler()
	_, err := c.Compile(context.Background(), "1 + + +")
	require.ErrorIs(t, err, ErrCompile)
}

func TestScriptAdapter_EvaluateRuntimeError(t *testing.T) {
	c := New().Compiler()
	prog, err := c.Compile(context.Background(), "nope")
	require.NoError(t, err)
	_, err = prog.Evaluate(context.Background(), nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Error bubbling: ensure custom function errors flow through unchanged ---

type customErr struct{ msg string }

func (c customErr) Error() string { return c.msg }

func TestCall_CustomErrorTypePropagates(t *testing.T) {
	sentinel := customErr{msg: "boom"}
	e := New(WithFunctions(map[string]any{
		"fail": func() (int, error) { return 0, sentinel },
	}))
	_, err := e.Eval("fail()", nil)
	require.Error(t, err)
	var ce customErr
	require.True(t, errors.As(err, &ce))
	require.Equal(t, "boom", ce.msg)
}

// --- Reserved name panic coverage for inputs ---

func TestEngine_ReservedInputsPanic(t *testing.T) {
	require.PanicsWithValue(t,
		`goexpr: function name "inputs" is reserved by the workflow engine`,
		func() {
			New(WithFunctions(map[string]any{"inputs": func() {}}))
		},
	)
}

// --- Nil env with struct env expectations ---

func TestLookup_NilPointerEnv(t *testing.T) {
	var p *ptrEnv
	_, err := Eval("Value", p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined")
}
