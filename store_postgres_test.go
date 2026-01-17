package workflow

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("postgres", connStr)
	require.NoError(t, err)

	err = db.Ping()
	require.NoError(t, err)

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
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Get the record
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	require.Equal(t, "exec-1", retrieved.ID)
	require.Equal(t, "test-workflow", retrieved.WorkflowName)
	require.Equal(t, EngineStatusPending, retrieved.Status)
	require.Equal(t, "value", retrieved.Inputs["key"])
	require.Equal(t, 1, retrieved.Attempt)
}

func TestPostgresStore_List(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
		require.NoError(t, err)
	}

	// List all
	records, err := store.List(ctx, ListFilter{})
	require.NoError(t, err)
	require.Len(t, records, 5)

	// List by status
	records, err = store.List(ctx, ListFilter{
		Statuses: []EngineExecutionStatus{EngineStatusPending},
	})
	require.NoError(t, err)
	require.Len(t, records, 3)

	// List with limit
	records, err = store.List(ctx, ListFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, records, 2)
}

func TestPostgresStore_ClaimExecution(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Claim with correct attempt
	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	require.NoError(t, err)
	require.True(t, claimed)

	// Verify status changed
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.Equal(t, EngineStatusRunning, retrieved.Status)
	require.Equal(t, "worker-1", retrieved.WorkerID)

	// Cannot claim again with same attempt
	claimed, err = store.ClaimExecution(ctx, "exec-1", "worker-2", 1)
	require.NoError(t, err)
	require.False(t, claimed)

	// Cannot claim with wrong attempt
	claimed, err = store.ClaimExecution(ctx, "exec-1", "worker-2", 2)
	require.NoError(t, err)
	require.False(t, claimed)
}

func TestPostgresStore_CompleteExecution(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	require.NoError(t, err)
	require.True(t, claimed)

	// Complete with correct attempt
	outputs := map[string]any{"result": "success"}
	completed, err := store.CompleteExecution(ctx, "exec-1", 1, EngineStatusCompleted, outputs, "")
	require.NoError(t, err)
	require.True(t, completed)

	// Verify status
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.Equal(t, EngineStatusCompleted, retrieved.Status)
	require.Equal(t, "success", retrieved.Outputs["result"])

	// Cannot complete again
	completed, err = store.CompleteExecution(ctx, "exec-1", 1, EngineStatusFailed, nil, "error")
	require.NoError(t, err)
	require.False(t, completed)
}

func TestPostgresStore_CompleteExecution_WithError(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	require.NoError(t, err)
	require.True(t, claimed)

	// Complete with error
	completed, err := store.CompleteExecution(ctx, "exec-1", 1, EngineStatusFailed, nil, "something went wrong")
	require.NoError(t, err)
	require.True(t, completed)

	// Verify error stored
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.Equal(t, EngineStatusFailed, retrieved.Status)
	require.Equal(t, "something went wrong", retrieved.LastError)
}

func TestPostgresStore_Heartbeat(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	require.NoError(t, err)
	require.True(t, claimed)

	// Get initial heartbeat
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	initialHeartbeat := retrieved.LastHeartbeat

	// Wait a bit and update heartbeat
	time.Sleep(10 * time.Millisecond)
	err = store.Heartbeat(ctx, "exec-1", "worker-1")
	require.NoError(t, err)

	// Verify heartbeat updated
	retrieved, err = store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.True(t, retrieved.LastHeartbeat.After(initialHeartbeat))
}

func TestPostgresStore_ListStaleRunning(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	require.NoError(t, err)
	require.True(t, claimed)

	// List stale with future cutoff - should find it
	stale, err := store.ListStaleRunning(ctx, time.Now().Add(1*time.Hour))
	require.NoError(t, err)
	require.Len(t, stale, 1)
	require.Equal(t, "exec-1", stale[0].ID)

	// List stale with past cutoff - should not find it
	stale, err = store.ListStaleRunning(ctx, time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	require.Len(t, stale, 0)
}

func TestPostgresStore_MarkDispatched(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Mark dispatched
	err = store.MarkDispatched(ctx, "exec-1", 1)
	require.NoError(t, err)

	// Verify dispatched_at is set
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.False(t, retrieved.DispatchedAt.IsZero())
}

func TestPostgresStore_ListStalePending(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Mark dispatched
	err = store.MarkDispatched(ctx, "exec-1", 1)
	require.NoError(t, err)

	// List stale pending with future cutoff - should find it
	stale, err := store.ListStalePending(ctx, time.Now().Add(1*time.Hour))
	require.NoError(t, err)
	require.Len(t, stale, 1)

	// List stale pending with past cutoff - should not find it
	stale, err = store.ListStalePending(ctx, time.Now().Add(-1*time.Hour))
	require.NoError(t, err)
	require.Len(t, stale, 0)
}

func TestPostgresStore_Update(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := NewPostgresStore(PostgresStoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Update record
	record.Attempt = 2
	record.Status = EngineStatusPending
	record.WorkerID = ""
	err = store.Update(ctx, record)
	require.NoError(t, err)

	// Verify update
	retrieved, err := store.Get(ctx, "exec-1")
	require.NoError(t, err)
	require.Equal(t, 2, retrieved.Attempt)
}
