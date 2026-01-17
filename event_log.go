package workflow

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

// MemoryEventLog implements EventLog using in-memory storage.
// Useful for testing and single-process deployments.
type MemoryEventLog struct {
	events []Event
}

// NewMemoryEventLog creates a new in-memory event log.
func NewMemoryEventLog() *MemoryEventLog {
	return &MemoryEventLog{
		events: make([]Event, 0),
	}
}

// Append adds an event to the log.
func (l *MemoryEventLog) Append(ctx context.Context, event Event) error {
	l.events = append(l.events, event)
	return nil
}

// List retrieves events for an execution matching the filter.
func (l *MemoryEventLog) List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error) {
	var result []Event
	for _, e := range l.events {
		if e.ExecutionID != executionID {
			continue
		}
		if len(filter.Types) > 0 {
			found := false
			for _, t := range filter.Types {
				if e.Type == t {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if !filter.After.IsZero() && !e.Timestamp.After(filter.After) {
			continue
		}
		if !filter.Before.IsZero() && !e.Timestamp.Before(filter.Before) {
			continue
		}
		result = append(result, e)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result, nil
}

// Verify MemoryEventLog implements EventLog.
var _ EventLog = (*MemoryEventLog)(nil)
