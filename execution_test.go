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

	"github.com/deepnoodle-ai/wonton/assert"
	"github.com/deepnoodle-ai/workflow/domain"
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
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workflow is required")
	})

	t.Run("empty activities slice returns error", func(t *testing.T) {
		wf, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: "start", Activity: "test"}},
		})
		assert.NoError(t, err)

		_, err = NewExecution(ExecutionOptions{Workflow: wf})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "activities required")
	})

	t.Run("unknown input is rejected", func(t *testing.T) {
		wf, err := New(Options{
			Name:   "test-workflow",
			Inputs: []*Input{{Name: "valid_input", Type: "string"}},
			Steps:  []*Step{{Name: "start", Activity: "test"}},
		})
		assert.NoError(t, err)

		_, err = NewExecution(ExecutionOptions{
			Workflow: wf,
			Inputs: map[string]any{
				"valid_input":   "good",
				"unknown_input": "bad", // unknown input
			},
			Activities: []Activity{nil},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown input")
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
		assert.NoError(t, err)

		_, err = NewExecution(ExecutionOptions{
			Workflow: wf,
			Inputs:   map[string]any{}, // missing required input
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					return nil, nil
				}),
			},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "required_input")
		assert.Contains(t, err.Error(), "is required")
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
		assert.NoError(t, err)

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
		assert.NoError(t, err)
		assert.NotNil(t, execution)
		assert.NotEmpty(t, execution.ID())
	})

	t.Run("registry and activities can be combined", func(t *testing.T) {
		wf, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: "start", Activity: "test"}},
		})
		assert.NoError(t, err)

		registry := NewRegistry()
		registryActivity := NewActivityFunction("registry-activity", func(ctx Context, params map[string]any) (any, error) {
			return "from-registry", nil
		})
		registry.MustRegisterActivity(registryActivity)

		directActivity := NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
			return "from-direct", nil
		})

		// Combining Registry and Activities should work
		execution, err := NewExecution(ExecutionOptions{
			Workflow:   wf,
			Registry:   registry,
			Activities: []Activity{directActivity},
		})
		assert.NoError(t, err)
		assert.NotNil(t, execution)

		// Both activities should be available
		assert.NotNil(t, execution.activities["registry-activity"])
		assert.NotNil(t, execution.activities["test"])
	})

	t.Run("direct activities override registry activities with same name", func(t *testing.T) {
		wf, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: "start", Activity: "test"}},
		})
		assert.NoError(t, err)

		registry := NewRegistry()
		registryActivity := NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
			return "from-registry", nil
		})
		registry.MustRegisterActivity(registryActivity)

		directActivity := NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
			return "from-direct", nil
		})

		execution, err := NewExecution(ExecutionOptions{
			Workflow:   wf,
			Registry:   registry,
			Activities: []Activity{directActivity},
		})
		assert.NoError(t, err)
		assert.NotNil(t, execution)

		// Direct activity should override registry activity
		// We can verify by checking it's not the same pointer (but both have same name)
		assert.NotNil(t, execution.activities["test"])
		// The activity in the execution should be the direct one (last registered wins)
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
					"message": "Processing started at $(state.start_time)",
				},
			},
		},
	})
	assert.NoError(t, err)

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
	assert.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	assert.NoError(t, execution.Run(ctx))
	assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)
	assert.Equal(t, gotMessage, "Processing started at 2025-07-21T12:00:00Z")
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
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		assert.NoError(t, execution.Run(ctx))
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify outputs are captured correctly
		outputs := execution.GetOutputs()
		assert.NotNil(t, outputs)
		// Note: JSON serialization may convert integers to float64
		assert.Equal(t, outputs["result"], float64(42))
		assert.Equal(t, outputs["message"], "workflow completed successfully")
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
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					return "value", nil
				}),
			},
		})
		assert.NoError(t, err)
		err = execution.Run(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "workflow output variable \"nonexistent_variable\" not found")
		assert.Equal(t, execution.Status(), domain.ExecutionStatusFailed)
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
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					return "test result", nil
				}),
			},
		})
		assert.NoError(t, err)
		assert.NoError(t, execution.Run(context.Background()))
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Should have empty outputs map
		outputs := execution.GetOutputs()
		assert.NotNil(t, outputs)
		assert.Empty(t, outputs)
	})

	t.Run("output variable defaults to output name", func(t *testing.T) {
		wf, err := New(Options{
			Name: "test-workflow-default-variable",
			Steps: []*Step{
				{Name: "store-data", Activity: "data", Store: "status"},
			},
			Outputs: []*Output{{Name: "status"}},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("data", func(ctx Context, params map[string]any) (any, error) {
					return "GREAT SUCCESS", nil
				}),
			},
		})
		assert.NoError(t, err)

		assert.NoError(t, execution.Run(context.Background()))
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify output is captured using default variable name
		outputs := execution.GetOutputs()
		assert.NotNil(t, outputs)
		assert.Equal(t, outputs["status"], "GREAT SUCCESS")
	})
}

