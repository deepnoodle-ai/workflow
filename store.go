package workflow

import (
	"context"
	"time"
)

// ExecutionStore is the unified interface for execution state and task distribution.
// Implementations are in internal/memory and internal/postgres packages.
type ExecutionStore interface {
	// Execution lifecycle
	CreateExecution(ctx context.Context, record *ExecutionRecord) error
	GetExecution(ctx context.Context, id string) (*ExecutionRecord, error)
	UpdateExecution(ctx context.Context, record *ExecutionRecord) error
	ListExecutions(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error)

	// Task lifecycle
	CreateTask(ctx context.Context, task *TaskRecord) error
	ClaimTask(ctx context.Context, workerID string) (*ClaimedTask, error)
	CompleteTask(ctx context.Context, taskID string, workerID string, result *TaskResult) error
	ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error
	HeartbeatTask(ctx context.Context, taskID string, workerID string) error
	GetTask(ctx context.Context, id string) (*TaskRecord, error)

	// Recovery
	ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*TaskRecord, error)
	ResetTask(ctx context.Context, taskID string) error

	// Schema management (for implementations that need it)
	CreateSchema(ctx context.Context) error
}

// ExecutionFilter specifies criteria for listing executions.
type ExecutionFilter struct {
	WorkflowName string
	Statuses     []EngineExecutionStatus
	Limit        int
	Offset       int
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
