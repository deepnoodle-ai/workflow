package script

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTemplate(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		globals     map[string]any
		wantErr     bool
		want        string
		errContains string
	}{
		{
			name:    "plain string without template variables",
			input:   "Hello World",
			globals: nil,
			want:    "Hello World",
		},
		{
			name:  "string with single template variable",
			input: "Hello ${state.name}",
			globals: map[string]any{
				"state": map[string]any{
					"name": "Alice",
				},
			},
			want: "Hello Alice",
		},
		{
			name:  "string with multiple template variables",
			input: "${state.greeting} ${state.name}! The answer is ${40 + 2}",
			globals: map[string]any{
				"state": map[string]any{
					"greeting": "Hello",
					"name":     "Bob",
				},
			},
			want: "Hello Bob! The answer is 42",
		},
		{
			name:    "string with nested expressions",
			input:   "Result: ${1 + (2 * 3)}",
			globals: nil,
			want:    "Result: 7",
		},
		{
			name:        "invalid template syntax - unclosed brace",
			input:       "Hello ${name",
			globals:     map[string]any{"name": "Alice"},
			wantErr:     true,
			errContains: "unclosed template expression",
		},
		{
			name:        "invalid expression inside template",
			input:       "Hello ${1 +}",
			globals:     nil,
			wantErr:     true,
			errContains: "invalid expression",
		},
		{
			name:        "undefined variable",
			input:       "Hello ${undefined_var}",
			globals:     nil,
			wantErr:     true,
			errContains: "undefined variable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewTemplate(NewRisorScriptingEngine(DefaultRisorGlobals()), tt.input)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			require.NotNil(t, s)
			got, err := s.Eval(context.Background(), tt.globals)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRisorScriptingEngine(t *testing.T) {
	ctx := context.Background()
	engine := NewRisorScriptingEngine(DefaultRisorGlobals())

	t.Run("compile and evaluate arithmetic", func(t *testing.T) {
		s, err := engine.Compile(ctx, "1 + 2 * 3")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, int64(7), result.Value())
	})

	t.Run("compile and evaluate string", func(t *testing.T) {
		s, err := engine.Compile(ctx, `"hello" + " " + "world"`)
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, "hello world", result.Value())
	})

	t.Run("compile and evaluate boolean", func(t *testing.T) {
		s, err := engine.Compile(ctx, "10 > 5")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, true, result.Value())
		require.True(t, result.IsTruthy())
	})

	t.Run("evaluate with globals", func(t *testing.T) {
		s, err := engine.Compile(ctx, "state.count + 10")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, map[string]any{
			"state": map[string]any{"count": 5},
		})
		require.NoError(t, err)
		require.Equal(t, int64(15), result.Value())
	})

	t.Run("evaluate list expression", func(t *testing.T) {
		s, err := engine.Compile(ctx, "[1, 2, 3]")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		items, err := result.Items()
		require.NoError(t, err)
		require.Equal(t, []any{int64(1), int64(2), int64(3)}, items)
	})

	t.Run("evaluate map expression", func(t *testing.T) {
		s, err := engine.Compile(ctx, `{name: "Alice", age: 30}`)
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		val := result.Value()
		m, ok := val.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "Alice", m["name"])
		require.Equal(t, int64(30), m["age"])
	})

	t.Run("evaluate null returns empty string", func(t *testing.T) {
		s, err := engine.Compile(ctx, "null")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, "", result.String())
		require.False(t, result.IsTruthy())
		require.Nil(t, result.Value())
	})

	t.Run("evaluate with builtins", func(t *testing.T) {
		s, err := engine.Compile(ctx, "len([1, 2, 3, 4, 5])")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, int64(5), result.Value())
	})

	t.Run("evaluate with math module", func(t *testing.T) {
		s, err := engine.Compile(ctx, "math.sqrt(16)")
		require.NoError(t, err)
		result, err := s.Evaluate(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, 4.0, result.Value())
	})

	t.Run("compile error", func(t *testing.T) {
		_, err := engine.Compile(ctx, "1 + + +")
		require.Error(t, err)
	})

	t.Run("reuse compiled script", func(t *testing.T) {
		s, err := engine.Compile(ctx, "state.x * 2")
		require.NoError(t, err)

		r1, err := s.Evaluate(ctx, map[string]any{"state": map[string]any{"x": 5}})
		require.NoError(t, err)
		require.Equal(t, int64(10), r1.Value())

		r2, err := s.Evaluate(ctx, map[string]any{"state": map[string]any{"x": 20}})
		require.NoError(t, err)
		require.Equal(t, int64(40), r2.Value())
	})
}

func TestRisorValueTruthiness(t *testing.T) {
	ctx := context.Background()
	engine := NewRisorScriptingEngine(DefaultRisorGlobals())

	tests := []struct {
		expr   string
		truthy bool
	}{
		{"true", true},
		{"false", false},
		{"1", true},
		{"0", false},
		{`"hello"`, true},
		{`""`, false},
		{"[1]", true},
		{"[]", false},
		{`{a: 1}`, true},
		{"null", false},
		{"3.14", true},
		{"0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			s, err := engine.Compile(ctx, tt.expr)
			require.NoError(t, err)
			result, err := s.Evaluate(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, tt.truthy, result.IsTruthy(), "expr=%s", tt.expr)
		})
	}
}

