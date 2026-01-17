package workflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestActivityFunction(t *testing.T) {
	activity := NewActivityFunction(
		"marshal",
		func(ctx Context, parameters map[string]any) (any, error) {
			data, err := json.Marshal(parameters)
			if err != nil {
				return nil, err
			}
			return string(data), nil
		},
	)

	parameters := map[string]any{
		"age":  30,
		"name": "John",
	}

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: &PathLocalState{},
		Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
		PathID:         "path1",
		StepName:       "step1",
	})

	assert.Equal(t, activity.Name(), "marshal")
	result, err := activity.Execute(ctx, parameters)
	assert.NoError(t, err)
	assert.Equal(t, result, "{\"age\":30,\"name\":\"John\"}")
}

func TestTypedActivityFunction(t *testing.T) {

	type Person struct {
		Age  int    `json:"age"`
		Name string `json:"name"`
	}

	activity := NewTypedActivityFunction(
		"marshal",
		func(ctx Context, person Person) (string, error) {
			data, err := json.Marshal(person)
			if err != nil {
				return "", err
			}
			return string(data), nil
		},
	)

	ctx := NewContext(context.Background(), ExecutionContextOptions{
		PathLocalState: &PathLocalState{},
		Logger:         slog.New(slog.NewTextHandler(os.Stdout, nil)),
		PathID:         "path1",
		StepName:       "step1",
	})

	input := map[string]any{"age": 30, "name": "John"}

	assert.Equal(t, activity.Name(), "marshal")
	result, err := activity.Execute(ctx, input)
	assert.NoError(t, err)
	assert.Equal(t, result, "{\"age\":30,\"name\":\"John\"}")

	adapter, ok := activity.(*TypedActivityAdapter[Person, string])
	assert.True(t, ok)

	typedFunc, ok := adapter.Activity().(*TypedActivityFunction[Person, string])
	assert.True(t, ok)

	assert.True(t, typedFunc.ParametersType() == reflect.TypeOf(Person{}))
	assert.True(t, typedFunc.ResultType() == reflect.TypeOf(""))
}
