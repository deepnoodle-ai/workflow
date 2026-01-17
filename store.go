package workflow

import (
	"context"
	"time"
)

// ExecutionStore is the source of truth for execution ownership and state.
type ExecutionStore interface {
	// Create persists a new execution record.
	Create(ctx context.Context, record *ExecutionRecord) error

	// Get retrieves an execution record by ID.
	Get(ctx context.Context, id string) (*ExecutionRecord, error)

	// List retrieves execution records matching the filter.
	List(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error)

	// ClaimExecution atomically updates status from pending to running if the
	// current attempt matches. Returns false if status is not pending or attempt
	// doesn't match. This provides distributed fencing.
	ClaimExecution(ctx context.Context, id string, workerID string, expectedAttempt int) (bool, error)

	// CompleteExecution atomically updates to completed/failed status if the
	// attempt matches. Returns false if attempt doesn't match (stale worker).
	CompleteExecution(ctx context.Context, id string, expectedAttempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) (bool, error)

	// MarkDispatched sets dispatched_at timestamp for dispatch mode tracking.
	MarkDispatched(ctx context.Context, id string, attempt int) error

	// Heartbeat updates the last_heartbeat timestamp for liveness tracking.
	Heartbeat(ctx context.Context, id string, workerID string) error

	// ListStaleRunning returns executions in running state with heartbeat older than cutoff.
	ListStaleRunning(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error)

	// ListStalePending returns executions in pending state with dispatched_at older than cutoff
	// (for dispatch mode where worker never claimed).
	ListStalePending(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error)

	// Update updates an execution record. Used for recovery to reset status and increment attempt.
	Update(ctx context.Context, record *ExecutionRecord) error
}

// ListFilter specifies criteria for listing executions.
type ListFilter struct {
	WorkflowName string
	Statuses     []EngineExecutionStatus
	Limit        int
	Offset       int
}
