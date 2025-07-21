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
