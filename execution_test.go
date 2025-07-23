package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/state"
	"github.com/stretchr/testify/require"
)

func TestNewExecutionValidation(t *testing.T) {
	t.Run("missing workflow returns error", func(t *testing.T) {
		_, err := NewExecution(ExecutionOptions{
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
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
			NewActivityFunction("time.now", func(ctx Context, params map[string]any) (any, error) {
				return "2025-07-21T12:00:00Z", nil
			}),
			NewActivityFunction("print", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("math", func(ctx Context, params map[string]any) (any, error) {
					return 42, nil
				}),
				NewActivityFunction("message", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("data", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
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
				NewActivityFunction("fail", func(ctx Context, params map[string]any) (any, error) {
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

func TestExecutionResumeFromCheckpoint(t *testing.T) {
	t.Run("resume failed execution and succeed", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := NewFileCheckpointer(tempDir)
		require.NoError(t, err)

		// Track how many times the flaky activity is called
		callCount := 0

		// Create workflow with a flaky activity that fails first time but succeeds second time
		wf, err := New(Options{
			Name: "resume-test-workflow",
			Steps: []*Step{
				{Name: "setup", Activity: "setup", Store: "setup_data", Next: []*Edge{{Step: "flaky"}}},
				{Name: "flaky", Activity: "flaky", Store: "result"},
			},
		})
		require.NoError(t, err)

		// First execution - should fail
		execution1, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}),
				NewActivityFunction("flaky", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					if callCount == 1 {
						return nil, errors.New("flaky failure on first attempt")
					}
					return "success on retry", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run first execution (should fail)
		err = execution1.Run(context.Background())
		require.Error(t, err)
		require.Equal(t, ExecutionStatusFailed, execution1.Status())

		// Verify checkpoint was saved
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, "failed", checkpoint.Status)

		// Create second execution to resume from the first one's checkpoint
		execution2, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}),
				NewActivityFunction("flaky", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					if callCount == 1 {
						return nil, errors.New("flaky failure on first attempt")
					}
					return "success on retry", nil
				}),
			},
		})
		require.NoError(t, err)

		// Resume from the failed execution
		err = execution2.Resume(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution2.Status())

		// Verify the flaky activity was called twice (once in each execution)
		require.Equal(t, 2, callCount)

		// Verify final checkpoint shows success
		finalCheckpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution2.ID())
		require.NoError(t, err)
		require.NotNil(t, finalCheckpoint)
		require.Equal(t, "completed", finalCheckpoint.Status)
	})

	t.Run("resume completed execution does nothing", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := NewFileCheckpointer(tempDir)
		require.NoError(t, err)

		// Create simple successful workflow
		wf, err := New(Options{
			Name: "completed-test-workflow",
			Steps: []*Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		require.NoError(t, err)

		// First execution - should succeed
		execution1, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					return "success", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run first execution (should succeed)
		err = execution1.Run(context.Background())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution1.Status())

		// Verify checkpoint was saved
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, "completed", checkpoint.Status)

		// Create second execution to resume from completed one
		execution2, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					t.Fatal("test activity should not be called when resuming completed execution")
					return nil, nil
				}),
			},
		})
		require.NoError(t, err)

		// Resume from the completed execution (should be no-op)
		err = execution2.Resume(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution2.Status())
	})

	t.Run("resume nonexistent execution returns error", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := NewFileCheckpointer(tempDir)
		require.NoError(t, err)

		// Create simple workflow
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		require.NoError(t, err)

		// Create execution
		execution, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					return "success", nil
				}),
			},
		})
		require.NoError(t, err)

		// Try to resume from nonexistent execution ID
		err = execution.Resume(context.Background(), "nonexistent-execution-id")
		require.Error(t, err)
		require.Contains(t, err.Error(), "no checkpoint found")
	})

	t.Run("resume with retry mechanism works", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := NewFileCheckpointer(tempDir)
		require.NoError(t, err)

		// Track how many times the retry activity is called
		callCount := 0

		// Create workflow with a step that has retry configuration
		wf, err := New(Options{
			Name: "retry-resume-test-workflow",
			Steps: []*Step{
				{
					Name:     "setup",
					Activity: "setup",
					Store:    "setup_data",
					Next:     []*Edge{{Step: "retry-step"}},
				},
				{
					Name:     "retry-step",
					Activity: "retry-activity",
					Store:    "result",
					Retry: []*RetryConfig{
						{
							ErrorEquals: []string{ErrorTypeActivityFailed},
							MaxRetries:  2, // Allow 2 retries (3 total attempts)
						},
					},
				},
			},
		})
		require.NoError(t, err)

		// First execution - should exhaust retries and fail
		execution1, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}),
				NewActivityFunction("retry-activity", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					// Fail for the first 4 attempts (initial + 2 retries in first execution + 1 attempt in resumed execution)
					if callCount <= 4 {
						return nil, errors.New("activity failure - attempt " + fmt.Sprintf("%d", callCount))
					}
					return "success after retries", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run first execution (should fail after exhausting retries)
		err = execution1.Run(context.Background())
		require.Error(t, err)
		require.Equal(t, ExecutionStatusFailed, execution1.Status())

		// At this point, callCount should be 3 (initial attempt + 2 retries)
		require.Equal(t, 3, callCount)

		// Verify checkpoint was saved with failed status
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, "failed", checkpoint.Status)

		// Create second execution to resume from the first one's checkpoint
		execution2, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}),
				NewActivityFunction("retry-activity", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					// Fail for the first 4 attempts, succeed on the 5th
					if callCount <= 4 {
						return nil, errors.New("activity failure - attempt " + fmt.Sprintf("%d", callCount))
					}
					return "success after retries", nil
				}),
			},
		})
		require.NoError(t, err)

		// Resume from the failed execution - should retry again and succeed
		err = execution2.Resume(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution2.Status())

		// Verify the retry activity was called 5 times total:
		// - First execution: 3 attempts (initial + 2 retries)
		// - Resumed execution: 2 more attempts (restart + 1 retry) = 5 total
		require.Equal(t, 5, callCount)

		// Verify final checkpoint shows success
		finalCheckpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution2.ID())
		require.NoError(t, err)
		require.NotNil(t, finalCheckpoint)
		require.Equal(t, "completed", finalCheckpoint.Status)
	})
}

