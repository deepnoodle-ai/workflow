package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestMemoryEventLog_AppendAndList(t *testing.T) {
	ctx := context.Background()
	log := NewMemoryEventLog()

	// Append events
	now := time.Now().UTC()
	events := []Event{
		{ID: "e1", ExecutionID: "exec-1", Timestamp: now, Type: EventWorkflowStarted},
		{ID: "e2", ExecutionID: "exec-1", Timestamp: now.Add(1 * time.Second), Type: EventStepStarted, StepName: "step1"},
		{ID: "e3", ExecutionID: "exec-1", Timestamp: now.Add(2 * time.Second), Type: EventStepCompleted, StepName: "step1"},
		{ID: "e4", ExecutionID: "exec-1", Timestamp: now.Add(3 * time.Second), Type: EventWorkflowCompleted},
		{ID: "e5", ExecutionID: "exec-2", Timestamp: now, Type: EventWorkflowStarted}, // Different execution
	}

	for _, e := range events {
		err := log.Append(ctx, e)
		assert.NoError(t, err)
	}

	// List all for exec-1
	result, err := log.List(ctx, "exec-1", EventFilter{})
	assert.NoError(t, err)
	assert.Len(t, result, 4)

	// List with type filter
	result, err = log.List(ctx, "exec-1", EventFilter{
		Types: []EventType{EventStepStarted, EventStepCompleted},
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// List with time filter
	result, err = log.List(ctx, "exec-1", EventFilter{
		After: now.Add(1 * time.Second),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2) // e3 and e4

	// List with limit
	result, err = log.List(ctx, "exec-1", EventFilter{
		Limit: 2,
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestPostgresEventLog_AppendAndList(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	log := NewPostgresEventLog(PostgresEventLogOptions{DB: db})

	err := log.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append events
	now := time.Now().UTC().Truncate(time.Microsecond)
	events := []Event{
		{ID: "e1", ExecutionID: "exec-1", Timestamp: now, Type: EventWorkflowStarted},
		{ID: "e2", ExecutionID: "exec-1", Timestamp: now.Add(1 * time.Second), Type: EventStepStarted, StepName: "step1", PathID: "main"},
		{ID: "e3", ExecutionID: "exec-1", Timestamp: now.Add(2 * time.Second), Type: EventStepCompleted, StepName: "step1", Data: map[string]any{"result": "ok"}},
		{ID: "e4", ExecutionID: "exec-1", Timestamp: now.Add(3 * time.Second), Type: EventWorkflowCompleted},
		{ID: "e5", ExecutionID: "exec-2", Timestamp: now, Type: EventWorkflowStarted},
	}

	for _, e := range events {
		err := log.Append(ctx, e)
		assert.NoError(t, err)
	}

	// List all for exec-1
	result, err := log.List(ctx, "exec-1", EventFilter{})
	assert.NoError(t, err)
	assert.Len(t, result, 4)
	assert.Equal(t, result[0].ID, "e1")
	assert.Equal(t, result[3].ID, "e4")

	// List with type filter
	result, err = log.List(ctx, "exec-1", EventFilter{
		Types: []EventType{EventStepStarted, EventStepCompleted},
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// Verify event data
	result, err = log.List(ctx, "exec-1", EventFilter{
		Types: []EventType{EventStepCompleted},
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
	log := NewPostgresEventLog(PostgresEventLogOptions{DB: db})

	err := log.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append events with different timestamps
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []Event{
		{ID: "e1", ExecutionID: "exec-1", Timestamp: baseTime, Type: EventWorkflowStarted},
		{ID: "e2", ExecutionID: "exec-1", Timestamp: baseTime.Add(1 * time.Hour), Type: EventStepStarted},
		{ID: "e3", ExecutionID: "exec-1", Timestamp: baseTime.Add(2 * time.Hour), Type: EventStepCompleted},
		{ID: "e4", ExecutionID: "exec-1", Timestamp: baseTime.Add(3 * time.Hour), Type: EventWorkflowCompleted},
	}

	for _, e := range events {
		err := log.Append(ctx, e)
		assert.NoError(t, err)
	}

	// List events after a certain time
	result, err := log.List(ctx, "exec-1", EventFilter{
		After: baseTime.Add(30 * time.Minute),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 3) // e2, e3, e4

	// List events before a certain time
	result, err = log.List(ctx, "exec-1", EventFilter{
		Before: baseTime.Add(90 * time.Minute),
	})
	assert.NoError(t, err)
	assert.Len(t, result, 2) // e1, e2

	// List events in a time range
	result, err = log.List(ctx, "exec-1", EventFilter{
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
	log := NewPostgresEventLog(PostgresEventLogOptions{DB: db})

	err := log.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append multiple events
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		event := Event{
			ID:          "e" + string(rune('0'+i)),
			ExecutionID: "exec-1",
			Timestamp:   baseTime.Add(time.Duration(i) * time.Second),
			Type:        EventStepStarted,
		}
		err := log.Append(ctx, event)
		assert.NoError(t, err)
	}

	// List with limit
	result, err := log.List(ctx, "exec-1", EventFilter{Limit: 5})
	assert.NoError(t, err)
	assert.Len(t, result, 5)
}

func TestPostgresEventLog_EventWithError(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	log := NewPostgresEventLog(PostgresEventLogOptions{DB: db})

	err := log.CreateSchema(ctx)
	assert.NoError(t, err)

	// Append event with error
	event := Event{
		ID:          "e1",
		ExecutionID: "exec-1",
		Timestamp:   time.Now().UTC(),
		Type:        EventStepFailed,
		StepName:    "failing-step",
		Attempt:     3,
		Error:       "something went wrong",
	}
	err = log.Append(ctx, event)
	assert.NoError(t, err)

	// List and verify
	result, err := log.List(ctx, "exec-1", EventFilter{})
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, result[0].Error, "something went wrong")
	assert.Equal(t, result[0].Attempt, 3)
	assert.Equal(t, result[0].StepName, "failing-step")
}
