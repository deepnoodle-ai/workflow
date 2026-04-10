package goexpr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/workflow/script"
	"github.com/stretchr/testify/require"
)

func TestEval_Literals(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"42", int64(42)},
		{"3.14", 3.14},
		{`"hello"`, "hello"},
		{"true", true},
		{"false", false},
		{"nil", nil},
		{"-5", int64(-5)},
		{"!true", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Arithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"1 + 2", int64(3)},
		{"5 - 3", int64(2)},
		{"4 * 5", int64(20)},
		{"10 / 3", int64(3)},
		{"10 % 3", int64(1)},
		{"2 + 3 * 4", int64(14)},
		{"(2 + 3) * 4", int64(20)},
		{"1.5 + 2.5", 4.0},
		{"10 / 4.0", 2.5},
		{`"foo" + "bar"`, "foobar"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Comparison(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"1 < 2", true},
		{"2 < 1", false},
		{"3 == 3", true},
		{"3 != 4", true},
		{"3 >= 3", true},
		{"3 <= 2", false},
		{`"a" < "b"`, true},
		{`"foo" == "foo"`, true},
		{"1 == 1.0", true},
		{"1 != 1.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Logical(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"true && true", true},
		{"true && false", false},
		{"false || true", true},
		{"false || false", false},
		{"1 < 2 && 3 < 4", true},
		{"1 > 2 || 3 < 4", true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_ShortCircuit(t *testing.T) {
	env := map[string]any{
		"exploder": func() bool { panic("should not be called") },
	}
	// && short-circuits when lhs is false
	got, err := Eval(ctx, "false && exploder()", env)
	require.NoError(t, err)
	require.Equal(t, false, got)

	// || short-circuits when lhs is true
	got, err = Eval(ctx, "true || exploder()", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestEval_Selectors(t *testing.T) {
	env := map[string]any{
		"state": map[string]any{
			"counter": int64(5),
			"user": map[string]any{
				"name": "Alice",
				"age":  int64(30),
			},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{"state.counter", int64(5)},
		{"state.user.name", "Alice"},
		{"state.user.age >= 18", true},
		{"state.counter + 10", int64(15)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_IndexExpressions(t *testing.T) {
	env := map[string]any{
		"state": map[string]any{
			"items":  []any{"a", "b", "c"},
			"counts": map[string]any{"apples": int64(3), "oranges": int64(7)},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{`state.items[0]`, "a"},
		{`state.items[2]`, "c"},
		{`state["counts"]["apples"]`, int64(3)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_StructAndPointer(t *testing.T) {
	type User struct {
		Name string
		Age  int
	}
	u := &User{Name: "Bob", Age: 42}
	env := map[string]any{"user": u}

	got, err := Eval(ctx, "user.Name", env)
	require.NoError(t, err)
	require.Equal(t, "Bob", got)

	got, err = Eval(ctx, "user.Age >= 18", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

type testEnv struct {
	Count int
	Name  string
	Items []int
}

func (e testEnv) Double() int             { return e.Count * 2 }
func (e testEnv) Greet(who string) string { return "Hello, " + who }

type ptrEnv struct {
	Value int
}

func (e *ptrEnv) Triple() int { return e.Value * 3 }

func TestEval_StructEnv(t *testing.T) {
	env := testEnv{Count: 5, Name: "Alice", Items: []int{1, 2, 3}}

	cases := []struct {
		expr string
		want any
	}{
		{"Count", 5},
		{"Count * 2", int64(10)},
		{"Count >= 5", true},
		{`Name + " says hi"`, "Alice says hi"},
		{"len(Items)", 3},
		{"Items[1]", 2},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_StructEnv_Methods(t *testing.T) {
	env := testEnv{Count: 21}

	got, err := Eval(ctx, "Double()", env)
	require.NoError(t, err)
	require.Equal(t, 42, got)

	got, err = Eval(ctx, `Greet("world")`, env)
	require.NoError(t, err)
	require.Equal(t, "Hello, world", got)
}

func TestEval_PointerEnv_WithPointerMethod(t *testing.T) {
	env := &ptrEnv{Value: 7}

	got, err := Eval(ctx, "Value * 2", env)
	require.NoError(t, err)
	require.Equal(t, int64(14), got)

	got, err = Eval(ctx, "Triple()", env)
	require.NoError(t, err)
	require.Equal(t, 21, got)
}

func TestEval_StructEnv_FieldBeatsFunction(t *testing.T) {
	// A struct field named "len" should shadow the len builtin at the
	// root-lookup stage. (This mostly documents the lookup order.)
	type hasLen struct{ Len int }
	env := hasLen{Len: 99}

	got, err := Eval(ctx, "Len", env)
	require.NoError(t, err)
	require.Equal(t, 99, got)
}

func TestEval_EngineEval_StructEnv(t *testing.T) {
	e := New()
	env := testEnv{Count: 10}
	got, err := e.Eval(ctx, "Count * 4", env)
	require.NoError(t, err)
	require.Equal(t, int64(40), got)
}

func TestEval_Builtins(t *testing.T) {
	env := map[string]any{
		"state": map[string]any{
			"name":  "Alice",
			"items": []any{1, 2, 3, 4},
			"tags":  map[string]any{"red": true, "blue": false},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{`len(state.items)`, 4},
		{`len(state.name)`, 5},
		{`upper(state.name)`, "ALICE"},
		{`lower("WORLD")`, "world"},
		{`contains(state.name, "lic")`, true},
		{`contains(state.items, 3)`, true},
		{`has(state.tags, "red")`, true},
		{`has(state.tags, "green")`, false},
		{`int("42")`, int64(42)},
		{`float("3.14")`, 3.14},
		{`string(42)`, "42"},
		{`sprintf("%d + %d = %d", 1, 2, 3)`, "1 + 2 = 3"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(ctx, tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Keys(t *testing.T) {
	env := map[string]any{
		"m": map[string]any{"b": 2, "a": 1, "c": 3},
	}
	got, err := Eval(ctx, "keys(m)", env)
	require.NoError(t, err)
	require.Equal(t, []any{"a", "b", "c"}, got)
}

func TestEngine_CustomFunctions(t *testing.T) {
	e := New(WithFunctions(map[string]any{
		"double": func(n int) int { return n * 2 },
		"greet":  func(name string) string { return "Hello, " + name },
	}))
	got, err := e.Eval(ctx, "double(state.count)", map[string]any{
		"state": map[string]any{"count": int64(21)},
	})
	require.NoError(t, err)
	require.Equal(t, 42, got)

	got, err = e.Eval(ctx, `greet("world")`, nil)
	require.NoError(t, err)
	require.Equal(t, "Hello, world", got)
}

func TestEngine_CustomFunction_WithError(t *testing.T) {
	boom := errors.New("boom")
	e := New(WithFunctions(map[string]any{
		"fail": func() (int, error) { return 0, boom },
	}))
	_, err := e.Eval(ctx, "fail()", nil)
	require.ErrorIs(t, err, boom)
}

func TestEngine_WithoutBuiltins(t *testing.T) {
	e := New(WithoutBuiltins())
	_, err := e.Eval(ctx, "len(state.items)", map[string]any{"state": map[string]any{"items": []any{1}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown function")
}

func TestEngine_ReservedNamesPanic(t *testing.T) {
	require.PanicsWithValue(t,
		`goexpr: function name "state" is reserved by the workflow engine`,
		func() {
			New(WithFunctions(map[string]any{"state": func() {}}))
		},
	)
}

func TestCompile_Reuse(t *testing.T) {
	p, err := Compile("state.x * 2")
	require.NoError(t, err)

	r1, err := p.Run(ctx, map[string]any{"state": map[string]any{"x": int64(5)}})
	require.NoError(t, err)
	require.Equal(t, int64(10), r1)

	r2, err := p.Run(ctx, map[string]any{"state": map[string]any{"x": int64(21)}})
	require.NoError(t, err)
	require.Equal(t, int64(42), r2)

	require.Equal(t, "state.x * 2", p.Source())
}

func TestCompile_SyntaxError(t *testing.T) {
	_, err := Compile("1 + + +")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
}

func TestEval_UnsupportedSyntax(t *testing.T) {
	cases := []string{
		"state.items[1:3]",    // slice expression
		"x.(int)",             // type assertion
		"func() int { 1 }()",  // function literal
		"[]int{1, 2, 3}",      // composite literal
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := Eval(ctx, expr, map[string]any{"state": map[string]any{"items": []any{1, 2, 3}}, "x": 1})
			require.Error(t, err)
		})
	}
}

func TestEngine_CompilerAdapter(t *testing.T) {
	c := New().Compiler()
	ctx := context.Background()

	prog, err := c.Compile(ctx, "state.x > 5")
	require.NoError(t, err)

	v, err := prog.Evaluate(ctx, map[string]any{"state": map[string]any{"x": int64(10)}})
	require.NoError(t, err)
	require.Equal(t, true, v.Value())
	require.True(t, v.IsTruthy())
}

func TestEngine_Template(t *testing.T) {
	ctx := context.Background()
	engine := New().Compiler()

	tmpl, err := script.NewTemplate(engine, "Hello ${state.name}, you are ${state.age} years old")
	require.NoError(t, err)
	got, err := tmpl.Eval(ctx, map[string]any{
		"state": map[string]any{"name": "Alice", "age": int64(30)},
	})
	require.NoError(t, err)
	require.Equal(t, "Hello Alice, you are 30 years old", got)
}

func TestEval_UndefinedIdentifier(t *testing.T) {
	_, err := Eval(ctx, "no_such_var", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined identifier")
}

func TestEval_StringMethods(t *testing.T) {
	// String builtins via WithFunctions — demonstrates Go interop.
	e := New(WithFunctions(map[string]any{
		"trim": strings.TrimSpace,
	}))
	got, err := e.Eval(ctx, `trim("  hi  ")`, nil)
	require.NoError(t, err)
	require.Equal(t, "hi", got)
}