func TestPathBranching(t *testing.T) {
	t.Run("simple conditional branching creates two paths", func(t *testing.T) {
		// Track which activities were called
		var executedActivities []string
		var activityMutex sync.Mutex

		addExecutedActivity := func(name string) {
			activityMutex.Lock()
			defer activityMutex.Unlock()
			executedActivities = append(executedActivities, name)
		}

		// Create workflow with conditional branching
		wf, err := New(Options{
			Name: "simple-branching-test",
			Steps: []*Step{
				{
					Name:     "setup",
					Activity: "setup",
					Store:    "condition_value",
					Next: []*Edge{
						{Step: "path_a", Condition: "state.condition_value == 'A'"},
						{Step: "path_b", Condition: "state.condition_value == 'B'"},
					},
				},
				{
					Name:     "path_a",
					Activity: "activity_a",
					Store:    "result_a",
				},
				{
					Name:     "path_b",
					Activity: "activity_b",
					Store:    "result_b",
				},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup", func(ctx Context, params map[string]any) (any, error) {
					addExecutedActivity("setup")
					// Set up state that will cause both branches to be taken
					return "A", nil // This will only match path_a condition
				}),
				NewActivityFunction("activity_a", func(ctx Context, params map[string]any) (any, error) {
					addExecutedActivity("activity_a")
					return "result from path A", nil
				}),
				NewActivityFunction("activity_b", func(ctx Context, params map[string]any) (any, error) {
					addExecutedActivity("activity_b")
					return "result from path B", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify only the matching path was executed
		require.Contains(t, executedActivities, "setup")
		require.Contains(t, executedActivities, "activity_a")
		require.NotContains(t, executedActivities, "activity_b")
	})

	t.Run("multiple conditional branches with state isolation", func(t *testing.T) {
		// Track activity executions with their path context
		type ActivityExecution struct {
			Activity string
			PathData map[string]any
		}
		var executions []ActivityExecution
		var executionMutex sync.Mutex

		recordExecution := func(activity string, stateReader state.State) {
			executionMutex.Lock()
			defer executionMutex.Unlock()
			executions = append(executions, ActivityExecution{
				Activity: activity,
				PathData: copyMap(stateReader.GetVariables()),
			})
		}

		// Create workflow with multiple branches
		wf, err := New(Options{
			Name: "multi-branch-test",
			Steps: []*Step{
				{
					Name:     "initial_setup",
					Activity: "setup_data",
					Store:    "base_value",
					Next: []*Edge{
						{Step: "branch_small", Condition: "state.base_value < 5"},
						{Step: "branch_medium", Condition: "state.base_value >= 5 && state.base_value < 10"},
						{Step: "branch_large", Condition: "state.base_value >= 10"},
					},
				},
				{
					Name:     "branch_small",
					Activity: "process_small",
					Store:    "small_result",
					Next:     []*Edge{{Step: "final_step"}},
				},
				{
					Name:     "branch_medium",
					Activity: "process_medium",
					Store:    "medium_result",
					Next:     []*Edge{{Step: "final_step"}},
				},
				{
					Name:     "branch_large",
					Activity: "process_large",
					Store:    "large_result",
					Next:     []*Edge{{Step: "final_step"}},
				},
				{
					Name:     "final_step",
					Activity: "final_activity",
					Store:    "final_result",
				},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup_data", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					recordExecution("setup_data", pathState)
					return 7, nil // Should trigger branch_medium
				}),
				NewActivityFunction("process_small", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					recordExecution("process_small", pathState)
					// Modify state in this path
					if localState, ok := pathState.(*PathLocalState); ok {
						localState.SetVariable("branch_type", "small")
					}
					return "small processed", nil
				}),
				NewActivityFunction("process_medium", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					recordExecution("process_medium", pathState)
					// Modify state in this path
					if localState, ok := pathState.(*PathLocalState); ok {
						localState.SetVariable("branch_type", "medium")
					}
					return "medium processed", nil
				}),
				NewActivityFunction("process_large", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					recordExecution("process_large", pathState)
					// Modify state in this path
					if localState, ok := pathState.(*PathLocalState); ok {
						localState.SetVariable("branch_type", "large")
					}
					return "large processed", nil
				}),
				NewActivityFunction("final_activity", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					recordExecution("final_activity", pathState)
					return "workflow completed", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify correct execution path
		var activityNames []string
		for _, exec := range executions {
			activityNames = append(activityNames, exec.Activity)
		}

		require.Contains(t, activityNames, "setup_data")
		require.Contains(t, activityNames, "process_medium") // base_value=7 should trigger medium branch
		require.Contains(t, activityNames, "final_activity")
		require.NotContains(t, activityNames, "process_small")
		require.NotContains(t, activityNames, "process_large")

		// Verify state was correctly propagated and modified
		for _, exec := range executions {
			if exec.Activity == "process_medium" {
				require.Equal(t, 7, exec.PathData["base_value"])
			}
			if exec.Activity == "final_activity" {
				require.Equal(t, 7, exec.PathData["base_value"])
				require.Equal(t, "medium", exec.PathData["branch_type"])
				require.Equal(t, "medium processed", exec.PathData["medium_result"])
			}
		}
	})

	t.Run("parallel branching with unconditional edges", func(t *testing.T) {
		// Track parallel executions
		var parallelPaths []string
		var pathMutex sync.Mutex

		recordPathExecution := func(pathName string) {
			pathMutex.Lock()
			defer pathMutex.Unlock()
			parallelPaths = append(parallelPaths, pathName)
		}

		// Create workflow with unconditional parallel branches
		wf, err := New(Options{
			Name: "parallel-branching-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Store:    "start_data",
					Next: []*Edge{
						{Step: "parallel_path_1"}, // No condition = always execute
						{Step: "parallel_path_2"}, // No condition = always execute
						{Step: "parallel_path_3"}, // No condition = always execute
					},
				},
				{
					Name:     "parallel_path_1",
					Activity: "work_1",
					Store:    "result_1",
				},
				{
					Name:     "parallel_path_2",
					Activity: "work_2",
					Store:    "result_2",
				},
				{
					Name:     "parallel_path_3",
					Activity: "work_3",
					Store:    "result_3",
				},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("start_activity", func(ctx Context, params map[string]any) (any, error) {
					recordPathExecution("start")
					return "initialized", nil
				}),
				NewActivityFunction("work_1", func(ctx Context, params map[string]any) (any, error) {
					recordPathExecution("path_1")
					// Simulate some work
					time.Sleep(10 * time.Millisecond)
					return "work 1 completed", nil
				}),
				NewActivityFunction("work_2", func(ctx Context, params map[string]any) (any, error) {
					recordPathExecution("path_2")
					// Simulate some work
					time.Sleep(15 * time.Millisecond)
					return "work 2 completed", nil
				}),
				NewActivityFunction("work_3", func(ctx Context, params map[string]any) (any, error) {
					recordPathExecution("path_3")
					// Simulate some work
					time.Sleep(5 * time.Millisecond)
					return "work 3 completed", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify all parallel paths were executed
		require.Contains(t, parallelPaths, "start")
		require.Contains(t, parallelPaths, "path_1")
		require.Contains(t, parallelPaths, "path_2")
		require.Contains(t, parallelPaths, "path_3")
		require.Len(t, parallelPaths, 4) // start + 3 parallel paths
	})

	t.Run("branching with failure in one path does not affect execution completion", func(t *testing.T) {
		var completedPaths []string
		var pathMutex sync.Mutex

		recordCompletion := func(pathName string) {
			pathMutex.Lock()
			defer pathMutex.Unlock()
			completedPaths = append(completedPaths, pathName)
		}

		// Create workflow where one branch will fail
		wf, err := New(Options{
			Name: "branching-with-failure-test",
			Steps: []*Step{
				{
					Name:     "setup",
					Activity: "setup_activity",
					Store:    "setup_complete",
					Next: []*Edge{
						{Step: "success_path", Condition: "true"}, // Always execute
						{Step: "failure_path", Condition: "true"}, // Always execute (will fail)
					},
				},
				{
					Name:     "success_path",
					Activity: "success_activity",
					Store:    "success_result",
				},
				{
					Name:     "failure_path",
					Activity: "failure_activity",
					Store:    "failure_result",
				},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup_activity", func(ctx Context, params map[string]any) (any, error) {
					recordCompletion("setup")
					return "setup complete", nil
				}),
				NewActivityFunction("success_activity", func(ctx Context, params map[string]any) (any, error) {
					recordCompletion("success_path")
					return "success result", nil
				}),
				NewActivityFunction("failure_activity", func(ctx Context, params map[string]any) (any, error) {
					recordCompletion("failure_path_attempted")
					return nil, errors.New("intentional failure in one branch")
				}),
			},
		})
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.Error(t, err) // Execution should fail due to the failed path
		require.Equal(t, ExecutionStatusFailed, execution.Status())

		// Verify setup ran and both paths were attempted
		require.Contains(t, completedPaths, "setup")
		require.Contains(t, completedPaths, "success_path")
		require.Contains(t, completedPaths, "failure_path_attempted")
	})

	t.Run("parallel paths have completely isolated state variables", func(t *testing.T) {
		// Track state access and modifications from each path to verify isolation
		var pathExecutions []string
		var pathMutex sync.Mutex

		recordPathExecution := func(pathName string) {
			pathMutex.Lock()
			defer pathMutex.Unlock()
			pathExecutions = append(pathExecutions, pathName)
		}

		// Create workflow with unconditional parallel branches that modify the same variable names
		wf, err := New(Options{
			Name: "state-isolation-test",
			Steps: []*Step{
				{
					Name:     "setup",
					Activity: "setup_initial_state",
					Store:    "shared_counter",
					Next: []*Edge{
						{Step: "path_alpha"}, // No condition = always execute
						{Step: "path_beta"},  // No condition = always execute
						{Step: "path_gamma"}, // No condition = always execute
					},
				},
				{
					Name:     "path_alpha",
					Activity: "modify_state_alpha",
					Store:    "final_value",
				},
				{
					Name:     "path_beta",
					Activity: "modify_state_beta",
					Store:    "final_value",
				},
				{
					Name:     "path_gamma",
					Activity: "modify_state_gamma",
					Store:    "final_value",
				},
			},
		})
		require.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup_initial_state", func(ctx Context, params map[string]any) (any, error) {
					// Initialize shared counter
					return 100, nil
				}),
				NewActivityFunction("modify_state_alpha", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					if localState, ok := pathState.(*PathLocalState); ok {
						// Verify we start with the setup value
						require.Equal(t, 100, localState.GetVariables()["shared_counter"])

						// Each path modifies the same variable name with different values
						localState.SetVariable("shared_counter", 200)
						localState.SetVariable("path_identifier", "ALPHA")
						localState.SetVariable("multiplier", 2)

						// Verify our changes are visible to us
						require.Equal(t, 200, localState.GetVariables()["shared_counter"])
						require.Equal(t, "ALPHA", localState.GetVariables()["path_identifier"])
					}
					recordPathExecution("alpha")
					return "alpha-200", nil
				}),
				NewActivityFunction("modify_state_beta", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx.PathLocalState
					if localState, ok := pathState.(*PathLocalState); ok {
						// Verify we start with the setup value (not alpha's modification)
						require.Equal(t, 100, localState.GetVariables()["shared_counter"])

						// Each path modifies the same variable name with different values
						localState.SetVariable("shared_counter", 300)
						localState.SetVariable("path_identifier", "BETA")
						localState.SetVariable("multiplier", 3)

						// Verify our changes are visible to us
						require.Equal(t, 300, localState.GetVariables()["shared_counter"])
						require.Equal(t, "BETA", localState.GetVariables()["path_identifier"])
					}
					recordPathExecution("beta")
					return "beta-300", nil
				}),
				NewActivityFunction("modify_state_gamma", func(ctx Context, params map[string]any) (any, error) {
					pathState := ctx
					if localState, ok := pathState.(*PathLocalState); ok {
						// Verify we start with the setup value (not alpha's or beta's modifications)
						require.Equal(t, 100, localState.GetVariables()["shared_counter"])

						// Each path modifies the same variable name with different values
						localState.SetVariable("shared_counter", 400)
						localState.SetVariable("path_identifier", "GAMMA")
						localState.SetVariable("multiplier", 4)

						// Verify our changes are visible to us
						require.Equal(t, 400, localState.GetVariables()["shared_counter"])
						require.Equal(t, "GAMMA", localState.GetVariables()["path_identifier"])
					}
					recordPathExecution("gamma")
					return "gamma-400", nil
				}),
			},
		})
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify all three paths executed
		require.Contains(t, pathExecutions, "alpha")
		require.Contains(t, pathExecutions, "beta")
		require.Contains(t, pathExecutions, "gamma")
		require.Len(t, pathExecutions, 3)
	})
}
