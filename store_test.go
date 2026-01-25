package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/memory"
)

func TestMemoryStore_CreateExecution(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	record := &domain.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       domain.ExecutionStatusPending,
		Inputs:       map[string]any{"key": "value"},
		CreatedAt:    time.Now(),
	}

	err := store.CreateExecution(ctx, record)
	assert.NoError(t, err)

	// Duplicate should fail
	err = store.CreateExecution(ctx, record)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestMemoryStore_GetExecution(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Get non-existent record should fail
	_, err := store.GetExecution(ctx, "non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Create and get
	record := &domain.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       domain.ExecutionStatusPending,
		Inputs:       map[string]any{"key": "value"},
		CreatedAt:    time.Now(),
	}
	err = store.CreateExecution(ctx, record)
	assert.NoError(t, err)

	retrieved, err := store.GetExecution(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.ID, "exec-1")
	assert.Equal(t, retrieved.WorkflowName, "test-workflow")
	assert.Equal(t, retrieved.Status, domain.ExecutionStatusPending)

	// Verify it's a copy, not the same instance
	retrieved.Status = domain.ExecutionStatusRunning
	original, _ := store.GetExecution(ctx, "exec-1")
	assert.Equal(t, original.Status, domain.ExecutionStatusPending)
}

func TestMemoryStore_ListExecutions(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Create multiple records
	for i := 1; i <= 5; i++ {
		status := domain.ExecutionStatusPending
		if i%2 == 0 {
			status = domain.ExecutionStatusCompleted
		}
		err := store.CreateExecution(ctx, &domain.ExecutionRecord{
			ID:           "exec-" + string(rune('0'+i)),
			WorkflowName: "workflow-" + string(rune('A'+i%2)),
			Status:       status,
			CreatedAt:    time.Now(),
		})
		assert.NoError(t, err)
	}

	// List all
	records, err := store.ListExecutions(ctx, domain.ExecutionFilter{})
	assert.NoError(t, err)
	assert.Len(t, records, 5)

	// Filter by status
	records, err = store.ListExecutions(ctx, domain.ExecutionFilter{Statuses: []domain.ExecutionStatus{domain.ExecutionStatusPending}})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// Filter by workflow name (i=1,3,5 have i%2=1 -> B; i=2,4 have i%2=0 -> A)
	records, err = store.ListExecutions(ctx, domain.ExecutionFilter{WorkflowName: "workflow-B"})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// Limit
	records, err = store.ListExecutions(ctx, domain.ExecutionFilter{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, records, 2)
}

func TestMemoryStore_UpdateExecution(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	record := &domain.ExecutionRecord{
		ID:        "exec-1",
		Status:    domain.ExecutionStatusPending,
		CreatedAt: time.Now(),
	}
	err := store.CreateExecution(ctx, record)
	assert.NoError(t, err)

	// Update
	record.Status = domain.ExecutionStatusRunning
	record.CurrentStep = "step1"
	err = store.UpdateExecution(ctx, record)
	assert.NoError(t, err)

	// Verify
	retrieved, err := store.GetExecution(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, domain.ExecutionStatusRunning)
	assert.Equal(t, retrieved.CurrentStep, "step1")

	// Update non-existent should fail
	err = store.UpdateExecution(ctx, &domain.ExecutionRecord{ID: "non-existent"})
	assert.Error(t, err)
}

func TestMemoryStore_CreateTask(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec: &domain.TaskSpec{
			Type:  "inline",
			Input: map[string]any{"key": "value"},
		},
		VisibleAt: time.Now(),
		CreatedAt: time.Now(),
	}

	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Duplicate should fail
	err = store.CreateTask(ctx, task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestMemoryStore_ClaimTask(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec: &domain.TaskSpec{
			Type: "inline",
		},
		VisibleAt: now.Add(-time.Second), // Already visible
		CreatedAt: now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim task
	claimed, err := store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)
	assert.Equal(t, claimed.ID, "task-1")
	assert.Equal(t, claimed.StepName, "step1")

	// Verify task is now running
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, domain.TaskStatusRunning)
	assert.Equal(t, retrieved.WorkerID, "worker-1")

	// No more tasks to claim
	claimed, err = store.ClaimTask(ctx, "worker-2")
	assert.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestMemoryStore_ClaimTask_VisibleAt(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()

	// Create task that's not visible yet
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec:        &domain.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(time.Hour), // Not visible yet
		CreatedAt:   now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Should not be claimable
	claimed, err := store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestMemoryStore_CompleteTask(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec:        &domain.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	claimed, err := store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)

	// Complete with success
	result := &domain.TaskResult{
		Success: true,
		Data:    map[string]any{"result": "success"},
	}
	err = store.CompleteTask(ctx, "task-1", "worker-1", result)
	assert.NoError(t, err)

	// Verify status
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, domain.TaskStatusCompleted)
	assert.Equal(t, retrieved.Result.Data["result"], "success")

	// Wrong worker cannot complete
	err = store.CompleteTask(ctx, "task-1", "worker-2", result)
	assert.Error(t, err)
}

