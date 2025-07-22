package workflow

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewExecutionValidation(t *testing.T) {
	t.Run("missing workflow returns error", func(t *testing.T) {
		_, err := NewExecution(ExecutionOptions{
			Activities: []Activity{
				NewActivityFunction("test", func(ctx context.Context, params map[string]any) (any, error) {
					return nil, nil
				}),
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow is required")
	})

	t.Run("empty activities slice returns error", func(t *testing.T) {
		wf, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: "start", Activity: "test"}},
		})
		require.NoError(t, err)

		_, err = NewExecution(ExecutionOptions{Workflow: wf})
		require.Error(t, err)
		require.Contains(t, err.Error(), "activities are required")
	})

	t.Run("unknown input is rejected", func(t *testing.T) {
		wf, err := New(Options{
			Name:   "test-workflow",
			Inputs: []*Input{{Name: "valid_input", Type: "string"}},
			Steps:  []*Step{{Name: "start", Activity: "test"}},
		})
		require.NoError(t, err)

		_, err = NewExecution(ExecutionOptions{
			Workflow: wf,
			Inputs: map[string]any{
				"valid_input":   "good",
				"unknown_input": "bad", // unknown input
			},
			Activities: []Activity{nil},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unknown input")
	})

	t.Run("required input without default causes error", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Inputs: []*Input{
				{Name: "required_input", Type: "string"}, // no default
			},
			Steps: []*Step{
				{Name: "start", Activity: "test"},
			},
		})
		require.NoError(t, err)

		_, err = NewExecution(ExecutionOptions{
			Workflow: wf,
			Inputs:   map[string]any{}, // missing required input
			Activities: []Activity{
				NewActivityFunction("test", func(ctx context.Context, params map[string]any) (any, error) {
					return nil, nil
				}),
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "required_input")
		require.Contains(t, err.Error(), "is required")
	})

	t.Run("valid configuration creates execution successfully", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow",
			Inputs: []*Input{
				{Name: "optional_input", Type: "string", Default: "default_value"},
			},
			Steps: []*Step{
				{Name: "start", Activity: "test"},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Inputs: map[string]any{
				"optional_input": "provided_value",
			},
			Activities: []Activity{
				NewActivityFunction("test", func(ctx context.Context, params map[string]any) (any, error) {
					return nil, nil
				}),
			},
		})
		require.NoError(t, err)
		require.NotNil(t, execution)
		require.NotEmpty(t, execution.ID())
	})
}

func TestWorkflowLibraryExample(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	wf, err := New(Options{
		Name: "data-processing",
		Steps: []*Step{
			{
				Name:     "Get Current Time",
				Activity: "time.now",
				Store:    "start_time",
				Next:     []*Edge{{Step: "Print Current Time"}},
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

	execution, err := NewExecution(ExecutionOptions{
		Workflow: wf,
		Inputs:   map[string]any{},
		Logger:   logger,
		Activities: []Activity{
			NewActivityFunction("time.now", func(ctx context.Context, params map[string]any) (any, error) {
				return "2025-07-21T12:00:00Z", nil
			}),
			NewActivityFunction("print", func(ctx context.Context, params map[string]any) (any, error) {
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
	require.Equal(t, ExecutionStatusCompleted, execution.Status())
	require.Equal(t, "Processing started at 2025-07-21T12:00:00Z", gotMessage)
}

func TestWorkflowOutputCapture(t *testing.T) {
	t.Run("basic output capture", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow-with-outputs",
			Steps: []*Step{
				{
					Name:     "calculate-result",
					Activity: "math",
					Store:    "calculation",
					Next:     []*Edge{{Step: "store-message"}},
				},
				{
					Name:     "store-message",
					Activity: "message",
					Store:    "final_message",
				},
			},
			Outputs: []*Output{
				{Name: "result", Variable: "calculation"},
				{Name: "message", Variable: "final_message"},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("math", func(ctx context.Context, params map[string]any) (any, error) {
					return 42, nil
				}),
				NewActivityFunction("message", func(ctx context.Context, params map[string]any) (any, error) {
					return "workflow completed successfully", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		require.NoError(t, execution.Run(ctx))
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify outputs are captured correctly
		outputs := execution.GetOutputs()
		require.NotNil(t, outputs)
		require.Equal(t, 42, outputs["result"])
		require.Equal(t, "workflow completed successfully", outputs["message"])
	})

	t.Run("output with missing variable returns error", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow-missing-output",
			Steps: []*Step{
				{Name: "some-step", Activity: "test", Store: "some_variable"},
			},
			Outputs: []*Output{
				{Name: "missing_output", Variable: "nonexistent_variable"},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx context.Context, params map[string]any) (any, error) {
					return "value", nil
				}),
			},
		})
		require.NoError(t, err)
		err = execution.Run(context.Background())
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow output variable \"nonexistent_variable\" not found")
		require.Equal(t, ExecutionStatusFailed, execution.Status())
	})

	t.Run("workflow with no outputs defined", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow-no-outputs",
			Steps: []*Step{
				{
					Name:     "simple-step",
					Activity: "test",
					Store:    "some_value",
				},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx context.Context, params map[string]any) (any, error) {
					return "test result", nil
				}),
			},
		})
		require.NoError(t, err)
		require.NoError(t, execution.Run(context.Background()))
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Should have empty outputs map
		outputs := execution.GetOutputs()
		require.NotNil(t, outputs)
		require.Empty(t, outputs)
	})

	t.Run("output variable defaults to output name", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow-default-variable",
			Steps: []*Step{
				{Name: "store-data", Activity: "data", Store: "status"},
			},
			Outputs: []*Output{{Name: "status"}},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("data", func(ctx context.Context, params map[string]any) (any, error) {
					return "GREAT SUCCESS", nil
				}),
			},
		})
		require.NoError(t, err)

		require.NoError(t, execution.Run(context.Background()))
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify output is captured using default variable name
		outputs := execution.GetOutputs()
		require.NotNil(t, outputs)
		require.Equal(t, "GREAT SUCCESS", outputs["status"])
	})
}

