package workflowtest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/workflowtest"
)

func newTestWorkflow(t *testing.T) *workflow.Workflow {
	t.Helper()
	wf, err := workflow.New(workflow.Options{
		Name: "test-wf",
		Steps: []*workflow.Step{
			{Name: "step-1", Activity: "a", Store: "val", Next: []*workflow.Edge{{Step: "step-2"}}},
			{Name: "step-2", Activity: "b", Store: "final"},
		},
		Outputs: []*workflow.Output{
			{Name: "final", Variable: "final"},
		},
	})
	require.NoError(t, err)
	return wf
}

func TestRunReturnsResult(t *testing.T) {
	wf := newTestWorkflow(t)
	result := workflowtest.Run(t, wf, []workflow.Activity{
		workflowtest.MockActivity("a", 42),
		workflowtest.MockActivity("b", "done"),
	}, nil)

	require.True(t, result.Completed())
	require.Equal(t, "done", result.Outputs["final"])
}

func TestMockActivityErrorProducesFailure(t *testing.T) {
	wf, err := workflow.New(workflow.Options{
		Name:  "fail-wf",
		Steps: []*workflow.Step{{Name: "boom", Activity: "explode"}},
	})
	require.NoError(t, err)

	result := workflowtest.Run(t, wf, []workflow.Activity{
		workflowtest.MockActivityError("explode", errors.New("kaboom")),
	}, nil)

	require.True(t, result.Failed())
	require.NotNil(t, result.Error)
	require.Contains(t, result.Error.Cause, "kaboom")
}

func TestMemoryCheckpointerRoundTrips(t *testing.T) {
	cp := workflowtest.NewMemoryCheckpointer()
	ctx := context.Background()

	// Save
	err := cp.SaveCheckpoint(ctx, &workflow.Checkpoint{
		SchemaVersion: workflow.CheckpointSchemaVersion,
		ExecutionID:   "exec-1",
		WorkflowName:  "test",
		Status:        workflow.ExecutionStatusCompleted,
	})
	require.NoError(t, err)

	// Load
	loaded, err := cp.LoadCheckpoint(ctx, "exec-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "exec-1", loaded.ExecutionID)
	require.Equal(t, workflow.ExecutionStatusCompleted, loaded.Status)

	// Load missing
	missing, err := cp.LoadCheckpoint(ctx, "nonexistent")
	require.NoError(t, err)
	require.Nil(t, missing)

	// Delete
	err = cp.DeleteCheckpoint(ctx, "exec-1")
	require.NoError(t, err)

	// Confirm deleted
	deleted, err := cp.LoadCheckpoint(ctx, "exec-1")
	require.NoError(t, err)
	require.Nil(t, deleted)
}

func TestMemoryCheckpointerDeepCopies(t *testing.T) {
	cp := workflowtest.NewMemoryCheckpointer()
	ctx := context.Background()

	original := &workflow.Checkpoint{
		SchemaVersion: workflow.CheckpointSchemaVersion,
		ExecutionID:   "exec-1",
		Inputs:        map[string]interface{}{"key": "original"},
	}
	err := cp.SaveCheckpoint(ctx, original)
	require.NoError(t, err)

	// Mutate the original after saving
	original.Inputs["key"] = "mutated"

	// Loaded checkpoint should not reflect the mutation
	loaded, err := cp.LoadCheckpoint(ctx, "exec-1")
	require.NoError(t, err)
	require.Equal(t, "original", loaded.Inputs["key"])
}

func TestRunWithOptionsCustomCheckpointer(t *testing.T) {
	wf := newTestWorkflow(t)
	cp := workflowtest.NewMemoryCheckpointer()

	result := workflowtest.RunWithOptions(t, wf, []workflow.Activity{
		workflowtest.MockActivity("a", 1),
		workflowtest.MockActivity("b", 2),
	}, nil, workflowtest.TestOptions{
		Checkpointer: cp,
	})

	require.True(t, result.Completed())
	// Checkpointer should have received at least one checkpoint
	require.NotEmpty(t, cp.Checkpoints())
}
