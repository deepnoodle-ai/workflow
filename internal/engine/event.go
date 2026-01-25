package engine

import (
	"context"

	"github.com/deepnoodle-ai/workflow/domain"
)

// Re-export domain event types for backward compatibility.
// New code should import directly from domain package.

// EventType represents the type of workflow event.
type EventType = domain.EventType

const (
	EventWorkflowStarted   = domain.EventTypeWorkflowStarted
	EventWorkflowCompleted = domain.EventTypeWorkflowCompleted
	EventWorkflowFailed    = domain.EventTypeWorkflowFailed
	EventStepStarted       = domain.EventTypeStepStarted
	EventStepCompleted     = domain.EventTypeStepCompleted
	EventStepFailed        = domain.EventTypeStepFailed
	EventStepRetrying      = domain.EventTypeStepRetrying
	EventCheckpointSaved   = domain.EventTypeCheckpointSaved
	EventTimerStarted      = domain.EventTypeTimerStarted
	EventTimerFired        = domain.EventTypeTimerFired
	EventPathForked        = domain.EventTypePathForked
	EventPathJoined        = domain.EventTypePathJoined
)

// Event represents a workflow event for observability.
type Event = domain.Event

// EventFilter specifies criteria for listing events.
type EventFilter = domain.EventFilter

// EventLog captures workflow events for observability (not recovery).
type EventLog interface {
	// Append adds an event to the log.
	Append(ctx context.Context, event Event) error

	// List retrieves events for an execution matching the filter.
	List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error)
}
