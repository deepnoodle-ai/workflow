package domain

import (
	"context"
	"time"
)

// EventType identifies the type of workflow event.
type EventType string

const (
	// Workflow lifecycle events
	EventTypeWorkflowStarted   EventType = "workflow_started"
	EventTypeWorkflowCompleted EventType = "workflow_completed"
	EventTypeWorkflowFailed    EventType = "workflow_failed"

	// Step lifecycle events
	EventTypeStepStarted   EventType = "step_started"
	EventTypeStepCompleted EventType = "step_completed"
	EventTypeStepFailed    EventType = "step_failed"
	EventTypeStepRetrying  EventType = "step_retrying"

	// Timer events
	EventTypeTimerStarted EventType = "timer_started"
	EventTypeTimerFired   EventType = "timer_fired"

	// Checkpoint events
	EventTypeCheckpointSaved EventType = "checkpoint_saved"

	// Path events (for branching workflows)
	EventTypePathForked EventType = "path_forked"
	EventTypePathJoined EventType = "path_joined"
)

// Event represents a workflow event for observability.
type Event struct {
	ID          string
	ExecutionID string
	Timestamp   time.Time
	Type        EventType
	StepName    string
	PathID      string
	Attempt     int
	Data        map[string]any
	Error       string
}

// EventFilter specifies criteria for listing events.
type EventFilter struct {
	Types  []EventType
	After  time.Time
	Before time.Time
	Limit  int
}

// EventLog defines operations for persisting workflow events.
type EventLog interface {
	Append(ctx context.Context, event Event) error
	List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error)
}