func TestFileCheckpointerSavesCheckpoints(t *testing.T) {
	t.Run("successful workflow saves checkpoints", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := NewFileCheckpointer(tempDir)
		require.NoError(t, err)

		// Create simple workflow
		wf, err := New(Options{
			Name: "checkpoint-test-success",
			Steps: []*Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		require.NoError(t, err)

		// Create execution with FileCheckpointer
		execution, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx context.Context, params map[string]any) (any, error) {
					return "success", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run the workflow
		require.NoError(t, execution.Run(context.Background()))
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify checkpoint files were created
		executionDir := tempDir + "/" + execution.ID()

		// Check that execution directory exists
		_, err = os.Stat(executionDir)
		require.NoError(t, err, "execution directory should exist")

		// Check that latest.json exists
		latestFile := executionDir + "/latest.json"
		_, err = os.Stat(latestFile)
		require.NoError(t, err, "latest.json should exist")

		// Verify we can load the checkpoint
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, execution.ID(), checkpoint.ExecutionID)
		require.Equal(t, "checkpoint-test-success", checkpoint.WorkflowName)
		require.Equal(t, "completed", checkpoint.Status)
	})

	t.Run("failed workflow saves checkpoints", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := NewFileCheckpointer(tempDir)
		require.NoError(t, err)

		// Create simple workflow that will fail
		wf, err := New(Options{
			Name: "checkpoint-test-failure",
			Steps: []*Step{
				{Name: "failing-step", Activity: "fail"},
			},
		})
		require.NoError(t, err)

		// Create execution with FileCheckpointer
		execution, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("fail", func(ctx context.Context, params map[string]any) (any, error) {
					return nil, errors.New("intentional test failure")
				}),
			},
		})
		require.NoError(t, err)

		// Run the workflow (expect failure)
		err = execution.Run(context.Background())
		require.Error(t, err)
		require.Equal(t, ExecutionStatusFailed, execution.Status())

		// Verify checkpoint files were created even for failed execution
		executionDir := tempDir + "/" + execution.ID()

		// Check that execution directory exists
		_, err = os.Stat(executionDir)
		require.NoError(t, err, "execution directory should exist")

		// Check that latest.json exists
		latestFile := executionDir + "/latest.json"
		_, err = os.Stat(latestFile)
		require.NoError(t, err, "latest.json should exist")

		// Verify we can load the checkpoint and it shows failed status
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, execution.ID(), checkpoint.ExecutionID)
		require.Equal(t, "checkpoint-test-failure", checkpoint.WorkflowName)
		require.Equal(t, "failed", checkpoint.Status)
		require.NotEmpty(t, checkpoint.Error)
	})
}
