package workflow

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
)

// Example of a typed activity for math operations
type MathParams struct {
	A int `mapstructure:"a"`
	B int `mapstructure:"b"`
}

type MathResult struct {
	Sum int `json:"sum"`
}

type AddActivity struct{}

func (a *AddActivity) Name() string {
	return "math.add"
}

func (a *AddActivity) Execute(ctx Context, params MathParams) (MathResult, error) {
	return MathResult{Sum: params.A + params.B}, nil
}

func TestTypedActivitySystem(t *testing.T) {
	// Test using struct-based typed activity
	addActivity := NewTypedActivity(&AddActivity{})

	params := map[string]any{
		"a": 5,
		"b": 3,
	}

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: &PathLocalState{},
		Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
		PathID:         "path1",
		StepName:       "step1",
	})

	result, err := addActivity.Execute(ctx, params)
	assert.NoError(t, err)

	mathResult, ok := result.(MathResult)
	assert.True(t, ok, "Expected MathResult type")
	assert.Equal(t, mathResult.Sum, 8)

	// Test using TypedActivityFunction
	multiplyActivity := NewTypedActivityFunction("math.multiply",
		func(ctx Context, params MathParams) (MathResult, error) {
			return MathResult{Sum: params.A * params.B}, nil
		})

	result2, err := multiplyActivity.Execute(ctx, params)
	assert.NoError(t, err)

	mathResult2, ok := result2.(MathResult)
	assert.True(t, ok, "Expected MathResult type")
	assert.Equal(t, mathResult2.Sum, 15)
}
