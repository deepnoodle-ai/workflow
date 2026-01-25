package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Event types for workflow observability.

// EventType represents the type of workflow event.
type EventType = engine.EventType

const (
	EventWorkflowStarted   = engine.EventWorkflowStarted
	EventWorkflowCompleted = engine.EventWorkflowCompleted
	EventWorkflowFailed    = engine.EventWorkflowFailed
	EventStepStarted       = engine.EventStepStarted
	EventStepCompleted     = engine.EventStepCompleted
	EventStepFailed        = engine.EventStepFailed
	EventStepRetrying      = engine.EventStepRetrying
	EventCheckpointSaved   = engine.EventCheckpointSaved
	EventTimerStarted      = engine.EventTimerStarted
	EventTimerFired        = engine.EventTimerFired
	EventPathForked        = engine.EventPathForked
	EventPathJoined        = engine.EventPathJoined
)

// Event represents a workflow event for observability.
type Event = engine.Event

// EventFilter specifies criteria for listing events.
type EventFilter = engine.EventFilter

// EventLog captures workflow events for observability (not recovery).
// Implementations are available in internal/memory and internal/postgres packages.
type EventLog = engine.EventLog
