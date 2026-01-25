package stores_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/stores"
)

func TestFileCheckpointerSavesCheckpoints(t *testing.T) {
	t.Run("successful workflow saves checkpoints", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := stores.NewFileCheckpointer(tempDir)
		assert.NoError(t, err)

		// Create simple workflow
		wf, err := workflow.New(workflow.Options{
			Name: "checkpoint-test-success",
			Steps: []*workflow.Step{
				{Name: "simple-step", Activity: "test"},
			},
		})
		assert.NoError(t, err)

		// Create execution with FileCheckpointer
		execution, err := workflow.NewExecution(workflow.ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []workflow.Activity{
				workflow.NewActivityFunction("test", func(ctx workflow.Context, params map[string]any) (any, error) {
					return "success", nil
				}),
			},
		})
		assert.NoError(t, err)

		// Run the workflow
		assert.NoError(t, execution.Run(context.Background()))
		assert.Equal(t, execution.Status(), domain.ExecutionStatusCompleted)

		// Verify checkpoint files were created
		executionDir := tempDir + "/" + execution.ID()

		// Check that execution directory exists
		_, err = os.Stat(executionDir)
		assert.NoError(t, err, "execution directory should exist")

		// Check that latest.json exists
		latestFile := executionDir + "/latest.json"
		_, err = os.Stat(latestFile)
		assert.NoError(t, err, "latest.json should exist")

		// Verify we can load the checkpoint
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.ExecutionID, execution.ID())
		assert.Equal(t, checkpoint.WorkflowName, "checkpoint-test-success")
		assert.Equal(t, checkpoint.Status, "completed")
	})

	t.Run("failed workflow saves checkpoints", func(t *testing.T) {
		// Create temp directory for checkpoints
		tempDir := t.TempDir()

		// Create FileCheckpointer
		checkpointer, err := stores.NewFileCheckpointer(tempDir)
		assert.NoError(t, err)

		// Create simple workflow that will fail
		wf, err := workflow.New(workflow.Options{
			Name: "checkpoint-test-failure",
			Steps: []*workflow.Step{
				{Name: "failing-step", Activity: "fail"},
			},
		})
		assert.NoError(t, err)

		// Create execution with FileCheckpointer
		execution, err := workflow.NewExecution(workflow.ExecutionOptions{
			Workflow:     wf,
			Checkpointer: checkpointer,
			Activities: []workflow.Activity{
				workflow.NewActivityFunction("fail", func(ctx workflow.Context, params map[string]any) (any, error) {
					return nil, errors.New("intentional test failure")
				}),
			},
		})
		assert.NoError(t, err)

		// Run the workflow (expect failure)
		err = execution.Run(context.Background())
		assert.Error(t, err)
		assert.Equal(t, execution.Status(), domain.ExecutionStatusFailed)

		// Verify checkpoint files were created even for failed execution
		executionDir := tempDir + "/" + execution.ID()

		// Check that execution directory exists
		_, err = os.Stat(executionDir)
		assert.NoError(t, err, "execution directory should exist")

		// Check that latest.json exists
		latestFile := executionDir + "/latest.json"
		_, err = os.Stat(latestFile)
		assert.NoError(t, err, "latest.json should exist")

		// Verify we can load the checkpoint
		checkpoint, err := checkpointer.LoadCheckpoint(context.Background(), execution.ID())
		assert.NoError(t, err)
		assert.NotNil(t, checkpoint)
		assert.Equal(t, checkpoint.Status, "failed")
	})
}

func TestFileActivityLogger(t *testing.T) {
	t.Run("logs activities to file", func(t *testing.T) {
		tempDir := t.TempDir()
		logger := stores.NewFileActivityLogger(tempDir)

		// Log an activity
		err := logger.LogActivity(context.Background(), &workflow.ActivityLogEntry{
			ExecutionID: "test-exec",
			Activity:    "test-activity",
		})
		assert.NoError(t, err)

		// Verify file was created
		logFile := tempDir + "/test-exec.jsonl"
		_, err = os.Stat(logFile)
		assert.NoError(t, err, "log file should exist")

		// Verify we can read the logs
		entries, err := logger.GetActivityHistory(context.Background(), "test-exec")
		assert.NoError(t, err)
		assert.Len(t, entries, 1)
		assert.Equal(t, entries[0].Activity, "test-activity")
	})
}
