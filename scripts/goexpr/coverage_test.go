package goexpr

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests fill the remaining coverage gaps that neither engine_test.go
// nor boundaries_test.go nor adversarial_test.go hit. Each test pins a
// specific piece of intentional behavior; none exist solely to color
// lines green.

// --- toInt64 branch sweep ---

// toInt64 accepts every integer kind in the standard library. Rather
// than write ten trivial tests, we stage each kind through env and read
// it back with a bare identifier.
func TestToInt64_AllIntegerKinds(t *testing.T) {
	cases := map[string]any{
		"vInt":    int(1),
		"vInt8":   int8(2),
		"vInt16":  int16(3),
		"vInt32":  int32(4),
		"vInt64":  int64(5),
		"vUint":   uint(6),
		"vUint8":  uint8(7),
		"vUint16": uint16(8),
		"vUint32": uint32(9),
		"vUint64": uint64(10),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			// `+` of integer and int64(0) forces the value through toInt64.
			got, err := Eval(ctx, name+" + 0", map[string]any{name: value})
			require.NoError(t, err)
			require.Equal(t, int64(toInt64OrPanic(value)), got)
		})
	}
}

func toInt64OrPanic(v any) int64 {
	i, ok := toInt64(v)
	if !ok {
		panic("toInt64 failed")
	}
	return i
}

// --- toFloat64 and float32 ---

func TestToFloat64_Float32Source(t *testing.T) {
	// float32 in env must add correctly.
	got, err := Eval(ctx, "v + 1.5", map[string]any{"v": float32(2.5)})
	require.NoError(t, err)
	require.InDelta(t, 4.0, got.(float64), 1e-9)
}

// --- isNilValue branch sweep ---

func TestIsNilValue_AllNilableKinds(t *testing.T) {
	cases := map[string]any{
		"chan":  (chan int)(nil),
		"func":  (func())(nil),
		"map":   (map[string]int)(nil),
		"ptr":   (*int)(nil),
		"slice": ([]int)(nil),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Eval(ctx, "x == nil", map[string]any{"x": v})
			require.NoError(t, err)
			require.Equal(t, true, got)
		})
	}
}

// --- looseEqual: comparable but different types and uncomparable path ---

func TestLooseEqual_ComparableDifferentTypes(t *testing.T) {
	type A struct{ X int }
	type B struct{ X int }
	env := map[string]any{"a": A{X: 1}, "b": B{X: 1}}
	got, err := Eval(ctx, "a == b", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// --- convertArg: interface param with implementing type ---

type stringer struct{ s string }

func (s stringer) String() string { return s.s }

func TestConvertArg_InterfaceImplements(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"toStr": func(s interface{ String() string }) string { return s.String() },
	}))
	got, err := e.Eval(ctx, "toStr(v)", map[string]any{"v": stringer{s: "hi"}})
	require.NoError(t, err)
	require.Equal(t, "hi", got)
}

// --- intToKind: all target kinds ---

func TestIntToKind_AllTargets(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i":    func(n int) int { return n },
		"i8":   func(n int8) int8 { return n },
		"i16":  func(n int16) int16 { return n },
		"i32":  func(n int32) int32 { return n },
		"i64":  func(n int64) int64 { return n },
		"u":    func(n uint) uint { return n },
		"u8":   func(n uint8) uint8 { return n },
		"u16":  func(n uint16) uint16 { return n },
		"u32":  func(n uint32) uint32 { return n },
		"u64":  func(n uint64) uint64 { return n },
		"uptr": func(n uintptr) uintptr { return n },
		"f32":  func(f float32) float32 { return f },
		"f64":  func(f float64) float64 { return f },
	}))
	cases := []struct {
		expr string
		want any
	}{
		{"i(10)", int(10)},
		{"i8(10)", int8(10)},
		{"i16(10)", int16(10)},
		{"i32(10)", int32(10)},
		{"i64(10)", int64(10)},
		{"u(10)", uint(10)},
		{"u8(10)", uint8(10)},
		{"u16(10)", uint16(10)},
		{"u32(10)", uint32(10)},
		{"u64(10)", uint64(10)},
		{"uptr(10)", uintptr(10)},
		{"f32(10)", float32(10)},
		{"f64(10)", float64(10)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := e.Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// --- intToKind: every negative→unsigned rejection path ---

func TestIntToKind_NegativeToUnsignedRejected(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"u":    func(n uint) uint { return n },
		"u64":  func(n uint64) uint64 { return n },
		"uptr": func(n uintptr) uintptr { return n },
	}))
	for _, expr := range []string{"u(-1)", "u64(-1)", "uptr(-1)"} {
		t.Run(expr, func(t *testing.T) {
			_, err := e.Eval(ctx, expr, nil)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

// --- safeNumericConvert: uint source, uint target (hits uintToKind) ---

func TestSafeNumericConvert_UintToUint(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"u":    func(n uint) uint { return n },
		"u8":   func(n uint8) uint8 { return n },
		"u16":  func(n uint16) uint16 { return n },
		"u32":  func(n uint32) uint32 { return n },
		"u64":  func(n uint64) uint64 { return n },
		"uptr": func(n uintptr) uintptr { return n },
	}))
	// A uint32 value in env narrows to uint8 via uintToKind.
	env := map[string]any{"v": uint32(200)}
	got, err := e.Eval(ctx, "u8(v)", env)
	require.NoError(t, err)
	require.Equal(t, uint8(200), got)

	// uint32 → uint16 also
	env = map[string]any{"v": uint32(60000)}
	got, err = e.Eval(ctx, "u16(v)", env)
	require.NoError(t, err)
	require.Equal(t, uint16(60000), got)

	// uint32(5) → uint, uint64, uintptr all widen cleanly
	env = map[string]any{"v": uint32(5)}
	for _, expr := range []string{"u(v)", "u64(v)", "uptr(v)"} {
		_, err := e.Eval(ctx, expr, env)
		require.NoError(t, err, expr)
	}

	// uint64 → uint32 overflow rejected
	env = map[string]any{"v": uint64(math.MaxUint32) + 1}
	_, err = e.Eval(ctx, "u32(v)", env)
	require.Error(t, err)

	// uint32 → uint8 overflow rejected
	env = map[string]any{"v": uint32(300)}
	_, err = e.Eval(ctx, "u8(v)", env)
	require.Error(t, err)

	// uint64 → uint16 overflow rejected
	env = map[string]any{"v": uint64(70000)}
	_, err = e.Eval(ctx, "u16(v)", env)
	require.Error(t, err)
}