func TestConvertValueToBool(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		expect bool
	}{
		{"true bool", true, true},
		{"false bool", false, false},
		{"nonzero int", 42, true},
		{"zero int", 0, false},
		{"nonzero int64", int64(1), true},
		{"zero int64", int64(0), false},
		{"nonzero float64", 3.14, true},
		{"zero float64", 0.0, false},
		{"nonempty string", "hello", true},
		{"empty string", "", false},
		{"false string", "false", false},
		{"nonempty slice", []any{1}, true},
		{"empty slice", []any{}, false},
		{"nonempty map", map[string]any{"a": 1}, true},
		{"empty map", map[string]any{}, false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, ConvertValueToBool(tt.value))
		})
	}
}

func TestConvertEachValue(t *testing.T) {
	t.Run("string slice", func(t *testing.T) {
		result, err := ConvertEachValue([]string{"a", "b", "c"})
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b", "c"}, result)
	})

	t.Run("int slice", func(t *testing.T) {
		result, err := ConvertEachValue([]int{1, 2, 3})
		require.NoError(t, err)
		require.Equal(t, []any{1, 2, 3}, result)
	})

	t.Run("any slice", func(t *testing.T) {
		input := []any{"hello", 42, true}
		result, err := ConvertEachValue(input)
		require.NoError(t, err)
		require.Equal(t, input, result)
	})

	t.Run("map converts to key-value pairs", func(t *testing.T) {
		result, err := ConvertEachValue(map[string]any{"key": "value"})
		require.NoError(t, err)
		require.Len(t, result, 1)
		item := result[0].(map[string]any)
		require.Equal(t, "key", item["key"])
		require.Equal(t, "value", item["value"])
	})

	t.Run("scalar wraps in slice", func(t *testing.T) {
		result, err := ConvertEachValue(42)
		require.NoError(t, err)
		require.Equal(t, []any{42}, result)
	})
}

func TestGetAllowedGlobals(t *testing.T) {
	allowedGlobals := GetAllowedGlobals()
	builtins := DefaultRisorGlobals()

	// All allowed globals should exist in the builtins
	for name := range allowedGlobals {
		_, exists := builtins[name]
		require.True(t, exists, "allowed global %q should exist in builtins", name)
	}
}

// func TestStringEval(t *testing.T) {
// 	tests := []struct {
// 		name        string
// 		input       string
// 		globals     map[string]any
// 		evalGlobals map[string]any // separate globals for evaluation
// 		want        string
// 		wantErr     bool
// 		errContains string
// 	}{
// 		{
// 			name:    "plain string without template variables",
// 			input:   "Hello World",
// 			globals: nil,
// 			want:    "Hello World",
// 		},
// 		{
// 			name:        "string with single string variable",
// 			input:       "Hello ${name}",
// 			globals:     map[string]any{"name": ""},
// 			evalGlobals: map[string]any{"name": "Alice"},
// 			want:        "Hello Alice",
// 		},
// 		{
// 			name:        "string with multiple variables",
// 			input:       "${greeting} ${name}!",
// 			globals:     map[string]any{"greeting": "", "name": ""},
// 			evalGlobals: map[string]any{"greeting": "Hello", "name": "Bob"},
// 			want:        "Hello Bob!",
// 		},
// 		{
// 			name:    "arithmetic expression",
// 			input:   "The answer is ${40 + 2}",
// 			globals: nil,
// 			want:    "The answer is 42",
// 		},
// 		{
// 			name:    "boolean expression",
// 			input:   "Is it true? ${1 < 2}",
// 			globals: nil,
// 			want:    "Is it true? true",
// 		},
// 		{
// 			name:    "float expression",
// 			input:   "Pi is approximately ${3.14159}",
// 			globals: nil,
// 			want:    "Pi is approximately 3.14159",
// 		},
// 		{
// 			name:    "nil value",
// 			input:   "Nil value: ${nil}",
// 			globals: nil,
// 			want:    "Nil value: ",
// 		},
// 		{
// 			name:        "complex expression with globals",
// 			input:       "${user.name} is ${user.age} years old",
// 			globals:     map[string]any{"user": map[string]any{}},
// 			evalGlobals: map[string]any{"user": map[string]any{"name": "Charlie", "age": 30}},
// 			want:        "Charlie is 30 years old",
// 		},
// 		{
// 			name:        "runtime error",
// 			input:       "${1 / 0}",
// 			globals:     nil,
// 			wantErr:     true,
// 			errContains: "integer divide by zero",
// 		},
// 		{
// 			name:    "list",
// 			input:   "Hello ${[1, 'two', true]} There",
// 			globals: nil,
// 			want:    "Hello 1\n\ntwo\n\ntrue There",
// 		},
// 	}

// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			s, err := Compile(tt.input, tt.globals)
// 			require.NoError(t, err)

// 			evalGlobals := tt.evalGlobals
// 			if evalGlobals == nil {
// 				evalGlobals = tt.globals
// 			}

// 			result, err := s.Eval(context.Background(), evalGlobals)
// 			if tt.wantErr {
// 				assert.Error(t, err)
// 				if tt.errContains != "" {
// 					assert.Contains(t, err.Error(), tt.errContains)
// 				}
// 				return
// 			}

// 			assert.NoError(t, err)
// 			assert.Equal(t, tt.want, result)
// 		})
// 	}
// }
