package workflow

import "github.com/deepnoodle-ai/workflow/domain"

// Event types for workflow observability.

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
// Implementations are available in internal/memory and internal/postgres packages.
type EventLog = domain.EventLog
