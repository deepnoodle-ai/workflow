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

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestNewExecutionID(t *testing.T) {
	t.Run("format and uniqueness", func(t *testing.T) {
		seen := make(map[string]struct{}, 1024)
		for i := 0; i < 1024; i++ {
			id := NewExecutionID()
			require.True(t, len(id) > len("exec_"), "id should not be empty")
			require.Equal(t, "exec_", id[:5])
			body := id[5:]
			require.Equal(t, 26, len(body))
			for _, r := range body {
				ok := (r >= 'a' && r <= 'z') || (r >= '2' && r <= '7')
				require.True(t, ok, "unexpected char %q in %s", r, id)
			}
			_, dup := seen[id]
			require.False(t, dup, "duplicate execution id generated: %s", id)
			seen[id] = struct{}{}
		}
	})
}

func TestNewExecutionValidation(t *testing.T) {
	t.Run("missing workflow returns error", func(t *testing.T) {
		reg := NewActivityRegistry()
		reg.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					return nil, nil
				}))
		_, err := NewExecution(nil, reg,
			WithScriptCompiler(newTestCompiler()),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "workflow is required")
	})

	t.Run("nil activity registry returns error", func(t *testing.T) {
		wf, err := New(Options{
			Name:  "test-workflow",
			Steps: []*Step{{Name: "start", Activity: "test"}},
		})
		require.NoError(t, err)

		_, err = NewExecution(wf, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "activity registry is required")
	})

	t.Run("unknown input is rejected", func(t *testing.T) {
		wf, err := New(Options{
			Name:   "test-workflow",
			Inputs: []*Input{{Name: "valid_input", Type: "string"}},
			Steps:  []*Step{{Name: "start", Activity: "test"}},
		})
		require.NoError(t, err)

		reg3 := NewActivityRegistry()
		_, err = NewExecution(wf, reg3,
			WithScriptCompiler(newTestCompiler()),
			WithInputs(map[string]any{
				"valid_input":   "good",
				"unknown_input": "bad", // unknown input
			}),
		)
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

		reg4 := NewActivityRegistry()
		_, err = NewExecution(wf, reg4,
			WithScriptCompiler(newTestCompiler()),
			WithInputs(map[string]any{}),
		)
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

		reg5 := NewActivityRegistry()
		reg5.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					return nil, nil
				}))
		execution, err := NewExecution(wf, reg5,
			WithScriptCompiler(newTestCompiler()),
			WithInputs(map[string]any{
				"optional_input": "provided_value",
			}),
		)
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

	reg := NewActivityRegistry()
	reg.MustRegister(ActivityFunc("time.now", func(ctx Context, params map[string]any) (any, error) {
				return "2025-07-21T12:00:00Z", nil
			}))
	reg.MustRegister(ActivityFunc("print", func(ctx Context, params map[string]any) (any, error) {
				message, ok := params["message"]
				if !ok {
					return nil, errors.New("print activity requires 'message' parameter")
				}
				gotMessage = message.(string)
				return nil, nil
			}))
	execution, err := NewExecution(wf, reg,
		WithScriptCompiler(newTestCompiler()),
		WithInputs(map[string]any{}),
		WithLogger(logger),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = execution.Execute(ctx)

	require.NoError(t, err)
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

		reg := NewActivityRegistry()
		reg.MustRegister(ActivityFunc("math", func(ctx Context, params map[string]any) (any, error) {
					return 42, nil
				}))
		reg.MustRegister(ActivityFunc("message", func(ctx Context, params map[string]any) (any, error) {
					return "workflow completed successfully", nil
				}))
		execution, err := NewExecution(wf, reg,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run the workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = execution.Execute(ctx)

		require.NoError(t, err)
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

		reg2 := NewActivityRegistry()
		reg2.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					return "value", nil
				}))
		execution, err := NewExecution(wf, reg2,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)
		result, err := execution.Execute(context.Background())
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, ExecutionStatusFailed, result.Status)
		require.NotNil(t, result.Error)
		require.Contains(t, result.Error.Error(), "workflow output variable \"nonexistent_variable\" not found")
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

		reg3 := NewActivityRegistry()
		reg3.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					return "test result", nil
				}))
		execution, err := NewExecution(wf, reg3,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)
		_, err = execution.Execute(context.Background())
		require.NoError(t, err)
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

		reg4 := NewActivityRegistry()
		reg4.MustRegister(ActivityFunc("data", func(ctx Context, params map[string]any) (any, error) {
					return "GREAT SUCCESS", nil
				}))
		execution, err := NewExecution(wf, reg4,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		_, err = execution.Execute(context.Background())

		require.NoError(t, err)
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
		reg := NewActivityRegistry()
		reg.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					return "success", nil
				}))
		execution, err := NewExecution(wf, reg,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Run the workflow
		_, err = execution.Execute(context.Background())
		require.NoError(t, err)
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
		reg2 := NewActivityRegistry()
		reg2.MustRegister(ActivityFunc("fail", func(ctx Context, params map[string]any) (any, error) {
					return nil, errors.New("intentional test failure")
				}))
		execution, err := NewExecution(wf, reg2,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Run the workflow (expect failure)
		result, err := execution.Execute(context.Background())
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, ExecutionStatusFailed, result.Status)
		require.NotNil(t, result.Error)
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
		reg := NewActivityRegistry()
		reg.MustRegister(ActivityFunc("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}))
		reg.MustRegister(ActivityFunc("flaky", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					if callCount == 1 {
						return nil, errors.New("flaky failure on first attempt")
					}
					return "success on retry", nil
				}))
		execution1, err := NewExecution(wf, reg,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Run first execution (should fail)
		result1, err := execution1.Execute(context.Background())
		require.NoError(t, err)
		require.NotNil(t, result1)
		require.Equal(t, ExecutionStatusFailed, result1.Status)
		require.NotNil(t, result1.Error)
		require.Equal(t, ExecutionStatusFailed, execution1.Status())

		// Verify checkpoint was saved
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, "failed", checkpoint.Status)

		// Create second execution to resume from the first one's checkpoint
		reg2 := NewActivityRegistry()
		reg2.MustRegister(ActivityFunc("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}))
		reg2.MustRegister(ActivityFunc("flaky", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					if callCount == 1 {
						return nil, errors.New("flaky failure on first attempt")
					}
					return "success on retry", nil
				}))
		execution2, err := NewExecution(wf, reg2,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Resume from the failed execution
		_, err = execution2.Execute(context.Background(), ResumeFrom(execution1.ID()))
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
		reg3 := NewActivityRegistry()
		reg3.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					return "success", nil
				}))
		execution1, err := NewExecution(wf, reg3,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Run first execution (should succeed)
		_, err = execution1.Execute(context.Background())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution1.Status())

		// Verify checkpoint was saved
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, "completed", checkpoint.Status)

		// Create second execution to resume from completed one
		reg4 := NewActivityRegistry()
		reg4.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					t.Fatal("test activity should not be called when resuming completed execution")
					return nil, nil
				}))
		execution2, err := NewExecution(wf, reg4,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Resume from the completed execution (should be no-op)
		_, err = execution2.Execute(context.Background(), ResumeFrom(execution1.ID()))
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution2.Status())
	})

	t.Run("resume nonexistent execution falls back to fresh run", func(t *testing.T) {
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
		callCount := 0
		reg5 := NewActivityRegistry()
		reg5.MustRegister(ActivityFunc("test", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					return "success", nil
				}))
		execution, err := NewExecution(wf, reg5,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Resume from nonexistent execution ID should silently fall back
		// to a fresh run under the new Execute(ResumeFrom(...)) contract.
		result, err := execution.Execute(context.Background(), ResumeFrom("nonexistent-execution-id"))
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, ExecutionStatusCompleted, result.Status)
		require.Equal(t, 1, callCount, "fresh run should have executed the activity once")
		require.Equal(t, ExecutionStatusCompleted, execution.Status())
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
		reg6 := NewActivityRegistry()
		reg6.MustRegister(ActivityFunc("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}))
		reg6.MustRegister(ActivityFunc("retry-activity", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					// Fail for the first 4 attempts (initial + 2 retries in first execution + 1 attempt in resumed execution)
					if callCount <= 4 {
						return nil, errors.New("activity failure - attempt " + fmt.Sprintf("%d", callCount))
					}
					return "success after retries", nil
				}))
		execution1, err := NewExecution(wf, reg6,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Run first execution (should fail after exhausting retries)
		result1, err := execution1.Execute(context.Background())
		require.NoError(t, err)
		require.NotNil(t, result1)
		require.Equal(t, ExecutionStatusFailed, result1.Status)
		require.NotNil(t, result1.Error)
		require.Equal(t, ExecutionStatusFailed, execution1.Status())

		// At this point, callCount should be 3 (initial attempt + 2 retries)
		require.Equal(t, 3, callCount)

		// Verify checkpoint was saved with failed status
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution1.ID())
		require.NoError(t, err)
		require.NotNil(t, checkpoint)
		require.Equal(t, "failed", checkpoint.Status)

		// Create second execution to resume from the first one's checkpoint
		reg7 := NewActivityRegistry()
		reg7.MustRegister(ActivityFunc("setup", func(ctx Context, params map[string]any) (any, error) {
					return "setup complete", nil
				}))
		reg7.MustRegister(ActivityFunc("retry-activity", func(ctx Context, params map[string]any) (any, error) {
					callCount++
					// Fail for the first 4 attempts, succeed on the 5th
					if callCount <= 4 {
						return nil, errors.New("activity failure - attempt " + fmt.Sprintf("%d", callCount))
					}
					return "success after retries", nil
				}))
		execution2, err := NewExecution(wf, reg7,
			WithScriptCompiler(newTestCompiler()),
			WithCheckpointer(checkpointer),
		)
		require.NoError(t, err)

		// Resume from the failed execution - should retry again and succeed
		_, err = execution2.Execute(context.Background(), ResumeFrom(execution1.ID()))
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

func TestBranchBranching(t *testing.T) {
	t.Run("simple conditional branching creates two branches", func(t *testing.T) {
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

		reg := NewActivityRegistry()
		reg.MustRegister(ActivityFunc("setup", func(ctx Context, params map[string]any) (any, error) {
					addExecutedActivity("setup")
					// Set up state that will cause both branches to be taken
					return "A", nil // This will only match path_a condition
				}))
		reg.MustRegister(ActivityFunc("activity_a", func(ctx Context, params map[string]any) (any, error) {
					addExecutedActivity("activity_a")
					return "result from branch A", nil
				}))
		reg.MustRegister(ActivityFunc("activity_b", func(ctx Context, params map[string]any) (any, error) {
					addExecutedActivity("activity_b")
					return "result from branch B", nil
				}))
		execution, err := NewExecution(wf, reg,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = execution.Execute(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify only the matching branch was executed
		require.Contains(t, executedActivities, "setup")
		require.Contains(t, executedActivities, "activity_a")
		require.NotContains(t, executedActivities, "activity_b")
	})

	t.Run("multiple conditional branches with state isolation", func(t *testing.T) {
		// Track activity executions with their branch context
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
		require.NoError(t, err)

		reg2 := NewActivityRegistry()
		reg2.MustRegister(ActivityFunc("setup_data", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "setup_data")
					return 7, nil // Should trigger branch_medium
				}))
		reg2.MustRegister(ActivityFunc("process_small", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "process_small")
					ctx.SetVariable("branch_type", "small")
					return "small processed", nil
				}))
		reg2.MustRegister(ActivityFunc("process_medium", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "process_medium")
					ctx.SetVariable("branch_type", "medium")
					return "medium processed", nil
				}))
		reg2.MustRegister(ActivityFunc("process_large", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "process_large")
					ctx.SetVariable("branch_type", "large")
					return "large processed", nil
				}))
		reg2.MustRegister(ActivityFunc("final_activity", func(ctx Context, params map[string]any) (any, error) {
					recordExecution(ctx, "final_activity")
					return "workflow completed", nil
				}))
		execution, err := NewExecution(wf, reg2,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = execution.Execute(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify correct execution branch
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
		var parallelBranches []string
		var pathMutex sync.Mutex

		recordBranchExecution := func(branchName string) {
			pathMutex.Lock()
			defer pathMutex.Unlock()
			parallelBranches = append(parallelBranches, branchName)
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

		reg3 := NewActivityRegistry()
		reg3.MustRegister(ActivityFunc("start_activity", func(ctx Context, params map[string]any) (any, error) {
					recordBranchExecution("start")
					return "initialized", nil
				}))
		reg3.MustRegister(ActivityFunc("work_1", func(ctx Context, params map[string]any) (any, error) {
					recordBranchExecution("path_1")
					// Simulate some work
					time.Sleep(10 * time.Millisecond)
					return "work 1 completed", nil
				}))
		reg3.MustRegister(ActivityFunc("work_2", func(ctx Context, params map[string]any) (any, error) {
					recordBranchExecution("path_2")
					// Simulate some work
					time.Sleep(15 * time.Millisecond)
					return "work 2 completed", nil
				}))
		reg3.MustRegister(ActivityFunc("work_3", func(ctx Context, params map[string]any) (any, error) {
					recordBranchExecution("path_3")
					// Simulate some work
					time.Sleep(5 * time.Millisecond)
					return "work 3 completed", nil
				}))
		execution, err := NewExecution(wf, reg3,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = execution.Execute(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify all parallel branches were executed
		require.Contains(t, parallelBranches, "start")
		require.Contains(t, parallelBranches, "path_1")
		require.Contains(t, parallelBranches, "path_2")
		require.Contains(t, parallelBranches, "path_3")
		require.Len(t, parallelBranches, 4) // start + 3 parallel branches
	})

	t.Run("branching with failure in one branch does not affect execution completion", func(t *testing.T) {
		var completedBranches []string
		var pathMutex sync.Mutex

		recordCompletion := func(branchName string) {
			pathMutex.Lock()
			defer pathMutex.Unlock()
			completedBranches = append(completedBranches, branchName)
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

		reg4 := NewActivityRegistry()
		reg4.MustRegister(ActivityFunc("setup_activity", func(ctx Context, params map[string]any) (any, error) {
					recordCompletion("setup")
					return "setup complete", nil
				}))
		reg4.MustRegister(ActivityFunc("success_activity", func(ctx Context, params map[string]any) (any, error) {
					recordCompletion("success_path")
					return "success result", nil
				}))
		reg4.MustRegister(ActivityFunc("failure_activity", func(ctx Context, params map[string]any) (any, error) {
					recordCompletion("failure_path_attempted")
					return nil, errors.New("intentional failure in one branch")
				}))
		execution, err := NewExecution(wf, reg4,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		result, err := execution.Execute(ctx)
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, ExecutionStatusFailed, result.Status) // Execution should fail due to the failed branch
		require.NotNil(t, result.Error)
		require.Equal(t, ExecutionStatusFailed, execution.Status())

		// Verify setup ran and both branches were attempted
		require.Contains(t, completedBranches, "setup")
		require.Contains(t, completedBranches, "success_path")
		require.Contains(t, completedBranches, "failure_path_attempted")
	})

	t.Run("parallel branches have completely isolated state variables", func(t *testing.T) {
		// Track state access and modifications from each branch to verify isolation
		var pathExecutions []string
		var pathMutex sync.Mutex

		recordBranchExecution := func(branchName string) {
			pathMutex.Lock()
			defer pathMutex.Unlock()
			pathExecutions = append(pathExecutions, branchName)
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

		reg5 := NewActivityRegistry()
		reg5.MustRegister(ActivityFunc("setup_initial_state", func(ctx Context, params map[string]any) (any, error) {
					// Initialize shared counter
					return 100, nil
				}))
		reg5.MustRegister(ActivityFunc("modify_state_alpha", func(ctx Context, params map[string]any) (any, error) {
					// Verify we start with the setup value
					counter, ok := ctx.GetVariable("shared_counter")
					require.True(t, ok)
					require.Equal(t, 100, counter)

					// Each branch modifies the same variable name with different values
					ctx.SetVariable("shared_counter", 200)
					ctx.SetVariable("path_identifier", "ALPHA")
					ctx.SetVariable("multiplier", 2)

					recordBranchExecution("alpha")
					return "alpha-200", nil
				}))
		reg5.MustRegister(ActivityFunc("modify_state_beta", func(ctx Context, params map[string]any) (any, error) {
					// Verify we start with the setup value (not alpha's modification)
					counter, ok := ctx.GetVariable("shared_counter")
					require.True(t, ok)
					require.Equal(t, 100, counter)

					// Each branch modifies the same variable name with different values
					ctx.SetVariable("shared_counter", 300)
					ctx.SetVariable("path_identifier", "BETA")
					ctx.SetVariable("multiplier", 3)

					recordBranchExecution("beta")
					return "beta-300", nil
				}))
		reg5.MustRegister(ActivityFunc("modify_state_gamma", func(ctx Context, params map[string]any) (any, error) {
					// Verify we start with the setup value (not alpha's or beta's modifications)
					counter, ok := ctx.GetVariable("shared_counter")
					require.True(t, ok)
					require.Equal(t, 100, counter)

					// Each branch modifies the same variable name with different values
					ctx.SetVariable("shared_counter", 400)
					ctx.SetVariable("path_identifier", "GAMMA")
					ctx.SetVariable("multiplier", 4)

					recordBranchExecution("gamma")
					return "gamma-400", nil
				}))
		execution, err := NewExecution(wf, reg5,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = execution.Execute(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify all three branches executed
		require.Contains(t, pathExecutions, "alpha")
		require.Contains(t, pathExecutions, "beta")
		require.Contains(t, pathExecutions, "gamma")
		require.Len(t, pathExecutions, 3)
	})
}

func TestNamedBranches(t *testing.T) {
	t.Run("named branches with branch-specific outputs", func(t *testing.T) {
		// Create workflow with named branches and branch-specific outputs
		wf, err := New(Options{
			Name: "named-branches-test",
			Steps: []*Step{
				{
					Name:     "analyze",
					Activity: "analyze_data",
					Store:    "data_size",
					Next: []*Edge{
						{Step: "process_large", BranchName: "large_processing", Condition: "state.data_size > 100"},
						{Step: "process_small", BranchName: "small_processing", Condition: "state.data_size <= 100"},
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
				{Name: "analysis", Variable: "data_size"}, // Default to "main" branch
				{Name: "processing_result", Variable: "large_result", Branch: "large_processing"},
				// Note: small_processing won't execute due to condition, so no output from it
			},
		})
		require.NoError(t, err)

		reg := NewActivityRegistry()
		reg.MustRegister(ActivityFunc("analyze_data", func(ctx Context, params map[string]any) (any, error) {
					return 150, nil // This will trigger large_processing branch
				}))
		reg.MustRegister(ActivityFunc("heavy_work", func(ctx Context, params map[string]any) (any, error) {
					return "heavy processing completed", nil
				}))
		reg.MustRegister(ActivityFunc("light_work", func(ctx Context, params map[string]any) (any, error) {
					return "light processing completed", nil
				}))
		execution, err := NewExecution(wf, reg,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		// Run workflow
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = execution.Execute(ctx)
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify outputs - should get analysis from main and processing_result from large_processing
		outputs := execution.GetOutputs()
		require.NotNil(t, outputs)
		require.Equal(t, 150, outputs["analysis"])                                   // From main branch
		require.Equal(t, "heavy processing completed", outputs["processing_result"]) // From large_processing branch
		require.NotContains(t, outputs, "light_result")                              // small_processing didn't run
	})

	t.Run("duplicate branch names are rejected", func(t *testing.T) {
		_, err := New(Options{
			Name: "duplicate-branch-names",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Next: []*Edge{
						{Step: "step_a", BranchName: "same_name"},
						{Step: "step_b", BranchName: "same_name"},
					},
				},
				{Name: "step_a", Activity: "activity_a"},
				{Name: "step_b", Activity: "activity_b"},
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), `duplicate branch name "same_name"`)
	})

	t.Run("reserved 'main' branch name is rejected", func(t *testing.T) {
		// Try to create workflow using reserved "main" branch name
		_, err := New(Options{
			Name: "reserved-main-name",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Next: []*Edge{
						{Step: "next_step", BranchName: "main"}, // Reserved name!
					},
				},
				{Name: "next_step", Activity: "next_activity"},
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "branch name 'main' is reserved")
	})

	t.Run("outputs from non-existent branch returns error", func(t *testing.T) {
		wf, err := New(Options{
			Name: "missing-branch-test",
			Steps: []*Step{
				{Name: "single_step", Activity: "simple_activity", Store: "result"},
			},
			Outputs: []*Output{
				{Name: "result", Variable: "result", Branch: "non_existent_path"},
			},
		})
		require.NoError(t, err)

		reg2 := NewActivityRegistry()
		reg2.MustRegister(ActivityFunc("simple_activity", func(ctx Context, params map[string]any) (any, error) {
					return "test result", nil
				}))
		execution, err := NewExecution(wf, reg2,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		result, err := execution.Execute(context.Background())
		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, ExecutionStatusFailed, result.Status)
		require.NotNil(t, result.Error)
		require.Contains(t, result.Error.Error(), "output branch \"non_existent_path\" not found")
	})

	t.Run("backwards compatibility with unnamed edges", func(t *testing.T) {
		// Test that existing workflows without branch names continue to work
		wf, err := New(Options{
			Name: "backwards-compatibility",
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
				{Name: "result", Variable: "condition"}, // Should default to "main" branch
			},
		})
		require.NoError(t, err)

		reg3 := NewActivityRegistry()
		reg3.MustRegister(ActivityFunc("start_activity", func(ctx Context, params map[string]any) (any, error) {
					return "A", nil
				}))
		reg3.MustRegister(ActivityFunc("activity_a", func(ctx Context, params map[string]any) (any, error) {
					return "result from A", nil
				}))
		reg3.MustRegister(ActivityFunc("activity_b", func(ctx Context, params map[string]any) (any, error) {
					return "result from B", nil
				}))
		execution, err := NewExecution(wf, reg3,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		_, err = execution.Execute(context.Background())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Should successfully extract from main branch
		outputs := execution.GetOutputs()
		require.Equal(t, "A", outputs["result"])
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
						{Step: "named_branch", BranchName: "special_path"},
						{Step: "unnamed_branch"}, // No branch name
					},
				},
				{Name: "named_branch", Activity: "named_activity", Store: "named_result"},
				{Name: "unnamed_branch", Activity: "unnamed_activity", Store: "unnamed_result"},
			},
			Outputs: []*Output{
				{Name: "from_named", Variable: "named_result", Branch: "special_path"},
				{Name: "from_main", Variable: "value"}, // Default to main
			},
		})
		require.NoError(t, err)

		reg4 := NewActivityRegistry()
		reg4.MustRegister(ActivityFunc("start_activity", func(ctx Context, params map[string]any) (any, error) {
					return "test_value", nil
				}))
		reg4.MustRegister(ActivityFunc("named_activity", func(ctx Context, params map[string]any) (any, error) {
					return "named result", nil
				}))
		reg4.MustRegister(ActivityFunc("unnamed_activity", func(ctx Context, params map[string]any) (any, error) {
					return "unnamed result", nil
				}))
		execution, err := NewExecution(wf, reg4,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		_, err = execution.Execute(context.Background())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		outputs := execution.GetOutputs()
		require.Equal(t, "named result", outputs["from_named"])
		require.Equal(t, "test_value", outputs["from_main"])
	})

	t.Run("branch continues when PathName matches current branch", func(t *testing.T) {
		// Test that a branch continues when the edge PathName matches the current branch name
		wf, err := New(Options{
			Name: "branch-continuation-test",
			Steps: []*Step{
				{
					Name:     "start",
					Activity: "start_activity",
					Store:    "step1_result",
					Next: []*Edge{
						{Step: "continue_same_path", BranchName: "special_path"},
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
				{Name: "all_results", Variable: "final_result", Branch: "special_path"},
			},
		})
		require.NoError(t, err)

		reg5 := NewActivityRegistry()
		reg5.MustRegister(ActivityFunc("start_activity", func(ctx Context, params map[string]any) (any, error) {
					return "step1_done", nil
				}))
		reg5.MustRegister(ActivityFunc("continue_activity", func(ctx Context, params map[string]any) (any, error) {
					// Verify we can see the previous step's result (proving branch continuity)
					step1Result, exists := ctx.GetVariable("step1_result")
					require.True(t, exists)
					require.Equal(t, "step1_done", step1Result)
					return "step2_done", nil
				}))
		reg5.MustRegister(ActivityFunc("final_activity", func(ctx Context, params map[string]any) (any, error) {
					// Verify we can see both previous steps' results
					step1Result, exists := ctx.GetVariable("step1_result")
					require.True(t, exists)
					require.Equal(t, "step1_done", step1Result)

					step2Result, exists := ctx.GetVariable("step2_result")
					require.True(t, exists)
					require.Equal(t, "step2_done", step2Result)

					return "all_steps_done", nil
				}))
		execution, err := NewExecution(wf, reg5,
			WithScriptCompiler(newTestCompiler()),
		)
		require.NoError(t, err)

		_, err = execution.Execute(context.Background())
		require.NoError(t, err)
		require.Equal(t, ExecutionStatusCompleted, execution.Status())

		// Verify that all steps executed in the same branch and we got the final result
		outputs := execution.GetOutputs()
		require.Equal(t, "all_steps_done", outputs["all_results"])

		// Verify that only one branch was created (the "special_path")
		branchStates := execution.state.GetBranchStates()
		require.Len(t, branchStates, 2) // main (completed) + special_path (completed)

		// Verify both branches completed successfully
		for branchID, branchState := range branchStates {
			require.Equal(t, ExecutionStatusCompleted, branchState.Status, "Path %s should be completed", branchID)
		}
	})
}
