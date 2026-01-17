package workflow

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Engine manages workflow executions with durable submission, bounded concurrency,
// and crash recovery.
type Engine struct {
	store        ExecutionStore
	queue        WorkQueue
	env          ExecutionEnvironment
	checkpointer Checkpointer
	callbacks    EngineCallbacks
	logger       *slog.Logger

	// Workflow and activity configuration
	workflowsMu sync.RWMutex
	workflows   map[string]*Workflow
	activities  []Activity

	workerID          string
	maxConcurrent     int
	shutdownTimeout   time.Duration
	recoveryMode      RecoveryMode
	heartbeatInterval time.Duration
	reaperInterval    time.Duration
	heartbeatTimeout  time.Duration
	dispatchTimeout   time.Duration

	activeWg        sync.WaitGroup
	stopping        atomic.Bool
	started         atomic.Bool
	processLoopDone chan struct{}
	reaperLoopDone  chan struct{}
	cancelLoops     context.CancelFunc // cancels internal loops on shutdown
	execCtx         context.Context    // context for executions (cancelled on final timeout)
	cancelExecs     context.CancelFunc // cancels executions on final timeout
}

// EngineOptions configures a new Engine.
type EngineOptions struct {
	Store        ExecutionStore
	Queue        WorkQueue
	Environment  ExecutionEnvironment
	Checkpointer Checkpointer
	Callbacks    EngineCallbacks
	Logger       *slog.Logger

	// Workflows is a map of workflow name to workflow definition
	Workflows map[string]*Workflow

	// Activities is the list of activities available for workflow execution
	Activities []Activity

	WorkerID          string        // unique identifier for this engine instance
	MaxConcurrent     int           // 0 = unlimited
	ShutdownTimeout   time.Duration // default 30s
	RecoveryMode      RecoveryMode  // resume or fail
	HeartbeatInterval time.Duration // default 30s
	ReaperInterval    time.Duration // default 30s - how often reaper runs
	HeartbeatTimeout  time.Duration // default 2m - when to consider running execution stale
	DispatchTimeout   time.Duration // default 5m - when to consider dispatched-but-not-claimed stale
}

// NewEngine creates a new workflow engine.
func NewEngine(opts EngineOptions) (*Engine, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if opts.Queue == nil {
		return nil, fmt.Errorf("queue is required")
	}
	if opts.Environment == nil {
		return nil, fmt.Errorf("environment is required")
	}
	if opts.WorkerID == "" {
		return nil, fmt.Errorf("worker ID is required")
	}

	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Checkpointer == nil {
		opts.Checkpointer = NewNullCheckpointer()
	}
	if opts.Callbacks == nil {
		opts.Callbacks = &BaseEngineCallbacks{}
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}
	if opts.RecoveryMode == "" {
		opts.RecoveryMode = RecoveryResume
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = 30 * time.Second
	}
	if opts.ReaperInterval == 0 {
		opts.ReaperInterval = 30 * time.Second
	}
	if opts.HeartbeatTimeout == 0 {
		opts.HeartbeatTimeout = 2 * time.Minute
	}
	if opts.DispatchTimeout == 0 {
		opts.DispatchTimeout = 5 * time.Minute
	}

	return &Engine{
		store:             opts.Store,
		queue:             opts.Queue,
		env:               opts.Environment,
		checkpointer:      opts.Checkpointer,
		callbacks:         opts.Callbacks,
		logger:            opts.Logger.With("component", "engine", "worker_id", opts.WorkerID),
		workflows:         opts.Workflows,
		activities:        opts.Activities,
		workerID:          opts.WorkerID,
		maxConcurrent:     opts.MaxConcurrent,
		shutdownTimeout:   opts.ShutdownTimeout,
		recoveryMode:      opts.RecoveryMode,
		heartbeatInterval: opts.HeartbeatInterval,
		reaperInterval:    opts.ReaperInterval,
		heartbeatTimeout:  opts.HeartbeatTimeout,
		dispatchTimeout:   opts.DispatchTimeout,
	}, nil
}

// Start begins processing workflow executions.
func (e *Engine) Start(ctx context.Context) error {
	if !e.started.CompareAndSwap(false, true) {
		return fmt.Errorf("engine already started")
	}

	e.logger.Info("starting engine",
		"max_concurrent", e.maxConcurrent,
		"recovery_mode", e.recoveryMode)

	// Initialize done channels
	e.processLoopDone = make(chan struct{})
	e.reaperLoopDone = make(chan struct{})

	// Create internal context that we can cancel on shutdown (for loops only)
	loopCtx, cancelLoops := context.WithCancel(ctx)
	e.cancelLoops = cancelLoops

	// Create execution context (separate from loop context, cancelled on final timeout)
	execCtx, cancelExecs := context.WithCancel(ctx)
	e.execCtx = execCtx
	e.cancelExecs = cancelExecs

	// Recover orphaned executions before starting processing
	if err := e.recoverOrphaned(ctx); err != nil {
		cancelLoops()
		cancelExecs()
		return fmt.Errorf("recovery failed: %w", err)
	}

	// Start the reaper loop
	go func() {
		e.reaperLoop(loopCtx)
		close(e.reaperLoopDone)
	}()

	// Start the processing loop
	go func() {
		e.processLoop(loopCtx)
		close(e.processLoopDone)
	}()

	return nil
}

