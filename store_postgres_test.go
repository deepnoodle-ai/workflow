package workflow

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

func TestPostgresStore_CreateAndGet(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	// Create schema
	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create a record
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{"key": "value"},
		Attempt:      1,
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	// Get the record
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.NotNil(t, retrieved)
	assert.Equal(t, retrieved.ID, "exec-1")
	assert.Equal(t, retrieved.WorkflowName, "test-workflow")
	assert.Equal(t, retrieved.Status, EngineStatusPending)
	assert.Equal(t, retrieved.Inputs["key"], "value")
	assert.Equal(t, retrieved.Attempt, 1)
}

func TestPostgresStore_List(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create multiple records
	for i := 1; i <= 5; i++ {
		status := EngineStatusPending
		if i > 3 {
			status = EngineStatusCompleted
		}
		record := &ExecutionRecord{
			ID:           "exec-" + string(rune('0'+i)),
			WorkflowName: "test-workflow",
			Status:       status,
			Inputs:       map[string]any{},
			Attempt:      1,
			CreatedAt:    time.Now().UTC(),
		}
		err := store.Create(ctx, record)
		assert.NoError(t, err)
	}

	// List all
	records, err := store.List(ctx, ListFilter{})
	assert.NoError(t, err)
	assert.Len(t, records, 5)

	// List by status
	records, err = store.List(ctx, ListFilter{
		Statuses: []EngineExecutionStatus{EngineStatusPending},
	})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// List with limit
	records, err = store.List(ctx, ListFilter{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, records, 2)
}

func TestPostgresStore_ClaimExecution(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create a pending record
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	// Claim with correct attempt
	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	assert.NoError(t, err)
	assert.True(t, claimed)

	// Verify status changed
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, EngineStatusRunning)
	assert.Equal(t, retrieved.WorkerID, "worker-1")

	// Cannot claim again with same attempt
	claimed, err = store.ClaimExecution(ctx, "exec-1", "worker-2", 1)
	assert.NoError(t, err)
	assert.False(t, claimed)

	// Cannot claim with wrong attempt
	claimed, err = store.ClaimExecution(ctx, "exec-1", "worker-2", 2)
	assert.NoError(t, err)
	assert.False(t, claimed)
}

func TestPostgresStore_CompleteExecution(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create and claim
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	assert.NoError(t, err)
	assert.True(t, claimed)

	// Complete with correct attempt
	outputs := map[string]any{"result": "success"}
	completed, err := store.CompleteExecution(ctx, "exec-1", 1, EngineStatusCompleted, outputs, "")
	assert.NoError(t, err)
	assert.True(t, completed)

	// Verify status
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, EngineStatusCompleted)
	assert.Equal(t, retrieved.Outputs["result"], "success")

	// Cannot complete again
	completed, err = store.CompleteExecution(ctx, "exec-1", 1, EngineStatusFailed, nil, "error")
	assert.NoError(t, err)
	assert.False(t, completed)
}

func TestPostgresStore_CompleteExecution_WithError(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create and claim
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	assert.NoError(t, err)
	assert.True(t, claimed)

	// Complete with error
	completed, err := store.CompleteExecution(ctx, "exec-1", 1, EngineStatusFailed, nil, "something went wrong")
	assert.NoError(t, err)
	assert.True(t, completed)

	// Verify error stored
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, EngineStatusFailed)
	assert.Equal(t, retrieved.LastError, "something went wrong")
}

func TestPostgresStore_Heartbeat(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create and claim
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	assert.NoError(t, err)
	assert.True(t, claimed)

	// Get initial heartbeat
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	initialHeartbeat := retrieved.LastHeartbeat

	// Wait a bit and update heartbeat
	time.Sleep(10 * time.Millisecond)
	err = store.Heartbeat(ctx, "exec-1", "worker-1")
	assert.NoError(t, err)

	// Verify heartbeat updated
	retrieved, err = store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.True(t, retrieved.LastHeartbeat.After(initialHeartbeat))
}

func TestPostgresStore_ListStaleRunning(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create and claim
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	assert.NoError(t, err)
	assert.True(t, claimed)

	// List stale with future cutoff - should find it
	stale, err := store.ListStaleRunning(ctx, time.Now().Add(1*time.Hour))
	assert.NoError(t, err)
	assert.Len(t, stale, 1)
	assert.Equal(t, stale[0].ID, "exec-1")

	// List stale with past cutoff - should not find it
	stale, err = store.ListStaleRunning(ctx, time.Now().Add(-1*time.Hour))
	assert.NoError(t, err)
	assert.Len(t, stale, 0)
}

func TestPostgresStore_MarkDispatched(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create record
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	// Mark dispatched
	err = store.MarkDispatched(ctx, "exec-1", 1)
	assert.NoError(t, err)

	// Verify dispatched_at is set
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.False(t, retrieved.DispatchedAt.IsZero())
}

func TestPostgresStore_ListStalePending(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create record
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	// Mark dispatched
	err = store.MarkDispatched(ctx, "exec-1", 1)
	assert.NoError(t, err)

	// List stale pending with future cutoff - should find it
	stale, err := store.ListStalePending(ctx, time.Now().Add(1*time.Hour))
	assert.NoError(t, err)
	assert.Len(t, stale, 1)

	// List stale pending with past cutoff - should not find it
	stale, err = store.ListStalePending(ctx, time.Now().Add(-1*time.Hour))
	assert.NoError(t, err)
	assert.Len(t, stale, 0)
}

func TestPostgresStore_Update(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Create record
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{},
		Attempt:      1,
		CreatedAt:    time.Now().UTC(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	// Update record
	record.Attempt = 2
	record.Status = EngineStatusPending
	record.WorkerID = ""
	err = store.Update(ctx, record)
	assert.NoError(t, err)

	// Verify update
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Attempt, 2)
}
