package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionService_Create(t *testing.T) {
	t.Run("creates execution and logs event", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		events := testutil.NewMockEventLog()

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
			Events:     events,
		})

		record := &domain.ExecutionRecord{
			ID:           "exec-1",
			WorkflowName: "test-workflow",
			Status:       domain.ExecutionStatusPending,
			Inputs:       map[string]any{"key": "value"},
			CreatedAt:    time.Now(),
		}

		err := svc.Create(context.Background(), record)
		require.NoError(t, err)

		// Verify execution was stored
		stored, err := repo.GetExecution(context.Background(), "exec-1")
		require.NoError(t, err)
		assert.Equal(t, "test-workflow", stored.WorkflowName)

		// Verify event was logged
		evts := events.GetEventsByType(domain.EventTypeWorkflowStarted)
		require.Len(t, evts, 1)
		assert.Equal(t, "exec-1", evts[0].ExecutionID)
		assert.Equal(t, "test-workflow", evts[0].Data["workflow_name"])
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateErr = errors.New("db error")

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		record := &domain.ExecutionRecord{ID: "exec-1"}
		err := svc.Create(context.Background(), record)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "db error")
	})

	t.Run("works without event log", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		record := &domain.ExecutionRecord{ID: "exec-1", WorkflowName: "test"}
		err := svc.Create(context.Background(), record)
		require.NoError(t, err)

		stored, err := repo.GetExecution(context.Background(), "exec-1")
		require.NoError(t, err)
		assert.Equal(t, "test", stored.WorkflowName)
	})
}

func TestExecutionService_Get(t *testing.T) {
	t.Run("retrieves execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:           "exec-1",
			WorkflowName: "test",
			Status:       domain.ExecutionStatusRunning,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		record, err := svc.Get(context.Background(), "exec-1")
		require.NoError(t, err)
		assert.Equal(t, "exec-1", record.ID)
		assert.Equal(t, domain.ExecutionStatusRunning, record.Status)
	})

	t.Run("returns error for non-existent execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		_, err := svc.Get(context.Background(), "nonexistent")
		assert.Error(t, err)
	})
}

func TestExecutionService_Update(t *testing.T) {
	t.Run("updates execution status", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusRunning,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		record := &domain.ExecutionRecord{
			ID:      "exec-1",
			Status:  domain.ExecutionStatusRunning,
			Outputs: map[string]any{"result": "done"},
		}

		err := svc.Update(context.Background(), record)
		require.NoError(t, err)

		stored, _ := repo.GetExecution(context.Background(), "exec-1")
		assert.Equal(t, "done", stored.Outputs["result"])
	})

	t.Run("logs completed event on status change", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		events := testutil.NewMockEventLog()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusRunning,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
			Events:     events,
		})

		record := &domain.ExecutionRecord{
			ID:      "exec-1",
			Status:  domain.ExecutionStatusCompleted,
			Outputs: map[string]any{"result": "success"},
		}

		err := svc.Update(context.Background(), record)
		require.NoError(t, err)

		evts := events.GetEventsByType(domain.EventTypeWorkflowCompleted)
		require.Len(t, evts, 1)
		assert.Equal(t, "exec-1", evts[0].ExecutionID)
	})

	t.Run("logs failed event on status change", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		events := testutil.NewMockEventLog()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusRunning,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
			Events:     events,
		})

		record := &domain.ExecutionRecord{
			ID:        "exec-1",
			Status:    domain.ExecutionStatusFailed,
			LastError: "something went wrong",
		}

		err := svc.Update(context.Background(), record)
		require.NoError(t, err)

		evts := events.GetEventsByType(domain.EventTypeWorkflowFailed)
		require.Len(t, evts, 1)
		assert.Equal(t, "something went wrong", evts[0].Error)
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusRunning,
		})
		repo.UpdateErr = errors.New("update failed")

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		record := &domain.ExecutionRecord{ID: "exec-1", Status: domain.ExecutionStatusCompleted}
		err := svc.Update(context.Background(), record)
		assert.Error(t, err)
	})
}

func TestExecutionService_List(t *testing.T) {
	repo := testutil.NewMockExecutionRepository()
	repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "wf-a",
		Status:       domain.ExecutionStatusCompleted,
	})
	repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
		ID:           "exec-2",
		WorkflowName: "wf-b",
		Status:       domain.ExecutionStatusRunning,
	})
	repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
		ID:           "exec-3",
		WorkflowName: "wf-a",
		Status:       domain.ExecutionStatusRunning,
	})

	svc := NewExecutionService(ExecutionServiceOptions{
		Executions: repo,
	})

	t.Run("list all", func(t *testing.T) {
		records, err := svc.List(context.Background(), domain.ExecutionFilter{})
		require.NoError(t, err)
		assert.Len(t, records, 3)
	})

	t.Run("filter by workflow name", func(t *testing.T) {
		records, err := svc.List(context.Background(), domain.ExecutionFilter{
			WorkflowName: "wf-a",
		})
		require.NoError(t, err)
		assert.Len(t, records, 2)
	})

	t.Run("filter by status", func(t *testing.T) {
		records, err := svc.List(context.Background(), domain.ExecutionFilter{
			Statuses: []domain.ExecutionStatus{domain.ExecutionStatusRunning},
		})
		require.NoError(t, err)
		assert.Len(t, records, 2)
	})
}

func TestExecutionService_Cancel(t *testing.T) {
	t.Run("cancels pending execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		events := testutil.NewMockEventLog()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusPending,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
			Events:     events,
		})

		err := svc.Cancel(context.Background(), "exec-1")
		require.NoError(t, err)

		record, _ := repo.GetExecution(context.Background(), "exec-1")
		assert.Equal(t, domain.ExecutionStatusCancelled, record.Status)
		assert.Equal(t, "cancelled by user", record.LastError)
	})

	t.Run("cancels running execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusRunning,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		err := svc.Cancel(context.Background(), "exec-1")
		require.NoError(t, err)

		record, _ := repo.GetExecution(context.Background(), "exec-1")
		assert.Equal(t, domain.ExecutionStatusCancelled, record.Status)
	})

	t.Run("no-op on completed execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:     "exec-1",
			Status: domain.ExecutionStatusCompleted,
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		err := svc.Cancel(context.Background(), "exec-1")
		require.NoError(t, err)

		record, _ := repo.GetExecution(context.Background(), "exec-1")
		assert.Equal(t, domain.ExecutionStatusCompleted, record.Status)
	})

	t.Run("no-op on already cancelled execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()
		repo.CreateExecution(context.Background(), &domain.ExecutionRecord{
			ID:        "exec-1",
			Status:    domain.ExecutionStatusCancelled,
			LastError: "original reason",
		})

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		err := svc.Cancel(context.Background(), "exec-1")
		require.NoError(t, err)

		record, _ := repo.GetExecution(context.Background(), "exec-1")
		assert.Equal(t, "original reason", record.LastError)
	})

	t.Run("returns error for non-existent execution", func(t *testing.T) {
		repo := testutil.NewMockExecutionRepository()

		svc := NewExecutionService(ExecutionServiceOptions{
			Executions: repo,
		})

		err := svc.Cancel(context.Background(), "nonexistent")
		assert.Error(t, err)
	})
}