func TestMemoryStore_CompleteTask_Failure(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec:        &domain.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	claimed, err := store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)

	// Complete with failure
	result := &domain.TaskResult{
		Success: false,
		Error:   "something went wrong",
	}
	err = store.CompleteTask(ctx, "task-1", "worker-1", result)
	assert.NoError(t, err)

	// Verify status
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, domain.TaskStatusFailed)
	assert.Equal(t, retrieved.Result.Error, "something went wrong")
}

func TestMemoryStore_ReleaseTask(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec:        &domain.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Release with retry after
	err = store.ReleaseTask(ctx, "task-1", "worker-1", 5*time.Minute)
	assert.NoError(t, err)

	// Verify task is pending again with incremented attempt
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, domain.TaskStatusPending)
	assert.Equal(t, retrieved.Attempt, 2)
	assert.True(t, retrieved.VisibleAt.After(now))
}

func TestMemoryStore_HeartbeatTask(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusPending,
		Spec:        &domain.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Record initial heartbeat
	retrieved, _ := store.GetTask(ctx, "task-1")
	initialHeartbeat := retrieved.LastHeartbeat

	// Sleep and heartbeat
	time.Sleep(10 * time.Millisecond)
	err = store.HeartbeatTask(ctx, "task-1", "worker-1")
	assert.NoError(t, err)

	// Verify heartbeat updated
	retrieved, _ = store.GetTask(ctx, "task-1")
	assert.True(t, retrieved.LastHeartbeat.After(initialHeartbeat))

	// Wrong worker cannot heartbeat
	err = store.HeartbeatTask(ctx, "task-1", "worker-2")
	assert.Error(t, err)
}

func TestMemoryStore_ListStaleTasks(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	oldTime := now.Add(-5 * time.Minute)
	cutoff := now.Add(-2 * time.Minute)

	// Create task with old heartbeat (stale)
	staleTask := &domain.TaskRecord{
		ID:            "stale-1",
		ExecutionID:   "exec-1",
		StepName:      "step1",
		Attempt:       1,
		Status:        domain.TaskStatusRunning,
		Spec:          &domain.TaskSpec{Type: "inline"},
		WorkerID:      "worker-1",
		LastHeartbeat: oldTime,
		VisibleAt:     now,
		CreatedAt:     now,
	}
	err := store.CreateTask(ctx, staleTask)
	assert.NoError(t, err)

	// Create task with recent heartbeat (not stale)
	freshTask := &domain.TaskRecord{
		ID:            "fresh-1",
		ExecutionID:   "exec-2",
		StepName:      "step1",
		Attempt:       1,
		Status:        domain.TaskStatusRunning,
		Spec:          &domain.TaskSpec{Type: "inline"},
		WorkerID:      "worker-2",
		LastHeartbeat: now,
		VisibleAt:     now,
		CreatedAt:     now,
	}
	err = store.CreateTask(ctx, freshTask)
	assert.NoError(t, err)

	// List stale tasks
	stale, err := store.ListStaleTasks(ctx, cutoff)
	assert.NoError(t, err)
	assert.Len(t, stale, 1)
	assert.Equal(t, stale[0].ID, "stale-1")
}

func TestMemoryStore_ResetTask(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	now := time.Now()
	task := &domain.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      domain.TaskStatusRunning,
		Spec:        &domain.TaskSpec{Type: "inline"},
		WorkerID:    "worker-1",
		VisibleAt:   now,
		CreatedAt:   now,
		StartedAt:   now,
	}
	err := store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Reset
	err = store.ResetTask(ctx, "task-1")
	assert.NoError(t, err)

	// Verify task is pending again
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, domain.TaskStatusPending)
	assert.Equal(t, retrieved.WorkerID, "")
	assert.Equal(t, retrieved.Attempt, 2)
}
