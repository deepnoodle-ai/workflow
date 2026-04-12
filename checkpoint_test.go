package workflow_test

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
	"github.com/deepnoodle-ai/workflow/workflowtest"
)

func TestCheckpointSchemaVersionIsSetOnSave(t *testing.T) {
	cp := workflowtest.NewMemoryCheckpointer()
	wf, err := workflow.New(workflow.Options{
		Name:  "schema-test",
		Steps: []*workflow.Step{{Name: "start", Activity: "noop"}},
	})
	require.NoError(t, err)

	reg := workflow.NewActivityRegistry()
	reg.MustRegister(workflow.ActivityFunc("noop", func(ctx workflow.Context, params map[string]any) (any, error) {
		return nil, nil
	}))

	exec, err := workflow.NewExecution(wf, reg,
		workflow.WithCheckpointer(cp),
	)
	require.NoError(t, err)

	_, err = exec.Execute(context.Background())
	require.NoError(t, err)

	loaded, err := cp.LoadCheckpoint(context.Background(), exec.ID())
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, workflow.CheckpointSchemaVersion, loaded.SchemaVersion)
}

func TestCheckpointNewerSchemaVersionIsRejected(t *testing.T) {
	cp := workflowtest.NewMemoryCheckpointer()
	err := cp.SaveCheckpoint(context.Background(), &workflow.Checkpoint{
		SchemaVersion: workflow.CheckpointSchemaVersion + 1,
		ID:            "cp1",
		ExecutionID:   "future-exec",
		WorkflowName:  "test",
		Status:        workflow.ExecutionStatusRunning,
	})
	require.NoError(t, err)

	_, err = cp.LoadCheckpoint(context.Background(), "future-exec")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}

func TestCheckpointOlderSchemaVersionIsRejected(t *testing.T) {
	cp := workflowtest.NewMemoryCheckpointer()
	err := cp.SaveCheckpoint(context.Background(), &workflow.Checkpoint{
		SchemaVersion: 0,
		ID:            "cp1",
		ExecutionID:   "old-exec",
		WorkflowName:  "test",
		Status:        workflow.ExecutionStatusRunning,
	})
	require.NoError(t, err)

	_, err = cp.LoadCheckpoint(context.Background(), "old-exec")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not supported")
}

func TestMemoryCheckpointer_AtomicUpdate(t *testing.T) {
	cp := workflowtest.NewMemoryCheckpointer()

	initial := &workflow.Checkpoint{
		SchemaVersion: workflow.CheckpointSchemaVersion,
		ID:            "cp1",
		ExecutionID:   "exec",
		WorkflowName:  "test",
		Status:        workflow.ExecutionStatusRunning,
		BranchStates: map[string]*workflow.BranchState{
			"main": {ID: "main", Status: workflow.ExecutionStatusRunning, CurrentStep: "s1"},
		},
	}
	require.NoError(t, cp.SaveCheckpoint(context.Background(), initial))

	err := cp.AtomicUpdate(context.Background(), "exec", func(c *workflow.Checkpoint) error {
		c.BranchStates["main"].PauseRequested = true
		return nil
	})
	require.NoError(t, err)

	loaded, err := cp.LoadCheckpoint(context.Background(), "exec")
	require.NoError(t, err)
	require.True(t, loaded.BranchStates["main"].PauseRequested)
}

func TestAtomicCheckpointerInterface(t *testing.T) {
	var _ workflow.Checkpointer = (*workflowtest.MemoryCheckpointer)(nil)
	var _ workflow.AtomicCheckpointer = (*workflowtest.MemoryCheckpointer)(nil)
}
