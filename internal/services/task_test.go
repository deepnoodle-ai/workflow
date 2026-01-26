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

func TestTaskService_Create(t *testing.T) {
	t.Run("creates task", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		task := &domain.TaskRecord{
			ID:           "task-1",
			ExecutionID:  "exec-1",
			StepName:     "step-1",
			ActivityName: "activity-1",
			Status:       domain.TaskStatusPending,
			Input:        &domain.TaskInput{Type: "http"},
		}

		err := svc.Create(context.Background(), task)
		require.NoError(t, err)

		stored, err := repo.GetTask(context.Background(), "task-1")
		require.NoError(t, err)
		assert.Equal(t, "step-1", stored.StepName)
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.CreateErr = errors.New("db error")

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		task := &domain.TaskRecord{ID: "task-1"}
		err := svc.Create(context.Background(), task)
		assert.Error(t, err)
	})
}

func TestTaskService_Claim(t *testing.T) {
	t.Run("claims pending task and logs event", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		events := testutil.NewMockEventLog()
		repo.AddTask(&domain.TaskRecord{
			ID:           "task-1",
			ExecutionID:  "exec-1",
			StepName:     "step-1",
			ActivityName: "fetch",
			Attempt:      1,
			Status:       domain.TaskStatusPending,
			Input:        &domain.TaskInput{Type: "http"},
		})

		svc := NewTaskService(TaskServiceOptions{
			Tasks:  repo,
			Events: events,
		})

		claimed, err := svc.Claim(context.Background(), "worker-1")
		require.NoError(t, err)
		require.NotNil(t, claimed)
		assert.Equal(t, "task-1", claimed.ID)
		assert.Equal(t, "fetch", claimed.ActivityName)

		// Verify event was logged
		evts := events.GetEventsByType(domain.EventTypeStepStarted)
		require.Len(t, evts, 1)
		assert.Equal(t, "exec-1", evts[0].ExecutionID)
		assert.Equal(t, "step-1", evts[0].StepName)
		assert.Equal(t, "worker-1", evts[0].Data["worker_id"])
	})

	t.Run("returns nil when no tasks available", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		claimed, err := svc.Claim(context.Background(), "worker-1")
		require.NoError(t, err)
		assert.Nil(t, claimed)
	})

	t.Run("skips tasks not yet visible", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.AddTask(&domain.TaskRecord{
			ID:        "task-1",
			Status:    domain.TaskStatusPending,
			VisibleAt: time.Now().Add(1 * time.Hour), // Not visible yet
		})

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		claimed, err := svc.Claim(context.Background(), "worker-1")
		require.NoError(t, err)
		assert.Nil(t, claimed)
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.ClaimErr = errors.New("claim failed")

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		_, err := svc.Claim(context.Background(), "worker-1")
		assert.Error(t, err)
	})
}

func TestTaskService_Complete(t *testing.T) {
	t.Run("completes task successfully and logs event", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		events := testutil.NewMockEventLog()
		repo.AddTask(&domain.TaskRecord{
			ID:           "task-1",
			ExecutionID:  "exec-1",
			StepName:     "step-1",
			Attempt:      1,
			Status:       domain.TaskStatusRunning,
			WorkerID:     "worker-1",
		})

		svc := NewTaskService(TaskServiceOptions{
			Tasks:  repo,
			Events: events,
		})

		result := &domain.TaskOutput{
			Success: true,
			Data:    map[string]any{"result": "ok"},
		}

		err := svc.Complete(context.Background(), "task-1", "worker-1", result)
		require.NoError(t, err)

		// Verify task status
		task, _ := repo.GetTask(context.Background(), "task-1")
		assert.Equal(t, domain.TaskStatusCompleted, task.Status)

		// Verify event was logged
		evts := events.GetEventsByType(domain.EventTypeStepCompleted)
		require.Len(t, evts, 1)
		assert.Equal(t, "step-1", evts[0].StepName)
	})

	t.Run("completes task with failure and logs event", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		events := testutil.NewMockEventLog()
		repo.AddTask(&domain.TaskRecord{
			ID:          "task-1",
			ExecutionID: "exec-1",
			StepName:    "step-1",
			Attempt:     1,
			Status:      domain.TaskStatusRunning,
			WorkerID:    "worker-1",
		})

		svc := NewTaskService(TaskServiceOptions{
			Tasks:  repo,
			Events: events,
		})

		result := &domain.TaskOutput{
			Success: false,
			Error:   "task failed",
		}

		err := svc.Complete(context.Background(), "task-1", "worker-1", result)
		require.NoError(t, err)

		task, _ := repo.GetTask(context.Background(), "task-1")
		assert.Equal(t, domain.TaskStatusFailed, task.Status)

		evts := events.GetEventsByType(domain.EventTypeStepFailed)
		require.Len(t, evts, 1)
		assert.Equal(t, "task failed", evts[0].Error)
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.AddTask(&domain.TaskRecord{
			ID:       "task-1",
			Status:   domain.TaskStatusRunning,
			WorkerID: "worker-1",
		})
		repo.CompleteErr = errors.New("complete failed")

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		result := &domain.TaskOutput{Success: true}
		err := svc.Complete(context.Background(), "task-1", "worker-1", result)
		assert.Error(t, err)
	})
}

