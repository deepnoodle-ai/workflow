package workflow

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
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
