package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestMemoryStore_Create(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{"key": "value"},
		Attempt:      1,
		CreatedAt:    time.Now(),
	}

	err := store.Create(ctx, record)
	assert.NoError(t, err)

	// Duplicate should fail
	err = store.Create(ctx, record)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestMemoryStore_Get(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Get non-existent record should fail
	_, err := store.Get(ctx, "non-existent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	// Create and get
	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Inputs:       map[string]any{"key": "value"},
		Attempt:      1,
		CreatedAt:    time.Now(),
	}
	err = store.Create(ctx, record)
	assert.NoError(t, err)

	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.ID, "exec-1")
	assert.Equal(t, retrieved.WorkflowName, "test-workflow")
	assert.Equal(t, retrieved.Status, EngineStatusPending)

	// Verify it's a copy, not the same instance
	retrieved.Status = EngineStatusRunning
	original, _ := store.Get(ctx, "exec-1")
	assert.Equal(t, original.Status, EngineStatusPending)
}

func TestMemoryStore_List(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Create multiple records
	for i := 1; i <= 5; i++ {
		status := EngineStatusPending
		if i%2 == 0 {
			status = EngineStatusCompleted
		}
		err := store.Create(ctx, &ExecutionRecord{
			ID:           "exec-" + string(rune('0'+i)),
			WorkflowName: "workflow-" + string(rune('A'+i%2)),
			Status:       status,
			Attempt:      1,
			CreatedAt:    time.Now(),
		})
		assert.NoError(t, err)
	}

	// List all
	records, err := store.List(ctx, ListFilter{})
	assert.NoError(t, err)
	assert.Len(t, records, 5)

	// Filter by status
	records, err = store.List(ctx, ListFilter{Statuses: []EngineExecutionStatus{EngineStatusPending}})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// Filter by workflow name (i=1,3,5 have i%2=1 -> B; i=2,4 have i%2=0 -> A)
	records, err = store.List(ctx, ListFilter{WorkflowName: "workflow-B"})
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// Limit
	records, err = store.List(ctx, ListFilter{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, records, 2)
}

func TestMemoryStore_ClaimExecution(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	record := &ExecutionRecord{
		ID:           "exec-1",
		WorkflowName: "test-workflow",
		Status:       EngineStatusPending,
		Attempt:      1,
		CreatedAt:    time.Now(),
	}
	err := store.Create(ctx, record)
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

	// Claim again should fail (status is now running)
	claimed, err = store.ClaimExecution(ctx, "exec-1", "worker-2", 1)
	assert.NoError(t, err)
	assert.False(t, claimed)

	// Claim with wrong attempt should fail
	store.Create(ctx, &ExecutionRecord{
		ID:       "exec-2",
		Status:   EngineStatusPending,
		Attempt:  2,
		CreatedAt: time.Now(),
	})
	claimed, err = store.ClaimExecution(ctx, "exec-2", "worker-1", 1)
	assert.NoError(t, err)
	assert.False(t, claimed)
}

func TestMemoryStore_CompleteExecution(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	record := &ExecutionRecord{
		ID:        "exec-1",
		Status:    EngineStatusPending,
		Attempt:   1,
		CreatedAt: time.Now(),
	}
	err := store.Create(ctx, record)
	assert.NoError(t, err)

	// Claim first
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

	// Complete with wrong attempt should fail
	completed, err = store.CompleteExecution(ctx, "exec-1", 2, EngineStatusFailed, nil, "error")
	assert.NoError(t, err)
	assert.False(t, completed)
}

func TestMemoryStore_Heartbeat(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	record := &ExecutionRecord{
		ID:        "exec-1",
		Status:    EngineStatusPending,
		Attempt:   1,
		CreatedAt: time.Now(),
	}
	err := store.Create(ctx, record)
	assert.NoError(t, err)

	// Claim first
	claimed, err := store.ClaimExecution(ctx, "exec-1", "worker-1", 1)
	assert.NoError(t, err)
	assert.True(t, claimed)

	// Record heartbeat time
	retrieved1, _ := store.Get(ctx, "exec-1")
	oldHeartbeat := retrieved1.LastHeartbeat

	// Sleep briefly then heartbeat
	time.Sleep(10 * time.Millisecond)
	err = store.Heartbeat(ctx, "exec-1", "worker-1")
	assert.NoError(t, err)

	// Verify heartbeat updated
	retrieved2, _ := store.Get(ctx, "exec-1")
	assert.True(t, retrieved2.LastHeartbeat.After(oldHeartbeat))

	// Wrong worker should fail
	err = store.Heartbeat(ctx, "exec-1", "worker-2")
	assert.Error(t, err)
}

func TestMemoryStore_ListStaleRunning(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	// Create some running records with old heartbeats
	now := time.Now()
	oldTime := now.Add(-5 * time.Minute)
	cutoff := now.Add(-2 * time.Minute)

	// Running with old heartbeat (stale)
	store.Create(ctx, &ExecutionRecord{
		ID:            "stale-1",
		Status:        EngineStatusRunning,
		LastHeartbeat: oldTime,
		CreatedAt:     now,
	})

	// Running with recent heartbeat (not stale)
	store.Create(ctx, &ExecutionRecord{
		ID:            "fresh-1",
		Status:        EngineStatusRunning,
		LastHeartbeat: now,
		CreatedAt:     now,
	})

	// Pending (not stale)
	store.Create(ctx, &ExecutionRecord{
		ID:        "pending-1",
		Status:    EngineStatusPending,
		CreatedAt: now,
	})

	stale, err := store.ListStaleRunning(ctx, cutoff)
	assert.NoError(t, err)
	assert.Len(t, stale, 1)
	assert.Equal(t, stale[0].ID, "stale-1")
}

func TestMemoryStore_ListStalePending(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	now := time.Now()
	oldTime := now.Add(-10 * time.Minute)
	cutoff := now.Add(-5 * time.Minute)

	// Pending with old dispatch (stale)
	store.Create(ctx, &ExecutionRecord{
		ID:           "stale-1",
		Status:       EngineStatusPending,
		DispatchedAt: oldTime,
		CreatedAt:    now,
	})

	// Pending with recent dispatch (not stale)
	store.Create(ctx, &ExecutionRecord{
		ID:           "fresh-1",
		Status:       EngineStatusPending,
		DispatchedAt: now,
		CreatedAt:    now,
	})

	// Pending without dispatch (not stale)
	store.Create(ctx, &ExecutionRecord{
		ID:        "no-dispatch",
		Status:    EngineStatusPending,
		CreatedAt: now,
	})

	stale, err := store.ListStalePending(ctx, cutoff)
	assert.NoError(t, err)
	assert.Len(t, stale, 1)
	assert.Equal(t, stale[0].ID, "stale-1")
}

func TestMemoryStore_Update(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	record := &ExecutionRecord{
		ID:        "exec-1",
		Status:    EngineStatusPending,
		Attempt:   1,
		CreatedAt: time.Now(),
	}
	err := store.Create(ctx, record)
	assert.NoError(t, err)

	// Update
	record.Status = EngineStatusRunning
	record.Attempt = 2
	err = store.Update(ctx, record)
	assert.NoError(t, err)

	// Verify
	retrieved, err := store.Get(ctx, "exec-1")
	assert.NoError(t, err)
	assert.Equal(t, retrieved.Status, EngineStatusRunning)
	assert.Equal(t, retrieved.Attempt, 2)

	// Update non-existent should fail
	err = store.Update(ctx, &ExecutionRecord{ID: "non-existent"})
	assert.Error(t, err)
}
