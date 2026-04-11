package workflow

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestDefaultScriptCompiler(t *testing.T) {
	c := DefaultScriptCompiler()
	require.NotNil(t, c)

	ctx := context.Background()
	globals := map[string]any{
		"state": map[string]any{
			"counter": 3,
			"name":    "ada",
			"items":   []any{1, 2, 3, 4},
		},
		"inputs": map[string]any{
			"max": 5,
		},
	}

	t.Run("arithmetic and comparison", func(t *testing.T) {
		s, err := c.Compile(ctx, "state.counter + 1")
		require.NoError(t, err)
		v, err := s.Evaluate(ctx, globals)
		require.NoError(t, err)
		require.EqualValues(t, 4, v.Value())
	})

	t.Run("boolean condition", func(t *testing.T) {
		s, err := c.Compile(ctx, `state.counter < inputs.max && state.name == "ada"`)
		require.NoError(t, err)
		v, err := s.Evaluate(ctx, globals)
		require.NoError(t, err)
		require.True(t, v.IsTruthy())
	})

	t.Run("iteration via Items()", func(t *testing.T) {
		s, err := c.Compile(ctx, "state.items")
		require.NoError(t, err)
		v, err := s.Evaluate(ctx, globals)
		require.NoError(t, err)
		items, err := v.Items()
		require.NoError(t, err)
		require.Equal(t, 4, len(items))
	})

	t.Run("string interpolation via Value().String()", func(t *testing.T) {
		s, err := c.Compile(ctx, "state.name")
		require.NoError(t, err)
		v, err := s.Evaluate(ctx, globals)
		require.NoError(t, err)
		require.Equal(t, "ada", v.String())
	})

	t.Run("compile error on invalid syntax", func(t *testing.T) {
		_, err := c.Compile(ctx, "state.counter +")
		require.Error(t, err)
	})

	t.Run("evaluate error on missing identifier", func(t *testing.T) {
		s, err := c.Compile(ctx, "state.does_not_exist + 1")
		require.NoError(t, err)
		_, err = s.Evaluate(ctx, globals)
		require.Error(t, err)
	})

	t.Run("respects ctx cancellation at compile time", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := c.Compile(canceled, "1 + 1")
		require.Error(t, err)
	})
}

// TestDefaultCompilerWiring ensures NewExecution wires DefaultScriptCompiler
// when ExecutionOptions.ScriptCompiler is nil — the autowire is the only
// reason consumers get a working engine without having to import expr
// themselves.
func TestDefaultCompilerWiring(t *testing.T) {
	w, err := New(Options{
		Name:   "wiring",
		Inputs: []*Input{{Name: "who", Type: "string"}},
		Steps: []*Step{
			{
				Name:     "Echo",
				Activity: "echo",
				Parameters: map[string]any{
					"value": "${inputs.who}",
				},
			},
		},
	})
	require.NoError(t, err)

	var captured string
	echo := ActivityFunc("echo", func(ctx Context, params map[string]any) (any, error) {
		captured, _ = params["value"].(string)
		return nil, nil
	})

	reg := NewActivityRegistry()
	reg.MustRegister(echo)
	exec, err := NewExecution(w, reg,
		WithInputs(map[string]any{"who": "world"}),
	)
	require.NoError(t, err)
	_, err = exec.Execute(context.Background())
	require.NoError(t, err)
	require.Equal(t, "world", captured)
}
