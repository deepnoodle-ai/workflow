package workflow

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// ExecutionStore is the unified interface for execution state and task distribution.
// For schema initialization, use type assertion with SchemaMigrator.
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
}

// ExecutionFilter specifies criteria for listing executions.
type ExecutionFilter = domain.ExecutionFilter

// StoreConfig contains common configuration for store implementations.
type StoreConfig = domain.StoreConfig

// DefaultStoreConfig returns sensible defaults.
func DefaultStoreConfig() StoreConfig {
	return domain.DefaultStoreConfig()
}

// SchemaMigrator is implemented by stores that need schema initialization.
// Use type assertion to check if a store supports this:
//
//	if migrator, ok := store.(workflow.SchemaMigrator); ok {
//	    migrator.CreateSchema(ctx)
//	}
type SchemaMigrator interface {
	// CreateSchema initializes the storage schema.
	CreateSchema(ctx context.Context) error
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

// Append implements EventLog.
func (a *storeAdapter) Append(ctx context.Context, event Event) error {
	return a.store.AppendEvent(ctx, event)
}

// List implements EventLog.
func (a *storeAdapter) List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error) {
	return a.store.ListEvents(ctx, executionID, filter)
}

// Ensure storeAdapter implements EventLog and SchemaMigrator.
var _ EventLog = (*storeAdapter)(nil)
var _ SchemaMigrator = (*storeAdapter)(nil)

// unwrapStore extracts the internal engine.Store from an ExecutionStore.
// If the store is already an engine.Store, it returns it directly.
// If it's a storeAdapter, it unwraps to get the underlying store.
// This is used by the Engine facade to pass stores to the internal engine.
func unwrapStore(store ExecutionStore) engine.Store {
	// If it's our adapter, unwrap it
	if adapter, ok := store.(*storeAdapter); ok {
		return adapter.store
	}
	// If it directly implements engine.Store (e.g., memory.Store or postgres.Store)
	if internalStore, ok := store.(engine.Store); ok {
		return internalStore
	}
	// Fallback: wrap it in an adapter that implements engine.Store
	// This handles custom ExecutionStore implementations
	return &engineStoreWrapper{store: store}
}

// engineStoreWrapper adapts workflow.ExecutionStore to engine.Store for custom implementations.
type engineStoreWrapper struct {
	store ExecutionStore
}

func (w *engineStoreWrapper) CreateExecution(ctx context.Context, record *engine.ExecutionRecord) error {
	return w.store.CreateExecution(ctx, record)
}

func (w *engineStoreWrapper) GetExecution(ctx context.Context, id string) (*engine.ExecutionRecord, error) {
	return w.store.GetExecution(ctx, id)
}

func (w *engineStoreWrapper) UpdateExecution(ctx context.Context, record *engine.ExecutionRecord) error {
	return w.store.UpdateExecution(ctx, record)
}

func (w *engineStoreWrapper) ListExecutions(ctx context.Context, filter engine.ExecutionFilter) ([]*engine.ExecutionRecord, error) {
	return w.store.ListExecutions(ctx, filter)
}

func (w *engineStoreWrapper) CreateTask(ctx context.Context, t *domain.TaskRecord) error {
	return w.store.CreateTask(ctx, t)
}

func (w *engineStoreWrapper) ClaimTask(ctx context.Context, workerID string) (*domain.TaskClaimed, error) {
	return w.store.ClaimTask(ctx, workerID)
}

func (w *engineStoreWrapper) CompleteTask(ctx context.Context, taskID string, workerID string, result *domain.TaskResult) error {
	return w.store.CompleteTask(ctx, taskID, workerID, result)
}

func (w *engineStoreWrapper) ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error {
	return w.store.ReleaseTask(ctx, taskID, workerID, retryAfter)
}

func (w *engineStoreWrapper) HeartbeatTask(ctx context.Context, taskID string, workerID string) error {
	return w.store.HeartbeatTask(ctx, taskID, workerID)
}

func (w *engineStoreWrapper) GetTask(ctx context.Context, id string) (*domain.TaskRecord, error) {
	return w.store.GetTask(ctx, id)
}

func (w *engineStoreWrapper) ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*domain.TaskRecord, error) {
	return w.store.ListStaleTasks(ctx, heartbeatCutoff)
}

func (w *engineStoreWrapper) ResetTask(ctx context.Context, taskID string) error {
	return w.store.ResetTask(ctx, taskID)
}

func (w *engineStoreWrapper) AppendEvent(ctx context.Context, event engine.Event) error {
	// Check if the ExecutionStore also implements EventLog
	if eventLog, ok := w.store.(EventLog); ok {
		return eventLog.Append(ctx, event)
	}
	// No event support - this is a no-op
	return nil
}

func (w *engineStoreWrapper) ListEvents(ctx context.Context, executionID string, filter engine.EventFilter) ([]engine.Event, error) {
	// Check if the ExecutionStore also implements EventLog
	if eventLog, ok := w.store.(EventLog); ok {
		return eventLog.List(ctx, executionID, filter)
	}
	// No event support - return empty
	return nil, nil
}

func (w *engineStoreWrapper) CreateSchema(ctx context.Context) error {
	if migrator, ok := w.store.(SchemaMigrator); ok {
		return migrator.CreateSchema(ctx)
	}
	return nil
}

// Type assertions to ensure type aliases match internal types
var _ *ExecutionRecord = (*domain.ExecutionRecord)(nil)
var _ *TaskRecord = (*domain.TaskRecord)(nil)
var _ *ClaimedTask = (*domain.TaskClaimed)(nil)
var _ *TaskResult = (*domain.TaskResult)(nil)

// Verify engineStoreWrapper implements engine.Store
var _ engine.Store = (*engineStoreWrapper)(nil)
