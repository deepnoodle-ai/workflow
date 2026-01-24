package workflow_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/deepnoodle-ai/wonton/assert"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/deepnoodle-ai/workflow"
	pgstore "github.com/deepnoodle-ai/workflow/internal/postgres"
)

// setupPostgres creates a Postgres container and returns a connected *sql.DB.
func setupPostgres(t *testing.T) (*sql.DB, func()) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	assert.NoError(t, err)

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	assert.NoError(t, err)

	db, err := sql.Open("postgres", connStr)
	assert.NoError(t, err)

	err = db.Ping()
	assert.NoError(t, err)

	cleanup := func() {
		db.Close()
		pgContainer.Terminate(ctx)
	}

	return db, cleanup
}

func TestPostgresStore_CreateAndGetExecution(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	// Create schema
	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create a record
	record := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{"key": "value"},
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
	err = store.CreateExecution(ctx, record)
	assert.NoError(t, err)

	// Get the record
	retrieved, err := store.GetExecution(ctx, "exec-1")
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, retrieved.ID, "exec-1")
	assert.Equal(t, retrieved.WorkflowName, "test-workflow")
	assert.Equal(t, retrieved.Status, workflow.EngineStatusPending)
	assert.Equal(t, retrieved.Inputs["key"], "value")
}

func TestPostgresStore_ListExecutions(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create multiple records
	for i := 1; i <= 5; i++ {
		status := workflow.EngineStatusPending
		if i > 3 {
			status = workflow.EngineStatusCompleted
		}
		record := &workflow.ExecutionRecord{
			ID:           "exec-" + string(rune('0'+i)),
			WorkflowName: "test-workflow",
			Status:       status,
			Inputs:       map[string]any{},
			CreatedAt:    time.Now().UTC(),
		}
		err := store.CreateExecution(ctx, record)
		assert.NoError(t, err)
	}

	// List all
	records, err := store.ListExecutions(ctx, workflow.ExecutionFilter{})
	assert.NoError(t, err)
	assert.Len(t, records, 5)

	// List by status
	records, err = store.ListExecutions(ctx, workflow.ExecutionFilter{
		Statuses: []workflow.EngineExecutionStatus{workflow.EngineStatusPending},
	})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// List with limit
	records, err = store.ListExecutions(ctx, workflow.ExecutionFilter{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, records, 2)
}

func TestPostgresStore_UpdateExecution(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create record
	record := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, record)
	assert.NoError(t, err)

	// Update record
	record.Status = workflow.EngineStatusRunning
	record.CurrentStep = "step1"
	err = store.UpdateExecution(ctx, record)
	assert.NoError(t, err)

	// Verify update
	retrieved, err := store.GetExecution(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, workflow.EngineStatusRunning)
	assert.Equal(t, retrieved.CurrentStep, "step1")
}

func TestPostgresStore_CreateTask(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution first (for foreign key)
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	// Create task
	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec: &workflow.TaskSpec{
			Type:  "inline",
			Input: map[string]any{"key": "value"},
		},
		VisibleAt: now,
		CreatedAt: now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Get task
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.ID, "task-1")
	assert.Equal(t, retrieved.StepName, "step1")
	assert.Equal(t, retrieved.Spec.Type, "inline")
}

func TestPostgresStore_ClaimTask(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	// Create task
	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second), // Already visible
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim task
	claimed, err := store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)
	assert.NotNil(t, claimed)
	assert.Equal(t, claimed.ID, "task-1")

	// Verify task is now running
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, workflow.TaskStatusRunning)
	assert.Equal(t, retrieved.WorkerID, "worker-1")

	// No more tasks to claim
	claimed, err = store.ClaimTask(ctx, "worker-2")
	assert.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestPostgresStore_CompleteTask(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution and task
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Complete
	result := &workflow.TaskResult{
		Success: true,
		Data:    map[string]any{"result": "success"},
	}
	err = store.CompleteTask(ctx, "task-1", "worker-1", result)
	assert.NoError(t, err)

	// Verify status
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, workflow.TaskStatusCompleted)
	assert.Equal(t, retrieved.Result.Data["result"], "success")
}

func TestPostgresStore_CompleteTask_Failure(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution and task
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Complete with failure
	result := &workflow.TaskResult{
		Success: false,
		Error:   "something went wrong",
	}
	err = store.CompleteTask(ctx, "task-1", "worker-1", result)
	assert.NoError(t, err)

	// Verify status
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, workflow.TaskStatusFailed)
	assert.Equal(t, retrieved.Result.Error, "something went wrong")
}

func TestPostgresStore_HeartbeatTask(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution and task
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	// Claim
	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Get initial heartbeat
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	initialHeartbeat := retrieved.LastHeartbeat

	// Wait and heartbeat
	time.Sleep(10 * time.Millisecond)
	err = store.HeartbeatTask(ctx, "task-1", "worker-1")
	assert.NoError(t, err)

	// Verify heartbeat updated
	retrieved, err = store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.True(t, retrieved.LastHeartbeat.After(initialHeartbeat))
}

func TestPostgresStore_ListStaleTasks(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	// Create and claim task
	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// List stale with future cutoff - should find it
	stale, err := store.ListStaleTasks(ctx, time.Now().Add(1*time.Hour))
	assert.NoError(t, err)
	assert.Len(t, stale, 1)
	assert.Equal(t, stale[0].ID, "task-1")

	// List stale with past cutoff - should not find it
	stale, err = store.ListStaleTasks(ctx, time.Now().Add(-1*time.Hour))
	assert.NoError(t, err)
	assert.Len(t, stale, 0)
}

func TestPostgresStore_ResetTask(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	// Create and claim task
	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Reset
	err = store.ResetTask(ctx, "task-1")
	assert.NoError(t, err)

	// Verify task is pending again
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, workflow.TaskStatusPending)
	assert.Equal(t, retrieved.WorkerID, "")
	assert.Equal(t, retrieved.Attempt, 2)
}

func TestPostgresStore_ReleaseTask(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := pgstore.NewStore(pgstore.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create execution
	exec := &workflow.ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       workflow.EngineStatusPending,
		Inputs:       map[string]any{},
		CreatedAt:    time.Now().UTC(),
	}
	err = store.CreateExecution(ctx, exec)
	assert.NoError(t, err)

	// Create and claim task
	now := time.Now().UTC()
	task := &workflow.TaskRecord{
		ID:          "task-1",
		ExecutionID: "exec-1",
		StepName:    "step1",
		Attempt:     1,
		Status:      workflow.TaskStatusPending,
		Spec:        &workflow.TaskSpec{Type: "inline"},
		VisibleAt:   now.Add(-time.Second),
		CreatedAt:   now,
	}
	err = store.CreateTask(ctx, task)
	assert.NoError(t, err)

	_, err = store.ClaimTask(ctx, "worker-1")
	assert.NoError(t, err)

	// Release with retry delay
	err = store.ReleaseTask(ctx, "task-1", "worker-1", 5*time.Minute)
	assert.NoError(t, err)

	// Verify task is pending with incremented attempt and delayed visibility
	retrieved, err := store.GetTask(ctx, "task-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, workflow.TaskStatusPending)
	assert.Equal(t, retrieved.WorkerID, "")
	assert.Equal(t, retrieved.Attempt, 2)
	assert.True(t, retrieved.VisibleAt.After(now))
}
