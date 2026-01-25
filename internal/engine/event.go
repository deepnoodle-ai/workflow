package engine

import (
	"context"
	"time"
)

// EventType represents the type of workflow event.
type EventType string

const (
	EventWorkflowStarted   EventType = "workflow_started"
	EventWorkflowCompleted EventType = "workflow_completed"
	EventWorkflowFailed    EventType = "workflow_failed"
	EventStepStarted       EventType = "step_started"
	EventStepCompleted     EventType = "step_completed"
	EventStepFailed        EventType = "step_failed"
	EventStepRetrying      EventType = "step_retrying"
	EventCheckpointSaved   EventType = "checkpoint_saved"
	EventTimerStarted      EventType = "timer_started"
	EventTimerFired        EventType = "timer_fired"
	EventPathForked        EventType = "path_forked"
	EventPathJoined        EventType = "path_joined"
)

// Event represents a workflow event for observability.
type Event struct {
	ID          string         `json:"id"`
	ExecutionID string         `json:"execution_id"`
	Timestamp   time.Time      `json:"timestamp"`
	Type        EventType      `json:"type"`
	StepName    string         `json:"step_name,omitempty"`
	PathID      string         `json:"path_id,omitempty"`
	Attempt     int            `json:"attempt,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// EventFilter specifies criteria for listing events.
type EventFilter struct {
	Types  []EventType
	After  time.Time
	Before time.Time
	Limit  int
}

// EventLog captures workflow events for observability (not recovery).
type EventLog interface {
	// Append adds an event to the log.
	Append(ctx context.Context, event Event) error

	// List retrieves events for an execution matching the filter.
	List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error)
}