// --- safeNumericConvert: uint source > MaxInt64 to signed target ---

func TestSafeNumericConvert_HugeUintToSignedRejected(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i64": func(n int64) int64 { return n },
	}))
	env := map[string]any{"v": uint64(math.MaxUint64)}
	_, err := e.Eval(ctx, "i64(v)", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- lookupEnv fallthrough for non-struct non-map values ---

func TestLookupEnv_UnsupportedEnvKind(t *testing.T) {
	// env is an int — no identifiers reachable. Must not panic.
	_, err := Eval(ctx, "x", 42)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- selectField: non-map non-struct receiver ---

func TestSelectField_UnsupportedReceiver(t *testing.T) {
	env := map[string]any{"x": 42}
	_, err := Eval(ctx, "x.field", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "cannot select")
}

// --- resolveMethod: map with non-string keys rejects ---

func TestResolveMethod_MapNonStringKey(t *testing.T) {
	// A typed map with int keys exposes no methods; attempting to call
	// an entry via selector falls through resolveMethod and errors.
	env := map[string]any{"m": map[int]func() int{1: func() int { return 1 }}}
	_, err := Eval(ctx, "m.x()", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Builtin: len(chan) ---

func TestBuiltin_LenChan(t *testing.T) {
	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	got, err := Eval(ctx, "len(c)", map[string]any{"c": ch})
	require.NoError(t, err)
	require.Equal(t, 2, got)
}

// --- evalUnary: unsupported operator token (e.g. ^) ---

func TestEvalUnary_UnsupportedOperator(t *testing.T) {
	// go/parser accepts ^x as a unary expression, but evalUnary only
	// implements !, -, + — so it must reject with ErrEvaluate.
	_, err := Eval(ctx, "^1", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- evalIdent and lookup fall-through for map with non-string key ---

func TestLookupEnv_TypedMapWithNonStringKey(t *testing.T) {
	// Env is map[int]int — reflect.Map path checks key kind and skips it.
	_, err := Eval(ctx, "x", map[int]int{1: 1})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- convertArg fallback ConvertibleTo path ---

func TestConvertArg_ConvertibleTo(t *testing.T) {
	// A named string type parameter accepts a plain string via Convert.
	type MyStr string
	e := New(WithFunctions(map[string]any{
		"take": func(s MyStr) string { return string(s) },
	}))
	got, err := e.Eval(ctx, `take("hi")`, nil)
	require.NoError(t, err)
	require.Equal(t, "hi", got)
}

// --- buildCallArgs variadic zero-args check (fixed too few) ---
// Already covered by TestCall_VariadicMinimumArgs, but pin the branch
// where fixed > 0 and len(args) < fixed to be explicit.
func TestBuildCallArgs_VariadicBelowFixed(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"f": func(a, b int, rest ...int) int { return a + b },
	}))
	_, err := e.Eval(ctx, "f(1)", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- applyBinary: operator with incompatible types (non-string non-numeric) ---

func TestApplyBinary_IncompatibleTypes(t *testing.T) {
	env := map[string]any{"s": []any{1}, "m": map[string]any{"k": 1}}
	_, err := Eval(ctx, "s + m", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- resolveMethod: nil receiver ---

func TestResolveMethod_NilReceiverDirect(t *testing.T) {
	// Verified via selector earlier, but this also hits the first line
	// of resolveMethod when the value is literally nil (not just a nil
	// pointer).
	env := map[string]any{"v": nil}
	_, err := Eval(ctx, "v.f()", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- String comparison operators (non-equality) ---

// Pins every string comparison operator so the `case token.X` branches
// in applyBinary's string-path stay covered.
func TestApplyBinary_StringComparators(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{`"a" != "b"`, true},
		{`"a" > "b"`, false},
		{`"a" <= "b"`, true},
		{`"a" >= "b"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// --- Float arithmetic and comparison operators ---

func TestApplyBinary_FloatOps(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"5.5 - 2.5", 3.0},
		{"1.5 < 2.0", true},
		{"1.5 > 2.0", false},
		{"1.5 <= 1.5", true},
		{"1.5 >= 2.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// --- evalIdent fallback to registered function ---

func TestEvalIdent_FunctionValue(t *testing.T) {
	// A bare identifier referring to a builtin returns the function
	// value, so you can pass it to another function. We verify by using
	// it as a variadic arg via a custom engine.
	e := New(WithFunctions(map[string]any{
		"myFn": func() int { return 7 },
		"call": func(fn func() int) int { return fn() },
	}))
	got, err := e.Eval(ctx, "call(myFn)", nil)
	require.NoError(t, err)
	require.Equal(t, 7, got)
}

// --- lookupEnv: typed map with string keys ---

func TestLookupEnv_TypedStringMap(t *testing.T) {
	// env is map[string]int directly — not a map[string]any. Triggers
	// the reflect.Map branch in lookupEnv.
	env := map[string]int{"x": 7}
	got, err := Eval(ctx, "x", env)
	require.NoError(t, err)
	require.Equal(t, 7, got)
}

// --- contains: map-of-strings path via reflect ---

func TestBuiltin_ContainsMapKeyPresent(t *testing.T) {
	// A typed map (not map[string]any) falls through to the reflect
	// path, which checks key presence.
	env := map[string]any{"m": map[string]int{"a": 1, "b": 2}}
	got, err := Eval(ctx, `contains(m, "a")`, env)
	require.NoError(t, err)
	require.Equal(t, true, got)

	_, err = Eval(ctx, `contains(m, 1)`, env)
	require.Error(t, err)
}

// --- has: map with non-string keys and wrong key type ---

func TestBuiltin_HasMapKeyKindWrong(t *testing.T) {
	env := map[string]any{"m": map[int]int{1: 1}}
	_, err := Eval(ctx, `has(m, "x")`, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "map key must be string")
}

func TestBuiltin_HasKeyWrongType(t *testing.T) {
	// map is map[string]any but key arg evaluates to non-string.
	// builtin_has uses strict type assert, so int key errors.
	e := New(WithFunctions(map[string]any{
		"badHas": func(m map[string]any, k any) (bool, error) {
			return builtinHas(m, k)
		},
	}))
	_, err := e.Eval(ctx, `badHas(m, 1)`, map[string]any{"m": map[string]any{"a": 1}})
	require.Error(t, err)
}

// --- convertArg: fast-path when rv.Type() is AssignableTo want ---

func TestConvertArg_AlreadyAssignable(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"take": func(a any) any { return a },
	}))
	got, err := e.Eval(ctx, "take(42)", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

// --- selectField: map with non-string keys ---

func TestSelectField_MapNonStringKeys(t *testing.T) {
	env := map[string]any{"m": map[int]int{1: 1}}
	_, err := Eval(ctx, "m.x", env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-string keys")
}

// --- selectField: typed map with string keys, missing key ---

func TestSelectField_TypedMapMissingKey(t *testing.T) {
	env := map[string]any{"m": map[string]int{"a": 1}}
	_, err := Eval(ctx, "m.b", env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// --- resolveMethod: pointer-to-struct method resolution after Elem ---

type deepStruct struct{ N int }

func (d deepStruct) Bump() int { return d.N + 1 }

func TestResolveMethod_PointerElemFallback(t *testing.T) {
	// The pointer itself has no methods with this name; its Elem does.
	// The code path goes: MethodByName fails, rv.Elem(), MethodByName
	// succeeds.
	d := deepStruct{N: 3}
	env := map[string]any{"d": &d}
	got, err := Eval(ctx, "d.Bump()", env)
	require.NoError(t, err)
	require.Equal(t, 4, got)
}

// --- looseEqual: non-nil compared with typed nil ---

func TestLooseEqual_NilOnLeft(t *testing.T) {
	env := map[string]any{"s": []any(nil)}
	got, err := Eval(ctx, "nil == s", env)
	require.NoError(t, err)
	require.Equal(t, true, got)

	env = map[string]any{"s": []any{1}}
	got, err = Eval(ctx, "nil == s", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// --- looseEqual: same comparable type native equality ---

func TestLooseEqual_SameTypeEqual(t *testing.T) {
	type K struct{ A int }
	env := map[string]any{"a": K{A: 1}, "b": K{A: 1}}
	got, err := Eval(ctx, "a == b", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

// --- Uncomparable LHS variant ---

func TestLooseEqual_UncomparableLHS(t *testing.T) {
	env := map[string]any{"s": []int{1}, "i": 2}
	_, err := Eval(ctx, "s == i", env)
	require.Error(t, err)
}

// --- safeNumericConvert: float to int16 out-of-range ---

func TestSafeNumericConvert_FloatNarrowingRejected(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i16": func(n int16) int16 { return n },
	}))
	_, err := e.Eval(ctx, "i16(100000.0)", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- isNilValue: direct, to pin the untyped-nil guarantee ---

func TestIsNilValue_Direct(t *testing.T) {
	require.True(t, isNilValue(nil))
	require.True(t, isNilValue([]int(nil)))
	require.True(t, isNilValue((*int)(nil)))
	require.False(t, isNilValue(0))
	require.False(t, isNilValue("x"))
}

// --- applyBinary: nil on one side of comparison ---

func TestApplyBinary_NilComparisonShortcircuit(t *testing.T) {
	// lhs nil, rhs non-nilable int: looseEqual rhs path and return false
	got, err := Eval(ctx, "nil == 42", nil)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// --- evalBinary: error in LAND rhs eval after truthy lhs ---

func TestEvalBinary_LandErrorsOnRHS(t *testing.T) {
	// lhs is truthy, so rhs must evaluate and can fail.
	_, err := Eval(ctx, "true && nosuch", nil)
	require.Error(t, err)
}

func TestEvalBinary_LandErrorsOnLHS(t *testing.T) {
	_, err := Eval(ctx, "nosuch && true", nil)
	require.Error(t, err)
}

func TestEvalBinary_LorErrorsOnLHS(t *testing.T) {
	_, err := Eval(ctx, "nosuch || true", nil)
	require.Error(t, err)
}

func TestEvalBinary_LorErrorsOnRHS(t *testing.T) {
	_, err := Eval(ctx, "false || nosuch", nil)
	require.Error(t, err)
}

func TestEvalBinary_NonLogicalRHSError(t *testing.T) {
	_, err := Eval(ctx, "1 + nosuch", nil)
	require.Error(t, err)
}

// --- evalIndex: rejections on err paths ---

func TestEvalIndex_RecvError(t *testing.T) {
	_, err := Eval(ctx, "nosuch[0]", nil)
	require.Error(t, err)
}

func TestEvalIndex_IdxError(t *testing.T) {
	env := map[string]any{"s": []any{1, 2, 3}}
	_, err := Eval(ctx, "s[nosuch]", env)
	require.Error(t, err)
}

// --- evalCall: arg eval error ---

func TestEvalCall_ArgError(t *testing.T) {
	_, err := Eval(ctx, "len(nosuch)", nil)
	require.Error(t, err)
}

// --- resolveCallable: err from selector expression ---

func TestResolveCallable_SelectorError(t *testing.T) {
	_, err := Eval(ctx, "nosuch.f()", nil)
	require.Error(t, err)
}

// --- resolveMethod: struct field holding function, through pointer ---

type fnHolder struct {
	F func() int
}

func TestResolveMethod_StructFieldFunction(t *testing.T) {
	env := map[string]any{"h": fnHolder{F: func() int { return 9 }}}
	got, err := Eval(ctx, "h.F()", env)
	require.NoError(t, err)
	require.Equal(t, 9, got)
}

// --- buildCallArgs: convertArg error inside variadic ---

func TestBuildCallArgs_VariadicBadArg(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"sum": func(xs ...int) int {
			s := 0
			for _, x := range xs {
				s += x
			}
			return s
		},
	}))
	// Can't convert a slice to int → error on a variadic position.
	_, err := e.Eval(ctx, "sum(s)", map[string]any{"s": []int{1}})
	require.Error(t, err)
}

func TestBuildCallArgs_BadFixedArg(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"sum": func(a int, rest ...int) int { return a },
	}))
	// First fixed arg of a variadic function also goes through convertArg;
	// a slice-typed value can't coerce to int.
	_, err := e.Eval(ctx, "sum(s, 1)", map[string]any{"s": []int{1}})
	require.Error(t, err)
}

// --- Package-level Eval with compile error ---

func TestPackageEval_CompileError(t *testing.T) {
	_, err := Eval(ctx, "1 + + +", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
}
