package engine

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/task"
)

// Store is the unified interface for execution state, task distribution, and events.
// Implementations are in internal/memory and internal/postgres packages.
type Store interface {
	// Execution lifecycle
	CreateExecution(ctx context.Context, record *ExecutionRecord) error
	GetExecution(ctx context.Context, id string) (*ExecutionRecord, error)
	UpdateExecution(ctx context.Context, record *ExecutionRecord) error
	ListExecutions(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error)

	// Task lifecycle
	CreateTask(ctx context.Context, t *task.Record) error
	ClaimTask(ctx context.Context, workerID string) (*task.Claimed, error)
	CompleteTask(ctx context.Context, taskID string, workerID string, result *task.Result) error
	ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error
	HeartbeatTask(ctx context.Context, taskID string, workerID string) error
	GetTask(ctx context.Context, id string) (*task.Record, error)

	// Recovery
	ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*task.Record, error)
	ResetTask(ctx context.Context, taskID string) error

	// Events
	AppendEvent(ctx context.Context, event Event) error
	ListEvents(ctx context.Context, executionID string, filter EventFilter) ([]Event, error)

	// Schema management (for implementations that need it)
	CreateSchema(ctx context.Context) error
}

// StoreConfig contains common configuration for store implementations.
type StoreConfig struct {
	// HeartbeatInterval is how often workers should heartbeat
	HeartbeatInterval time.Duration

	// LeaseTimeout is how long before a task is considered abandoned
	LeaseTimeout time.Duration

	// MaxAttempts is the maximum number of retry attempts for a task
	MaxAttempts int
}

// DefaultStoreConfig returns sensible defaults.
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		HeartbeatInterval: 30 * time.Second,
		LeaseTimeout:      2 * time.Minute,
		MaxAttempts:       3,
	}
}
