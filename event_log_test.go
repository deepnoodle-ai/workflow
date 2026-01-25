package workflow_test

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/memory"
	"github.com/deepnoodle-ai/workflow/internal/postgres"
)

func TestMemoryEventLog_AppendAndList(t *testing.T) {
	ctx := context.Background()
	store := memory.NewStore()

	// Append events
	now := time.Now().UTC()
	events := []domain.Event{
		{ID: "e1", ExecutionID: "exec-1", Timestamp: now, Type: domain.EventTypeWorkflowStarted},
		{ID: "e2", ExecutionID: "exec-1", Timestamp: now.Add(1 * time.Second), Type: domain.EventTypeStepStarted, StepName: "step1"},
		{ID: "e3", ExecutionID: "exec-1", Timestamp: now.Add(2 * time.Second), Type: domain.EventTypeStepCompleted, StepName: "step1"},
		{ID: "e4", ExecutionID: "exec-1", Timestamp: now.Add(3 * time.Second), Type: domain.EventTypeWorkflowCompleted},
		{ID: "e5", ExecutionID: "exec-2", Timestamp: now, Type: domain.EventTypeWorkflowStarted}, // Different execution
	}

	for _, e := range events {
		err := store.Append(ctx, e)
		assert.NoError(t, err)
	}

	// List all for exec-1
	result, err := store.List(ctx, "exec-1", domain.EventFilter{})
	assert.NoError(t, err)
	assert.Len(t, result, 4)

	// List with type filter
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		Types: []domain.EventType{domain.EventTypeStepStarted, domain.EventTypeStepCompleted},
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// List with time filter
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		After: now.Add(1 * time.Second),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2) // e3 and e4

	// List with limit
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		Limit: 2,
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestPostgresEventLog_AppendAndList(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := postgres.NewStore(postgres.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append events
	now := time.Now().UTC().Truncate(time.Microsecond)
	events := []domain.Event{
		{ID: "e1", ExecutionID: "exec-1", Timestamp: now, Type: domain.EventTypeWorkflowStarted},
		{ID: "e2", ExecutionID: "exec-1", Timestamp: now.Add(1 * time.Second), Type: domain.EventTypeStepStarted, StepName: "step1", PathID: "main"},
		{ID: "e3", ExecutionID: "exec-1", Timestamp: now.Add(2 * time.Second), Type: domain.EventTypeStepCompleted, StepName: "step1", Data: map[string]any{"result": "ok"}},
		{ID: "e4", ExecutionID: "exec-1", Timestamp: now.Add(3 * time.Second), Type: domain.EventTypeWorkflowCompleted},
		{ID: "e5", ExecutionID: "exec-2", Timestamp: now, Type: domain.EventTypeWorkflowStarted},
	}

	for _, e := range events {
		err := store.Append(ctx, e)
		assert.NoError(t, err)
	}

	// List all for exec-1
	result, err := store.List(ctx, "exec-1", domain.EventFilter{})
	assert.NoError(t, err)
	assert.Len(t, result, 4)
	assert.Equal(t, result[0].ID, "e1")
	assert.Equal(t, result[3].ID, "e4")

	// List with type filter
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		Types: []domain.EventType{domain.EventTypeStepStarted, domain.EventTypeStepCompleted},
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// Verify event data
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		Types: []domain.EventType{domain.EventTypeStepCompleted},
	})
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, result[0].Data["result"], "ok")
	assert.Equal(t, result[0].StepName, "step1")
}

func TestPostgresEventLog_ListWithTimeFilter(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := postgres.NewStore(postgres.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append events with different timestamps
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []domain.Event{
		{ID: "e1", ExecutionID: "exec-1", Timestamp: baseTime, Type: domain.EventTypeWorkflowStarted},
		{ID: "e2", ExecutionID: "exec-1", Timestamp: baseTime.Add(1 * time.Hour), Type: domain.EventTypeStepStarted},
		{ID: "e3", ExecutionID: "exec-1", Timestamp: baseTime.Add(2 * time.Hour), Type: domain.EventTypeStepCompleted},
		{ID: "e4", ExecutionID: "exec-1", Timestamp: baseTime.Add(3 * time.Hour), Type: domain.EventTypeWorkflowCompleted},
	}

	for _, e := range events {
		err := store.Append(ctx, e)
		assert.NoError(t, err)
	}

	// List events after a certain time
	result, err := store.List(ctx, "exec-1", domain.EventFilter{
		After: baseTime.Add(30 * time.Minute),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 3) // e2, e3, e4

	// List events before a certain time
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		Before: baseTime.Add(90 * time.Minute),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2) // e1, e2

	// List events in a time range
	result, err = store.List(ctx, "exec-1", domain.EventFilter{
		After:  baseTime.Add(30 * time.Minute),
		Before: baseTime.Add(150 * time.Minute),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2) // e2, e3
}

func TestPostgresEventLog_ListWithLimit(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := postgres.NewStore(postgres.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append multiple events
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		event := domain.Event{
			ID:          "e" + string(rune('0'+i)),
			ExecutionID: "exec-1",
			Timestamp:   baseTime.Add(time.Duration(i) * time.Second),
			Type:        domain.EventTypeStepStarted,
		}
		err := store.Append(ctx, event)
		assert.NoError(t, err)
	}

	// List with limit
	result, err := store.List(ctx, "exec-1", domain.EventFilter{Limit: 5})
	assert.NoError(t, err)
	assert.Len(t, result, 5)
}

func TestPostgresEventLog_EventWithError(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	store := postgres.NewStore(postgres.StoreOptions{DB: db})

	err := store.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append event with error
	event := domain.Event{
		ID:          "e1",
		ExecutionID: "exec-1",
		Timestamp:   time.Now().UTC(),
		Type:        domain.EventTypeStepFailed,
		StepName:    "failing-step",
		Attempt:     3,
		Error:       "something went wrong",
	}
	err = store.Append(ctx, event)
	assert.NoError(t, err)

	// List and verify
	result, err := store.List(ctx, "exec-1", domain.EventFilter{})
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, result[0].Error, "something went wrong")
	assert.Equal(t, result[0].Attempt, 3)
	assert.Equal(t, result[0].StepName, "failing-step")
}
