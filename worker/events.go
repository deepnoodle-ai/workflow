package worker

import (
	"context"
	"time"
)

// Event is a lifecycle event emitted by the worker during run
// execution. Events are append-only and intended for real-time
// streaming (SSE) and observability.
type Event struct {
	// Seq is a store-assigned sequence number for cursor-based
	// pagination. Zero on input to AppendEvent; set by the store.
	Seq int64

	RunID     string
	EventType string // "running", "completed", "failed", "suspended", "review"
	Attempt   int
	WorkerID  string
	StepName  string
	Payload   map[string]any
	CreatedAt time.Time
}

// EventStore persists and retrieves lifecycle events for runs.
type EventStore interface {
	// AppendEvent records an event. The store assigns Seq.
	AppendEvent(ctx context.Context, event *Event) error

	// ListEvents returns events for a run with Seq > afterSeq,
	// ordered by Seq ascending.
	ListEvents(ctx context.Context, runID string, afterSeq int64) ([]*Event, error)

	// CleanupEvents deletes events older than the given time.
	// Returns the number of events deleted.
	CleanupEvents(ctx context.Context, olderThan time.Time) (int, error)
}
