package workflow

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/engine"
	"github.com/deepnoodle-ai/workflow/internal/task"
)

// ExecutionStore is the unified interface for execution state and task distribution.
// This is re-exported from internal/engine for backwards compatibility.
// New code should use internal/engine.Store directly.
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
type ExecutionFilter = engine.ExecutionFilter

// StoreConfig contains common configuration for store implementations.
type StoreConfig = engine.StoreConfig

// DefaultStoreConfig returns sensible defaults.
func DefaultStoreConfig() StoreConfig {
	return engine.DefaultStoreConfig()
}

// storeAdapter adapts internal/engine.Store to workflow.ExecutionStore.
// This handles the type aliasing between the two interfaces.
type storeAdapter struct {
	store engine.Store
}

// NewStoreAdapter wraps an engine.Store to implement workflow.ExecutionStore.
func NewStoreAdapter(store engine.Store) ExecutionStore {
	return &storeAdapter{store: store}
}

func (a *storeAdapter) CreateExecution(ctx context.Context, record *ExecutionRecord) error {
	return a.store.CreateExecution(ctx, record)
}

func (a *storeAdapter) GetExecution(ctx context.Context, id string) (*ExecutionRecord, error) {
	return a.store.GetExecution(ctx, id)
}

func (a *storeAdapter) UpdateExecution(ctx context.Context, record *ExecutionRecord) error {
	return a.store.UpdateExecution(ctx, record)
}

func (a *storeAdapter) ListExecutions(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error) {
	return a.store.ListExecutions(ctx, filter)
}

func (a *storeAdapter) CreateTask(ctx context.Context, t *TaskRecord) error {
	return a.store.CreateTask(ctx, t)
}

func (a *storeAdapter) ClaimTask(ctx context.Context, workerID string) (*ClaimedTask, error) {
	return a.store.ClaimTask(ctx, workerID)
}

func (a *storeAdapter) CompleteTask(ctx context.Context, taskID string, workerID string, result *TaskResult) error {
	return a.store.CompleteTask(ctx, taskID, workerID, result)
}

func (a *storeAdapter) ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error {
	return a.store.ReleaseTask(ctx, taskID, workerID, retryAfter)
}

func (a *storeAdapter) HeartbeatTask(ctx context.Context, taskID string, workerID string) error {
	return a.store.HeartbeatTask(ctx, taskID, workerID)
}

func (a *storeAdapter) GetTask(ctx context.Context, id string) (*TaskRecord, error) {
	return a.store.GetTask(ctx, id)
}

func (a *storeAdapter) ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*TaskRecord, error) {
	return a.store.ListStaleTasks(ctx, heartbeatCutoff)
}

func (a *storeAdapter) ResetTask(ctx context.Context, taskID string) error {
	return a.store.ResetTask(ctx, taskID)
}

func (a *storeAdapter) CreateSchema(ctx context.Context) error {
	return a.store.CreateSchema(ctx)
}

// Verify that internal types are compatible with our aliases
var _ *ExecutionRecord = (*engine.ExecutionRecord)(nil)
var _ *TaskRecord = (*task.Record)(nil)
var _ *ClaimedTask = (*task.Claimed)(nil)
var _ *TaskResult = (*task.Result)(nil)
