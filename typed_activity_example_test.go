package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func (a *AddActivity) Execute(ctx context.Context, params MathParams) (MathResult, error) {
	return MathResult{Sum: params.A + params.B}, nil
}

func TestTypedActivitySystem(t *testing.T) {
	// Test using struct-based typed activity
	addActivity := NewTypedActivity(&AddActivity{})
	
	params := map[string]any{
		"a": 5,
		"b": 3,
	}
	
	result, err := addActivity.Execute(context.Background(), params)
	require.NoError(t, err)
	
	mathResult, ok := result.(MathResult)
	require.True(t, ok, "Expected MathResult type")
	assert.Equal(t, 8, mathResult.Sum)
	
	// Test using TypedActivityFunction
	multiplyActivity := TypedActivityFunction("math.multiply", func(ctx context.Context, params MathParams) (MathResult, error) {
		return MathResult{Sum: params.A * params.B}, nil
	})
	
	result2, err := multiplyActivity.Execute(context.Background(), params)
	require.NoError(t, err)
	
	mathResult2, ok := result2.(MathResult)
	require.True(t, ok, "Expected MathResult type")
	assert.Equal(t, 15, mathResult2.Sum)
}
