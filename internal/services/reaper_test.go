package services

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReaperService_ReapStaleTasks(t *testing.T) {
	t.Run("finds and resets stale tasks", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		staleTime := time.Now().Add(-10 * time.Minute)
		recentTime := time.Now()

		// Stale running task
		repo.AddTask(&domain.TaskRecord{
			ID:            "task-1",
			Status:        domain.TaskStatusRunning,
			WorkerID:      "worker-1",
			LastHeartbeat: staleTime,
			Attempt:       1,
		})
		// Recent running task (should not be reaped)
		repo.AddTask(&domain.TaskRecord{
			ID:            "task-2",
			Status:        domain.TaskStatusRunning,
			WorkerID:      "worker-2",
			LastHeartbeat: recentTime,
			Attempt:       1,
		})
		// Stale but not running (should not be reaped)
		repo.AddTask(&domain.TaskRecord{
			ID:            "task-3",
			Status:        domain.TaskStatusPending,
			LastHeartbeat: staleTime,
			Attempt:       1,
		})

		svc := NewReaperService(ReaperServiceOptions{
			Tasks:            repo,
			HeartbeatTimeout: 2 * time.Minute,
		})

		count, err := svc.ReapStaleTasks(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, count)

		// Verify task-1 was reset
		task1, _ := repo.GetTask(context.Background(), "task-1")
		assert.Equal(t, domain.TaskStatusPending, task1.Status)
		assert.Empty(t, task1.WorkerID)
		assert.Equal(t, 2, task1.Attempt)

		// Verify task-2 was not touched
		task2, _ := repo.GetTask(context.Background(), "task-2")
		assert.Equal(t, domain.TaskStatusRunning, task2.Status)
		assert.Equal(t, "worker-2", task2.WorkerID)
	})

	t.Run("handles no stale tasks", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.AddTask(&domain.TaskRecord{
			ID:            "task-1",
			Status:        domain.TaskStatusRunning,
			WorkerID:      "worker-1",
			LastHeartbeat: time.Now(),
		})

		svc := NewReaperService(ReaperServiceOptions{
			Tasks:            repo,
			HeartbeatTimeout: 2 * time.Minute,
		})

		count, err := svc.ReapStaleTasks(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})

	t.Run("handles list error", func(t *testing.T) {
		repo := testutil.NewMockTaskRepository()
		repo.ListStaleErr = errors.New("db error")

		svc := NewReaperService(ReaperServiceOptions{
			Tasks:            repo,
			HeartbeatTimeout: 2 * time.Minute,
		})

		_, err := svc.ReapStaleTasks(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "db error")
	})

	t.Run("continues on individual reset failure", func(t *testing.T) {
		repo := &partialFailRepo{
			MockTaskRepository: testutil.NewMockTaskRepository(),
			failOnTaskID:       "task-1",
		}
		staleTime := time.Now().Add(-10 * time.Minute)

		repo.AddTask(&domain.TaskRecord{
			ID:            "task-1",
			Status:        domain.TaskStatusRunning,
			WorkerID:      "worker-1",
			LastHeartbeat: staleTime,
			Attempt:       1,
		})
		repo.AddTask(&domain.TaskRecord{
			ID:            "task-2",
			Status:        domain.TaskStatusRunning,
			WorkerID:      "worker-2",
			LastHeartbeat: staleTime,
			Attempt:       1,
		})

		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
		svc := NewReaperService(ReaperServiceOptions{
			Tasks:            repo,
			HeartbeatTimeout: 2 * time.Minute,
			Logger:           logger,
		})

		count, err := svc.ReapStaleTasks(context.Background())
		require.NoError(t, err)
		// Only task-2 should be reset (task-1 failed)
		assert.Equal(t, 1, count)

		// task-1 should still be running (reset failed)
		task1, _ := repo.GetTask(context.Background(), "task-1")
		assert.Equal(t, domain.TaskStatusRunning, task1.Status)

		// task-2 should be reset
		task2, _ := repo.GetTask(context.Background(), "task-2")
		assert.Equal(t, domain.TaskStatusPending, task2.Status)
	})
}

func TestReaperService_DefaultTimeout(t *testing.T) {
	repo := testutil.NewMockTaskRepository()

	svc := NewReaperService(ReaperServiceOptions{
		Tasks: repo,
		// HeartbeatTimeout not set, should default to 2 minutes
	})

	assert.Equal(t, 2*time.Minute, svc.heartbeatTimeout)
}

func TestReaperService_RecoverStaleTasks(t *testing.T) {
	repo := testutil.NewMockTaskRepository()
	staleTime := time.Now().Add(-10 * time.Minute)

	repo.AddTask(&domain.TaskRecord{
		ID:            "task-1",
		Status:        domain.TaskStatusRunning,
		WorkerID:      "worker-1",
		LastHeartbeat: staleTime,
		Attempt:       1,
	})

	svc := NewReaperService(ReaperServiceOptions{
		Tasks:            repo,
		HeartbeatTimeout: 2 * time.Minute,
	})

	// RecoverStaleTasks should call ReapStaleTasks
	count, err := svc.RecoverStaleTasks(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// partialFailRepo is a mock that fails to reset specific tasks
type partialFailRepo struct {
	*testutil.MockTaskRepository
	failOnTaskID string
}

func (r *partialFailRepo) ResetTask(ctx context.Context, taskID string) error {
	if taskID == r.failOnTaskID {
		return errors.New("reset failed")
	}
	return r.MockTaskRepository.ResetTask(ctx, taskID)
}