func TestTaskService_Release(t *testing.T) {
	t.Run("releases task and logs event", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		events := testutil.NewMockEventLog()
		repo.AddTask(&domain.TaskRecord{
			ID:          "task-1",
			ExecutionID: "exec-1",
			StepName:    "step-1",
			Attempt:     1,
			Status:      domain.TaskStatusRunning,
			WorkerID:    "worker-1",
		})

		svc := NewTaskService(TaskServiceOptions{
			Tasks:  repo,
			Events: events,
		})

		err := svc.Release(context.Background(), "task-1", "worker-1", 5*time.Second)
		require.NoError(t, err)

		// Verify task was released
		task, _ := repo.GetTask(context.Background(), "task-1")
		assert.Equal(t, domain.TaskStatusPending, task.Status)
		assert.Empty(t, task.WorkerID)
		assert.Equal(t, 2, task.Attempt)

		// Verify event was logged
		evts := events.GetEventsByType(domain.EventTypeStepRetrying)
		require.Len(t, evts, 1)
		assert.Contains(t, evts[0].Data["retry_after"], "5s")
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.AddTask(&domain.TaskRecord{
			ID:       "task-1",
			Status:   domain.TaskStatusRunning,
			WorkerID: "worker-1",
		})
		repo.ReleaseErr = errors.New("release failed")

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		err := svc.Release(context.Background(), "task-1", "worker-1", 0)
		assert.Error(t, err)
	})
}

func TestTaskService_Heartbeat(t *testing.T) {
	t.Run("updates heartbeat", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		oldHeartbeat := time.Now().Add(-1 * time.Minute)
		repo.AddTask(&domain.TaskRecord{
			ID:            "task-1",
			Status:        domain.TaskStatusRunning,
			WorkerID:      "worker-1",
			LastHeartbeat: oldHeartbeat,
		})

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		err := svc.Heartbeat(context.Background(), "task-1", "worker-1")
		require.NoError(t, err)

		task, _ := repo.GetTask(context.Background(), "task-1")
		assert.True(t, task.LastHeartbeat.After(oldHeartbeat))
	})

	t.Run("handles repository error", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.HeartbeatErr = errors.New("heartbeat failed")

		svc := NewTaskService(TaskServiceOptions{
			Tasks: repo,
		})

		err := svc.Heartbeat(context.Background(), "task-1", "worker-1")
		assert.Error(t, err)
	})
}

func TestTaskService_Get(t *testing.T) {
	repo := testutil.NewMockTaskRepository()
	repo.AddTask(&domain.TaskRecord{
		ID:       "task-1",
		StepName: "step-1",
		Status:   domain.TaskStatusPending,
	})

	svc := NewTaskService(TaskServiceOptions{
		Tasks: repo,
	})

	t.Run("retrieves task", func(t *testing.T) {
		task, err := svc.Get(context.Background(), "task-1")
		require.NoError(t, err)
		assert.Equal(t, "step-1", task.StepName)
	})

	t.Run("returns error for non-existent task", func(t *testing.T) {
		_, err := svc.Get(context.Background(), "nonexistent")
		assert.Error(t, err)
	})
}

func TestTaskService_ListStale(t *testing.T) {
	repo := testutil.NewMockTaskRepository()
	oldTime := time.Now().Add(-10 * time.Minute)
	recentTime := time.Now()

	repo.AddTask(&domain.TaskRecord{
		ID:            "task-1",
		Status:        domain.TaskStatusRunning,
		LastHeartbeat: oldTime,
	})
	repo.AddTask(&domain.TaskRecord{
		ID:            "task-2",
		Status:        domain.TaskStatusRunning,
		LastHeartbeat: recentTime,
	})
	repo.AddTask(&domain.TaskRecord{
		ID:            "task-3",
		Status:        domain.TaskStatusPending,
		LastHeartbeat: oldTime,
	})

	svc := NewTaskService(TaskServiceOptions{
		Tasks: repo,
	})

	cutoff := time.Now().Add(-5 * time.Minute)
	stale, err := svc.ListStale(context.Background(), cutoff)
	require.NoError(t, err)
	assert.Len(t, stale, 1)
	assert.Equal(t, "task-1", stale[0].ID)
}

func TestTaskService_Reset(t *testing.T) {
	repo := testutil.NewMockTaskRepository()
	repo.AddTask(&domain.TaskRecord{
		ID:       "task-1",
		Status:   domain.TaskStatusRunning,
		WorkerID: "worker-1",
		Attempt:  1,
	})

	svc := NewTaskService(TaskServiceOptions{
		Tasks: repo,
	})

	err := svc.Reset(context.Background(), "task-1")
	require.NoError(t, err)

	task, _ := repo.GetTask(context.Background(), "task-1")
	assert.Equal(t, domain.TaskStatusPending, task.Status)
	assert.Empty(t, task.WorkerID)
	assert.Equal(t, 2, task.Attempt)
}
