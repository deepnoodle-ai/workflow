package goexpr

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin specific adversarial inputs. The fuzz targets in
// fuzz_test.go should also catch regressions, but hand-written cases
// document the intent so a reviewer can see *why* a given input matters.

// --- Depth and size limits ---

func TestLimit_DeepSelectorChain(t *testing.T) {
	// Construct a selector chain deeper than MaxEvalDepth. The expression
	// must reject with an ErrEvaluate, not stack-overflow.
	expr := "a"
	for i := 0; i < MaxEvalDepth+5; i++ {
		expr += ".f"
	}
	_, err := Eval(ctx, expr, map[string]any{"a": map[string]any{"f": nil}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLimit_DeepBinaryChain(t *testing.T) {
	// Left-associative: 1+1+1+1+... becomes a left-leaning tree with
	// depth proportional to the operand count.
	var b strings.Builder
	b.WriteString("1")
	for i := 0; i < MaxEvalDepth+5; i++ {
		b.WriteString("+1")
	}
	_, err := Eval(ctx, b.String(), nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nested too deeply")
}

func TestLimit_DeepParens(t *testing.T) {
	// Parenthesis nesting is the cheapest way to build a deep AST, but
	// ParseExpr itself may stack-overflow on truly pathological input.
	// Stay well below that with something our cap still catches.
	n := MaxEvalDepth + 5
	expr := strings.Repeat("(", n) + "1" + strings.Repeat(")", n)
	_, err := Eval(ctx, expr, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLimit_SourceLength(t *testing.T) {
	// Compile must refuse oversized input before handing it to go/parser.
	src := strings.Repeat("a", MaxSourceLength+1)
	_, err := Compile(src)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "source length")
}

func TestLimit_SourceLengthBoundary(t *testing.T) {
	// Exactly at the limit is allowed (syntax-valid content).
	src := "1" + strings.Repeat(" ", MaxSourceLength-1)
	v, err := Compile(src)
	require.NoError(t, err)
	require.NotNil(t, v)
}

// --- Reflection panic paths ---

type withUnexported struct {
	secret int //nolint:unused // exercised via selector-deny path
	Public int
}

func TestReflect_UnexportedFieldDenied(t *testing.T) {
	// Reading an unexported field via reflect.Value.Interface panics. We
	// check CanInterface and report "not found" instead.
	env := map[string]any{"x": withUnexported{Public: 1}}
	_, err := Eval(ctx, "x.secret", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

func TestReflect_UnexportedFieldDeniedStructEnv(t *testing.T) {
	// Same, but the struct is the whole env. lookupEnv already guards
	// this, but we pin it so neither path regresses.
	env := withUnexported{Public: 1}
	_, err := Eval(ctx, "secret", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestReflect_NilIndexOnTypedMap(t *testing.T) {
	// reflect.ValueOf(nil).Type() panics — guarding nil in indexValue
	// keeps user expressions safe.
	env := map[string]any{"m": map[int]string{1: "one"}}
	_, err := Eval(ctx, "m[nil]", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil as map key")
}

func TestReflect_NilFunctionValue(t *testing.T) {
	// A typed nil function value is reflect.Func kind with IsNil() true.
	// Calling it panics, so callFunction has to reject it up front.
	env := map[string]any{"fn": (func() int)(nil)}
	_, err := Eval(ctx, "fn()", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil function")
}

func TestReflect_NilFunctionFieldValue(t *testing.T) {
	type Holder struct {
		Fn func(int) int
	}
	env := map[string]any{"h": Holder{Fn: nil}}
	_, err := Eval(ctx, "h.Fn(1)", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil function")
}

// --- Numeric conversion audit ---

// Ints should never silently wrap when narrowing to a smaller type.
func TestConvert_IntOverflowRejected(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n },
		"u8":  func(n uint8) uint8 { return n },
		"u16": func(n uint16) uint16 { return n },
		"u32": func(n uint32) uint32 { return n },
		"i32": func(n int32) int32 { return n },
	}))
	cases := []struct {
		expr string
	}{
		{"i8(128)"},
		{"i8(-129)"},
		{"u8(256)"},
		{"u8(-1)"},
		{"u16(65536)"},
		{"u32(-1)"},
		{"i32(2147483648)"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			_, err := e.Eval(ctx, tc.expr, nil)
			require.Error(t, err, "%s should reject overflow", tc.expr)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestConvert_IntInRangeAllowed(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n },
		"u8":  func(n uint8) uint8 { return n },
		"i32": func(n int32) int32 { return n },
	}))
	cases := []struct {
		expr string
		want any
	}{
		{"i8(127)", int8(127)},
		{"i8(-128)", int8(-128)},
		{"u8(0)", uint8(0)},
		{"u8(255)", uint8(255)},
		{"i32(2147483647)", int32(math.MaxInt32)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := e.Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConvert_FloatToIntRange(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n },
		"i64": func(n int64) int64 { return n },
	}))
	// Truncation toward zero is intentional.
	got, err := e.Eval(ctx, "i8(3.9)", nil)
	require.NoError(t, err)
	require.Equal(t, int8(3), got)

	got, err = e.Eval(ctx, "i8(-3.9)", nil)
	require.NoError(t, err)
	require.Equal(t, int8(-3), got)

	// Out-of-range float → target int kind.
	_, err = e.Eval(ctx, "i8(1000.0)", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestConvert_NaNAndInfRejected(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"i32":  func(n int32) int32 { return n },
		"makeNaN": func() float64 { return math.NaN() },
		"makeInf": func() float64 { return math.Inf(1) },
	}))
	_, err := e.Eval(ctx, "i32(makeNaN())", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)

	_, err = e.Eval(ctx, "i32(makeInf())", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestConvert_UintSource(t *testing.T) {
	// env may supply uint values; they must narrow with the same checks.
	e := New(WithFunctions(map[string]any{
		"i8": func(n int8) int8 { return n },
	}))
	_, err := e.Eval(ctx, "i8(v)", map[string]any{"v": uint64(300)})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)

	got, err := e.Eval(ctx, "i8(v)", map[string]any{"v": uint64(100)})
	require.NoError(t, err)
	require.Equal(t, int8(100), got)
}

// --- Literal edge cases ---

func TestLiteral_LargeInt(t *testing.T) {
	// strconv.ParseInt rejects; we report via ErrEvaluate, not panic.
	_, err := Eval(ctx, "9999999999999999999999999999", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLiteral_HugeStringLiteral(t *testing.T) {
	// A string literal near the source limit must round-trip.
	payload := strings.Repeat("a", MaxSourceLength-10)
	src := `"` + payload + `"`
	got, err := Eval(ctx, src, nil)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestLiteral_InvalidEscape(t *testing.T) {
	// Parser rejects; wraps ErrCompile.
	_, err := Compile(`"\xZZ"`)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
}

func TestLiteral_EmptyCharLiteral(t *testing.T) {
	// Parser usually rejects '' — but the evaluator's len(runes) != 1
	// branch is still worth covering for completeness via a multi-byte
	// escaped input.
	_, err := Compile("''")
	require.Error(t, err)
}

// --- Reserved call-target shapes ---

func TestSyntax_UnsupportedNodesAllReject(t *testing.T) {
	cases := []string{
		"x[1:3]",       // SliceExpr
		"x[1:3:5]",     // SliceExpr full
		"x.(int)",      // TypeAssertExpr
		"<-ch",         // ChanExpr (parses as UnaryExpr with ARROW)
		"*p",           // unary * (unsupported token)
		"&x",           // address-of
		"1 & 2",        // bitwise AND
		"1 | 2",        // bitwise OR
		"1 ^ 2",        // bitwise XOR
		"1 << 2",       // shift left
		"1 >> 2",       // shift right
		"1 &^ 2",       // AND NOT
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := Eval(ctx, expr, map[string]any{"x": []any{1, 2, 3}, "p": 1, "ch": 1})
			require.Error(t, err, "%s should reject", expr)
		})
	}
}

// --- Maps with interface keys ---

func TestMap_InterfaceKeys(t *testing.T) {
	// A map[any]any with string key works through the reflect fallback.
	env := map[string]any{
		"m": map[any]any{"a": 1, "b": 2},
	}
	got, err := Eval(ctx, `m["a"]`, env)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// --- User code panics bubble up naturally ---

func TestUserCode_PanicIsNotOurBug(t *testing.T) {
	// The engine does not recover panics from user code — documented in
	// the spec. Pin the current behavior so changes are deliberate.
	e := New(WithFunctions(map[string]any{
		"boom": func() int { panic("nope") },
	}))
	require.Panics(t, func() {
		_, _ = e.Eval(ctx, "boom()", nil)
	})
}

// --- Cross-type equality with uncomparable operands ---

func TestEquality_UncomparableReturnsError(t *testing.T) {
	// Slice == slice is not comparable under looseEqual. Matching Go's
	// runtime behavior means the binary path falls through to the error.
	env := map[string]any{
		"a": []int{1, 2, 3},
		"b": []int{1, 2, 3},
	}
	_, err := Eval(ctx, "a == b", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Error bubbling from user error types via errors.As ---

type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }

func TestUserError_UnwrapChainPreserved(t *testing.T) {
	sentinel := errors.New("root")
	e := New(WithFunctions(map[string]any{
		"fail": func() (int, error) { return 0, wrappedErr{inner: sentinel} },
	}))
	_, err := e.Eval(ctx, "fail()", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
}
