package workflow_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/stretchr/testify/require"
)

func TestWorkflowLibraryExample(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	wf, err := workflow.New(workflow.Options{
		Name: "data-processing",
		Steps: []*workflow.Step{
			{
				Name:     "Get Current Time",
				Activity: "time.now",
				Store:    "start_time",
				Next:     []*workflow.Edge{{Step: "Print Current Time"}},
			},
			{
				Name:     "Print Current Time",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Processing started at ${state.start_time}",
				},
			},
		},
	})
	require.NoError(t, err)

	gotMessage := ""

	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow: wf,
		Inputs:   map[string]any{},
		Logger:   logger,
		Activities: []workflow.Activity{
			workflow.NewActivityFunction("time.now", func(ctx context.Context, params map[string]any) (any, error) {
				return "2025-07-21T12:00:00Z", nil
			}),
			workflow.NewActivityFunction("print", func(ctx context.Context, params map[string]any) (any, error) {
				message, ok := params["message"]
				if !ok {
					return nil, errors.New("print activity requires 'message' parameter")
				}
				gotMessage = message.(string)
				return nil, nil
			}),
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, execution.Run(ctx))
	require.Equal(t, workflow.ExecutionStatusCompleted, execution.Status())
	require.Equal(t, "Processing started at 2025-07-21T12:00:00Z", gotMessage)
}
