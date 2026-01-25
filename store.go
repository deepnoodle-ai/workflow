package workflow

import (
	"context"
	"database/sql"
	"time"

	"github.com/deepnoodle-ai/workflow/internal/engine"
	"github.com/deepnoodle-ai/workflow/internal/memory"
	"github.com/deepnoodle-ai/workflow/internal/postgres"
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

// NewMemoryStore creates an in-memory store for testing and development.
// The store is not durable and loses all data when the process exits.
func NewMemoryStore() ExecutionStore {
	return NewStoreAdapter(memory.NewStore())
}

// PostgresStoreOption configures a PostgreSQL store.
type PostgresStoreOption func(*postgresStoreConfig)

type postgresStoreConfig struct {
	config engine.StoreConfig
}

// WithStoreConfig sets custom store configuration.
func WithStoreConfig(config StoreConfig) PostgresStoreOption {
	return func(c *postgresStoreConfig) {
		c.config = config
	}
}

// NewPostgresStore creates a PostgreSQL-backed store for production use.
// The db connection must be opened and configured by the caller.
// Call CreateSchema() on the returned store to initialize database tables.
func NewPostgresStore(db *sql.DB, opts ...PostgresStoreOption) ExecutionStore {
	cfg := &postgresStoreConfig{
		config: engine.DefaultStoreConfig(),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return NewStoreAdapter(postgres.NewStore(postgres.StoreOptions{
		DB:     db,
		Config: cfg.config,
	}))
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

func (w *engineStoreWrapper) CreateTask(ctx context.Context, t *task.Record) error {
	return w.store.CreateTask(ctx, t)
}

func (w *engineStoreWrapper) ClaimTask(ctx context.Context, workerID string) (*task.Claimed, error) {
	return w.store.ClaimTask(ctx, workerID)
}

func (w *engineStoreWrapper) CompleteTask(ctx context.Context, taskID string, workerID string, result *task.Result) error {
	return w.store.CompleteTask(ctx, taskID, workerID, result)
}

func (w *engineStoreWrapper) ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error {
	return w.store.ReleaseTask(ctx, taskID, workerID, retryAfter)
}

func (w *engineStoreWrapper) HeartbeatTask(ctx context.Context, taskID string, workerID string) error {
	return w.store.HeartbeatTask(ctx, taskID, workerID)
}

func (w *engineStoreWrapper) GetTask(ctx context.Context, id string) (*task.Record, error) {
	return w.store.GetTask(ctx, id)
}

func (w *engineStoreWrapper) ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*task.Record, error) {
	return w.store.ListStaleTasks(ctx, heartbeatCutoff)
}

func (w *engineStoreWrapper) ResetTask(ctx context.Context, taskID string) error {
	return w.store.ResetTask(ctx, taskID)
}

func (w *engineStoreWrapper) AppendEvent(ctx context.Context, event engine.Event) error {
	// ExecutionStore doesn't have AppendEvent - this is a no-op for custom stores
	return nil
}

func (w *engineStoreWrapper) ListEvents(ctx context.Context, executionID string, filter engine.EventFilter) ([]engine.Event, error) {
	// ExecutionStore doesn't have ListEvents - return empty for custom stores
	return nil, nil
}

func (w *engineStoreWrapper) CreateSchema(ctx context.Context) error {
	return w.store.CreateSchema(ctx)
}

// Verify that internal types are compatible with our aliases
var _ *ExecutionRecord = (*engine.ExecutionRecord)(nil)
var _ *TaskRecord = (*task.Record)(nil)
var _ *ClaimedTask = (*task.Claimed)(nil)
var _ *TaskResult = (*task.Result)(nil)

// Verify engineStoreWrapper implements engine.Store
var _ engine.Store = (*engineStoreWrapper)(nil)