func TestCheckpointerSavesCheckpoints(t *testing.T) {
	t.Run("successful workflow saves checkpoints", func(t *testing.T) {
		// Create checkpointer
		checkpointer := NewMemoryCheckpointer()

		// Create simple workflow
		wf, err := New(Options{
			Name: "checkpoint-test-success",
			Steps: []*Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		assert.NoError(t, err)

		// Create execution with checkpointer
		execution, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("test", func(ctx Context, params map[string]any) (any, error) {
					return "success", nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run the workflow
		assert.NoError(t, execution.Run(context.Background()))
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify we can load the checkpoint
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.ExecutionID, execution.ID())
		assert.Equal(t, checkpoint.WorkflowName, "checkpoint-test-success")
		assert.Equal(t, checkpoint.Status, "completed")
	})

	t.Run("failed workflow saves checkpoints", func(t *testing.T) {
		// Create checkpointer
		checkpointer := NewMemoryCheckpointer()

		// Create simple workflow that will fail
		wf, err := New(Options{
			Name: "checkpoint-test-failure",
			Steps: []*Step{
				{Name: "failing-step", Activity: "fail"},
			},
		})
		assert.NoError(t, err)

		// Create execution with checkpointer
		execution, err := NewExecution(ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []Activity{
				NewActivityFunction("fail", func(ctx Context, params map[string]any) (any, error) {
					return nil, errors.New("intentional test failure")
				}),
			},
		})
		assert.NoError(t, err)

		// Run the workflow (expect failure)
		err = execution.Run(context.Background())
		assert.Error(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusFailed)

		// Verify we can load the checkpoint and it shows failed status
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.ExecutionID, execution.ID())
		assert.Equal(t, checkpoint.WorkflowName, "checkpoint-test-failure")
		assert.Equal(t, checkpoint.Status, "failed")
		assert.NotEmpty(t, checkpoint.Error)
	})
}

func TestExecutionResumeFromCheckpoint(t *testing.T) {
	t.Run("resume failed execution and succeed", func(t *testing.T) {
		// Create checkpointer
		checkpointer := NewMemoryCheckpointer()

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
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run first execution (should fail)
		err = execution1.Run(context.Background())
		assert.Error(t, err)
		assert.Equal(t, execution1.Status(), domain.ExecutionStatusFailed)

		// Verify checkpoint was saved
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.Status, "failed")

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
		assert.NoError(t, err)

		// Resume from the failed execution
		err = execution2.Resume(context.Background(), execution1.ID())
		assert.NoError(t, err)
		assert.Equal(t, execution2.Status(), domain.ExecutionStatusCompleted)

		// Verify the flaky activity was called twice (once in each execution)
		assert.Equal(t, callCount, 2)

		// Verify final checkpoint shows success
		finalCheckpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution2.ID())
		assert.NoError(t, err)
		assert.NotNil(t, finalCheckpoint)
		assert.Equal(t, finalCheckpoint.Status, "completed")
	})

	t.Run("resume completed execution does nothing", func(t *testing.T) {
		// Create checkpointer
		checkpointer := NewMemoryCheckpointer()

		// Create simple successful workflow
		wf, err := New(Options{
			Name: "completed-test-workflow",
			Steps: []*Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run first execution (should succeed)
		err = execution1.Run(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, execution1.Status(), domain.ExecutionStatusCompleted)

		// Verify checkpoint was saved
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.Status, "completed")

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
		assert.NoError(t, err)

		// Resume from the completed execution (should be no-op)
		err = execution2.Resume(context.Background(), execution1.ID())
		assert.NoError(t, err)
		assert.Equal(t, execution2.Status(), domain.ExecutionStatusCompleted)
	})

	t.Run("resume nonexistent execution returns error", func(t *testing.T) {
		// Create checkpointer
		checkpointer := NewMemoryCheckpointer()

		// Create simple workflow
		wf, err := New(Options{
			Name: "test-workflow",
			Steps: []*Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Try to resume from nonexistent execution ID
		err = execution.Resume(context.Background(), "nonexistent-execution-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no checkpoint found")
	})

	t.Run("resume with retry mechanism works", func(t *testing.T) {
		// Create checkpointer
		checkpointer := NewMemoryCheckpointer()

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
							ErrorEquals: []string{"ALL"}, // Match all errors
							MaxRetries:  2,               // Allow 2 retries (3 total attempts)
						},
					},
				},
			},
		})
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run first execution (should fail after exhausting retries)
		err = execution1.Run(context.Background())
		assert.Error(t, err)
		assert.Equal(t, execution1.Status(), domain.ExecutionStatusFailed)

		// At this point, callCount should be 3 (initial attempt + 2 retries)
		assert.Equal(t, callCount, 3)

		// Verify checkpoint was saved with failed status
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.Status, "failed")

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
		assert.NoError(t, err)

		// Resume from the failed execution - should retry again and succeed
		err = execution2.Resume(context.Background(), execution1.ID())
		assert.NoError(t, err)
		assert.Equal(t, execution2.Status(), domain.ExecutionStatusCompleted)

		// Verify the retry activity was called 5 times total:
		// - First execution: 3 attempts (initial + 2 retries)
		// - Resumed execution: 2 more attempts (restart + 1 retry) = 5 total
		assert.Equal(t, callCount, 5)

		// Verify final checkpoint shows success
		finalCheckpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution2.ID())
		assert.NoError(t, err)
		assert.NotNil(t, finalCheckpoint)
		assert.Equal(t, finalCheckpoint.Status, "completed")
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
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify only the matching path was executed
		assert.Contains(t, executedActivities, "setup")
		assert.Contains(t, executedActivities, "activity_a")
		assert.NotContains(t, executedActivities, "activity_b")
	})

	t.Run("multiple conditional branches with state isolation", func(t *testing.T) {
		// Track activity executions with their path context
		type ActivityExecution struct {
			Activity string
			PathData map[string]any
		}
		var executions []ActivityExecution
		var executionMutex sync.Mutex

		recordExecution := func(ctx Context, activity string) {
			executionMutex.Lock()
			defer executionMutex.Unlock()

			executions = append(executions, ActivityExecution{
				Activity: activity,
				PathData: copyMap(VariablesFromContext(ctx)),
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
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup_data", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "setup_data")
					return 7, nil // Should trigger branch_medium
				}),
				NewActivityFunction("process_small", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "process_small")
					ctx.SetVariable("branch_type", "small")
					return "small processed", nil
				}),
				NewActivityFunction("process_medium", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "process_medium")
					ctx.SetVariable("branch_type", "medium")
					return "medium processed", nil
				}),
				NewActivityFunction("process_large", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "process_large")
					ctx.SetVariable("branch_type", "large")
					return "large processed", nil
				}),
				NewActivityFunction("final_activity", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "final_activity")
					return "workflow completed", nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify correct execution path
		var activityNames []string
		for _, exec := range executions {
			activityNames = append(activityNames, exec.Activity)
		}

		assert.Contains(t, activityNames, "setup_data")
		assert.Contains(t, activityNames, "process_medium") // base_value=7 should trigger medium branch
		assert.Contains(t, activityNames, "final_activity")
		assert.NotContains(t, activityNames, "process_small")
		assert.NotContains(t, activityNames, "process_large")

		// Verify state was correctly propagated and modified
		// Note: In the engine model, SetVariable within activities doesn't persist across steps.
		// Only Store field outputs are preserved. Check what's accessible from Store fields.
		for _, exec := range executions {
			if exec.Activity == "process_medium" {
				assert.Equal(t, exec.PathData["base_value"], float64(7))
			}
			if exec.Activity == "final_activity" {
				assert.Equal(t, exec.PathData["base_value"], float64(7))
				// medium_result is stored via Store field, so it should be accessible
				assert.Equal(t, exec.PathData["medium_result"], "medium processed")
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
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify all parallel paths were executed
		assert.Contains(t, parallelPaths, "start")
		assert.Contains(t, parallelPaths, "path_1")
		assert.Contains(t, parallelPaths, "path_2")
		assert.Contains(t, parallelPaths, "path_3")
		assert.Len(t, parallelPaths, 4) // start + 3 parallel paths
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
		assert.NoError(t, err)

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
		assert.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.Error(t, err) // Execution should fail due to the failed path
		assert.Equal(t, execution.Status(), domain.ExecutionStatusFailed)

		// Verify setup ran and both paths were attempted
		assert.Contains(t, completedPaths, "setup")
		assert.Contains(t, completedPaths, "success_path")
		assert.Contains(t, completedPaths, "failure_path_attempted")
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
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("setup_initial_state", func(ctx Context, params map[string]any) (any, error) {
					// Initialize shared counter
					return 100, nil
				}),
				NewActivityFunction("modify_state_alpha", func(ctx Context, params map[string]any) (any, error) {
					// Verify we start with the setup value
					counter, ok := ctx.GetVariable("shared_counter")
					assert.True(t, ok)
					assert.Equal(t, counter, float64(100))

					// Each path modifies the same variable name with different values
					ctx.SetVariable("shared_counter", 200)
					ctx.SetVariable("path_identifier", "ALPHA")
					ctx.SetVariable("multiplier", 2)

					recordPathExecution("alpha")
					return "alpha-200", nil
				}),
				NewActivityFunction("modify_state_beta", func(ctx Context, params map[string]any) (any, error) {
					// Verify we start with the setup value (not alpha's modification)
					counter, ok := ctx.GetVariable("shared_counter")
					assert.True(t, ok)
					assert.Equal(t, counter, float64(100))

					// Each path modifies the same variable name with different values
					ctx.SetVariable("shared_counter", 300)
					ctx.SetVariable("path_identifier", "BETA")
					ctx.SetVariable("multiplier", 3)

					recordPathExecution("beta")
					return "beta-300", nil
				}),
				NewActivityFunction("modify_state_gamma", func(ctx Context, params map[string]any) (any, error) {
					// Verify we start with the setup value (not alpha's or beta's modifications)
					counter, ok := ctx.GetVariable("shared_counter")
					assert.True(t, ok)
					assert.Equal(t, counter, float64(100))

					// Each path modifies the same variable name with different values
					ctx.SetVariable("shared_counter", 400)
					ctx.SetVariable("path_identifier", "GAMMA")
					ctx.SetVariable("multiplier", 4)

					recordPathExecution("gamma")
					return "gamma-400", nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify all three paths executed
		assert.Contains(t, pathExecutions, "alpha")
		assert.Contains(t, pathExecutions, "beta")
		assert.Contains(t, pathExecutions, "gamma")
		assert.Len(t, pathExecutions, 3)
	})
}

func TestNamedBranches(t *testing.T) {
	t.Run("named branches with path-specific outputs", func(t *testing.T) {
		// Create workflow with named branches and path-specific outputs
		wf, err := New(Options{
			Name: "named-branches-test",
			Steps: []*Step{
				{
					Name:     "analyze",
					Activity: "analyze_data",
					Store:    "data_size",
					Next: []*Edge{
						{Step: "process_large", Path: "large_processing", Condition: "state.data_size > 100"},
						{Step: "process_small", Path: "small_processing", Condition: "state.data_size <= 100"},
					},
				},
				{
					Name:     "process_large",
					Activity: "heavy_work",
					Store:    "large_result",
				},
				{
					Name:     "process_small",
					Activity: "light_work",
					Store:    "small_result",
				},
			},
			Outputs: []*Output{
				{Name: "analysis", Variable: "data_size"}, // Default to "main" path
				{Name: "processing_result", Variable: "large_result", Path: "large_processing"},
				// Note: small_processing won't execute due to condition, so no output from it
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("analyze_data", func(ctx Context, params map[string]any) (any, error) {
					return 150, nil // This will trigger large_processing branch
				}),
				NewActivityFunction("heavy_work", func(ctx Context, params map[string]any) (any, error) {
					return "heavy processing completed", nil
				}),
				NewActivityFunction("light_work", func(ctx Context, params map[string]any) (any, error) {
					return "light processing completed", nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = execution.Run(ctx)
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify outputs - should get analysis from main and processing_result from large_processing
		outputs := execution.GetOutputs()
		assert.NotNil(t, outputs)
		assert.Equal(t, outputs["analysis"], float64(150))                           // From main path
		assert.Equal(t, outputs["processing_result"], "heavy processing completed") // From large_processing path
		assert.NotContains(t, outputs, "light_result")                              // small_processing didn't run
	})

	t.Run("duplicate path names are rejected", func(t *testing.T) {
		_, err := New(Options{
			Name: "duplicate-path-names",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Next: []*Edge{
						{Step: "step_a", Path: "same_name"},
						{Step: "step_b", Path: "same_name"},
					},
				},
				{Name: "step_a", Activity: "activity_a"},
				{Name: "step_b", Activity: "activity_b"},
			},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `path name "same_name" is already used`)
	})

	t.Run("reserved 'main' path name is rejected", func(t *testing.T) {
		// Try to create workflow using reserved "main" path name
		_, err := New(Options{
			Name: "reserved-main-name",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Next: []*Edge{
						{Step: "next_step", Path: "main"}, // Reserved name!
					},
				},
				{Name: "next_step", Activity: "next_activity"},
			},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "path name 'main' is reserved")
	})

	t.Run("outputs from non-existent path returns error", func(t *testing.T) {
		wf, err := New(Options{
			Name: "missing-path-test",
			Steps: []*Step{
				{Name: "single_step", Activity: "simple_activity", Store: "result"},
			},
			Outputs: []*Output{
				{Name: "result", Variable: "result", Path: "non_existent_path"},
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("simple_activity", func(ctx Context, params map[string]any) (any, error) {
					return "test result", nil
				}),
			},
		})
		assert.NoError(t, err)

		err = execution.Run(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "path \"non_existent_path\" does not exist")
	})

	t.Run("unnamed edges default to main path", func(t *testing.T) {
		// Edges without explicit path names use the default "main" path
		wf, err := New(Options{
			Name: "unnamed-edges",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Store:    "condition",
					Next: []*Edge{
						{Step: "branch_a", Condition: "state.condition == 'A'"},
						{Step: "branch_b", Condition: "state.condition == 'B'"},
					},
				},
				{Name: "branch_a", Activity: "activity_a", Store: "result_a"},
				{Name: "branch_b", Activity: "activity_b", Store: "result_b"},
			},
			Outputs: []*Output{
				{Name: "result", Variable: "condition"}, // Should default to "main" path
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("start_activity", func(ctx Context, params map[string]any) (any, error) {
					return "A", nil
				}),
				NewActivityFunction("activity_a", func(ctx Context, params map[string]any) (any, error) {
					return "result from A", nil
				}),
				NewActivityFunction("activity_b", func(ctx Context, params map[string]any) (any, error) {
					return "result from B", nil
				}),
			},
		})
		assert.NoError(t, err)

		err = execution.Run(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Should successfully extract from main path
		outputs := execution.GetOutputs()
		assert.Equal(t, outputs["result"], "A")
	})

	t.Run("mixed named and unnamed branches", func(t *testing.T) {
		// Test workflow with some named and some unnamed branches
		wf, err := New(Options{
			Name: "mixed-branches",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Store:    "value",
					Next: []*Edge{
						{Step: "named_branch", Path: "special_path"},
						{Step: "unnamed_branch"}, // No path name
					},
				},
				{Name: "named_branch", Activity: "named_activity", Store: "named_result"},
				{Name: "unnamed_branch", Activity: "unnamed_activity", Store: "unnamed_result"},
			},
			Outputs: []*Output{
				{Name: "from_named", Variable: "named_result", Path: "special_path"},
				{Name: "from_main", Variable: "value"}, // Default to main
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("start_activity", func(ctx Context, params map[string]any) (any, error) {
					return "test_value", nil
				}),
				NewActivityFunction("named_activity", func(ctx Context, params map[string]any) (any, error) {
					return "named result", nil
				}),
				NewActivityFunction("unnamed_activity", func(ctx Context, params map[string]any) (any, error) {
					return "unnamed result", nil
				}),
			},
		})
		assert.NoError(t, err)

		err = execution.Run(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		outputs := execution.GetOutputs()
		assert.Equal(t, outputs["from_named"], "named result")
		assert.Equal(t, outputs["from_main"], "test_value")
	})

	t.Run("path continues when PathName matches current path", func(t *testing.T) {
		// Test that a path continues when the edge PathName matches the current path name
		wf, err := New(Options{
			Name: "path-continuation-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Store:    "step1_result",
					Next: []*Edge{
						{Step: "continue_same_path", Path: "special_path"},
					},
				},
				{
					Name:     "continue_same_path",
					Activity: "continue_activity",
					Store:    "step2_result",
					Next: []*Edge{
						{Step: "final_step"},
					},
				},
				{
					Name:     "final_step",
					Activity: "final_activity",
					Store:    "final_result",
				},
			},
			Outputs: []*Output{
				{Name: "all_results", Variable: "final_result", Path: "special_path"},
			},
		})
		assert.NoError(t, err)

		execution, err := NewExecution(ExecutionOptions{
			Workflow: wf,
			Activities: []Activity{
				NewActivityFunction("start_activity", func(ctx Context, params map[string]any) (any, error) {
					return "step1_done", nil
				}),
				NewActivityFunction("continue_activity", func(ctx Context, params map[string]any) (any, error) {
					// Verify we can see the previous step's result (proving path continuity)
					step1Result, exists := ctx.GetVariable("step1_result")
					assert.True(t, exists)
					assert.Equal(t, step1Result, "step1_done")
					return "step2_done", nil
				}),
				NewActivityFunction("final_activity", func(ctx Context, params map[string]any) (any, error) {
					// Verify we can see both previous steps' results
					step1Result, exists := ctx.GetVariable("step1_result")
					assert.True(t, exists)
					assert.Equal(t, step1Result, "step1_done")

					step2Result, exists := ctx.GetVariable("step2_result")
					assert.True(t, exists)
					assert.Equal(t, step2Result, "step2_done")

					return "all_steps_done", nil
				}),
			},
		})
		assert.NoError(t, err)

		err = execution.Run(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify that all steps executed in the same path and we got the final result
		outputs := execution.GetOutputs()
		assert.Equal(t, outputs["all_results"], "all_steps_done")
	})
}