// Submit submits a new workflow execution. The execution record is persisted
// before returning, then enqueued for background processing.
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*ExecutionHandle, error) {
	if req.Workflow == nil {
		return nil, fmt.Errorf("workflow is required")
	}

	// Generate execution ID if not provided
	execID := req.ExecutionID
	if execID == "" {
		execID = NewExecutionID()
	}

	// Create execution record
	record := &ExecutionRecord{
		ID:           execID,
		WorkflowName: req.Workflow.Name(),
		Status:       EngineStatusPending,
		Inputs:       copyMapAny(req.Inputs),
		Attempt:      1,
		CreatedAt:    time.Now(),
	}

	// Persist record before acknowledging submit
	if err := e.store.Create(ctx, record); err != nil {
		return nil, fmt.Errorf("failed to create execution record: %w", err)
	}

	// Register workflow if not already registered
	e.workflowsMu.Lock()
	if e.workflows == nil {
		e.workflows = make(map[string]*Workflow)
	}
	e.workflows[req.Workflow.Name()] = req.Workflow
	e.workflowsMu.Unlock()

	// Invoke callback
	e.callbacks.OnExecutionSubmitted(execID, req.Workflow.Name())

	// Enqueue for processing
	if err := e.queue.Enqueue(ctx, WorkItem{ExecutionID: execID}); err != nil {
		// Record is persisted but not enqueued. It will be recovered on startup.
		e.logger.Warn("failed to enqueue execution, will recover on restart",
			"id", execID, "error", err)
	}

	return &ExecutionHandle{
		ID:     execID,
		Status: EngineStatusPending,
	}, nil
}

// Get retrieves an execution record by ID.
func (e *Engine) Get(ctx context.Context, id string) (*ExecutionRecord, error) {
	return e.store.Get(ctx, id)
}

// List retrieves execution records matching the filter.
func (e *Engine) List(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error) {
	return e.store.List(ctx, filter)
}

// Cancel requests cancellation of an execution.
func (e *Engine) Cancel(ctx context.Context, id string) error {
	record, err := e.store.Get(ctx, id)
	if err != nil {
		return err
	}

	// Only pending executions can be cancelled directly
	if record.Status == EngineStatusPending {
		record.Status = EngineStatusCancelled
		record.LastError = "cancelled by user"
		record.CompletedAt = time.Now()
		return e.store.Update(ctx, record)
	}

	// Running executions would need context cancellation (not implemented yet)
	if record.Status == EngineStatusRunning {
		return fmt.Errorf("cancellation of running executions not yet supported")
	}

	return fmt.Errorf("execution %q is already in terminal state: %s", id, record.Status)
}

// Shutdown gracefully stops the engine, waiting for in-flight executions to complete.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.logger.Info("shutting down engine")

	// Signal processing loop and reaper loop to stop
	e.stopping.Store(true)

	// Cancel internal loops context
	if e.cancelLoops != nil {
		e.cancelLoops()
	}

	// Close the queue to unblock Dequeue and stop new work
	if err := e.queue.Close(); err != nil {
		e.logger.Warn("error closing queue", "error", err)
	}

	// Wait for the processing loop to exit first (prevents Add/Wait race)
	if e.processLoopDone != nil {
		select {
		case <-e.processLoopDone:
		case <-ctx.Done():
			e.logger.Warn("shutdown timeout waiting for process loop")
			return ctx.Err()
		}
	}

	// Wait for the reaper loop to exit
	if e.reaperLoopDone != nil {
		select {
		case <-e.reaperLoopDone:
		case <-ctx.Done():
			e.logger.Warn("shutdown timeout waiting for reaper loop")
			return ctx.Err()
		}
	}

	// Wait for in-flight executions
	done := make(chan struct{})
	go func() {
		e.activeWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		e.logger.Info("all executions completed")
		// Cancel execution context (cleanup)
		if e.cancelExecs != nil {
			e.cancelExecs()
		}
		return nil
	case <-ctx.Done():
		e.logger.Warn("shutdown timeout, executions may be interrupted")
		// Cancel execution context to interrupt in-flight executions
		if e.cancelExecs != nil {
			e.cancelExecs()
		}
		return ctx.Err()
	}
}
