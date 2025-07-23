package workflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
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

	require.Equal(t, "marshal", activity.Name())
	result, err := activity.Execute(ctx, parameters)
	require.NoError(t, err)
	require.Equal(t, "{\"age\":30,\"name\":\"John\"}", result)
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

	require.Equal(t, "marshal", activity.Name())
	result, err := activity.Execute(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "{\"age\":30,\"name\":\"John\"}", result)

	adapter, ok := activity.(*TypedActivityAdapter[Person, string])
	require.True(t, ok)

	typedFunc, ok := adapter.Activity().(*TypedActivityFunction[Person, string])
	require.True(t, ok)

	require.Equal(t, reflect.TypeOf(Person{}), typedFunc.ParametersType())
	require.Equal(t, reflect.TypeOf(""), typedFunc.ResultType())
}
