package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/task"
	"github.com/google/uuid"
)

// Mode determines how the engine processes tasks.
type Mode string

const (
	// ModeLocal claims and executes tasks directly in-process.
	ModeLocal Mode = "local"

	// ModeOrchestrator only creates tasks; workers claim them externally.
	ModeOrchestrator Mode = "orchestrator"
)

// Engine manages workflow executions with durable submission and task-based execution.
// It can run in two modes:
// - Local mode: Claims and executes tasks directly
// - Orchestrator mode: Creates tasks for remote workers to claim
type Engine struct {
	store     Store
	logger    *slog.Logger
	callbacks Callbacks

	// Workflow and activity configuration
	workflowsMu sync.RWMutex
	workflows   map[string]WorkflowDefinition
	runners     map[string]task.Runner // activity name -> runner

	// Engine configuration
	workerID          string
	mode              Mode
	maxConcurrent     int
	pollInterval      time.Duration
	heartbeatInterval time.Duration
	reaperInterval    time.Duration
	heartbeatTimeout  time.Duration
	shutdownTimeout   time.Duration

	// Runtime state
	activeWg        sync.WaitGroup
	stopping        atomic.Bool
	started         atomic.Bool
	processLoopDone chan struct{}
	reaperLoopDone  chan struct{}
	cancelLoops     context.CancelFunc
}

// Options configures a new Engine.
type Options struct {
	Store     Store
	Logger    *slog.Logger
	Callbacks Callbacks

	// Workflows is a map of workflow name to workflow definition
	Workflows map[string]WorkflowDefinition

	// Runners maps activity names to their runners
	Runners map[string]task.Runner

	// Mode determines how tasks are processed
	Mode Mode

	WorkerID          string        // unique identifier for this engine instance
	MaxConcurrent     int           // max concurrent tasks (local mode only)
	PollInterval      time.Duration // how often to poll for tasks (default 1s)
	HeartbeatInterval time.Duration // default 30s
	ReaperInterval    time.Duration // default 30s
	HeartbeatTimeout  time.Duration // default 2m
	ShutdownTimeout   time.Duration // default 30s
}

// New creates a new workflow engine.
func New(opts Options) (*Engine, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if opts.WorkerID == "" {
		return nil, fmt.Errorf("worker ID is required")
	}

	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Callbacks == nil {
		opts.Callbacks = &BaseCallbacks{}
	}
	if opts.Mode == "" {
		opts.Mode = ModeLocal
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
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
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}

	return &Engine{
		store:             opts.Store,
		logger:            opts.Logger.With("component", "engine", "worker_id", opts.WorkerID),
		callbacks:         opts.Callbacks,
		workflows:         opts.Workflows,
		runners:           opts.Runners,
		workerID:          opts.WorkerID,
		mode:              opts.Mode,
		maxConcurrent:     opts.MaxConcurrent,
		pollInterval:      opts.PollInterval,
		heartbeatInterval: opts.HeartbeatInterval,
		reaperInterval:    opts.ReaperInterval,
		heartbeatTimeout:  opts.HeartbeatTimeout,
		shutdownTimeout:   opts.ShutdownTimeout,
	}, nil
}

// Start begins processing workflow executions.
func (e *Engine) Start(ctx context.Context) error {
	if !e.started.CompareAndSwap(false, true) {
		return fmt.Errorf("engine already started")
	}

	e.logger.Info("starting engine", "mode", e.mode)

	e.processLoopDone = make(chan struct{})
	e.reaperLoopDone = make(chan struct{})

	loopCtx, cancelLoops := context.WithCancel(ctx)
	e.cancelLoops = cancelLoops

	// Recover stale tasks
	if err := e.recoverStaleTasks(ctx); err != nil {
		cancelLoops()
		return fmt.Errorf("recovery failed: %w", err)
	}

	// Start reaper loop
	go func() {
		e.reaperLoop(loopCtx)
		close(e.reaperLoopDone)
	}()

	// In local mode, start task processing loop
	if e.mode == ModeLocal {
		go func() {
			e.taskProcessLoop(loopCtx)
			close(e.processLoopDone)
		}()
	} else {
		close(e.processLoopDone)
	}

	return nil
}

// Submit submits a new workflow execution.
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*ExecutionHandle, error) {
	if req.Workflow == nil {
		return nil, fmt.Errorf("workflow is required")
	}

	execID := req.ExecutionID
	if execID == "" {
		execID = "exec_" + uuid.New().String()
	}

	now := time.Now()
	record := &ExecutionRecord{
		ID:           execID,
		WorkflowName: req.Workflow.Name(),
		Status:       StatusPending,
		Inputs:       copyMapAny(req.Inputs),
		CreatedAt:    now,
	}

	if err := e.store.CreateExecution(ctx, record); err != nil {
		return nil, fmt.Errorf("create execution: %w", err)
	}

	// Register workflow
	e.workflowsMu.Lock()
	if e.workflows == nil {
		e.workflows = make(map[string]WorkflowDefinition)
	}
	e.workflows[req.Workflow.Name()] = req.Workflow
	e.workflowsMu.Unlock()

	e.callbacks.OnExecutionSubmitted(execID, req.Workflow.Name())

	// Create task for the first step
	if err := e.createNextTask(ctx, record, req.Workflow); err != nil {
		e.logger.Error("failed to create initial task", "execution_id", execID, "error", err)
	}

	return &ExecutionHandle{
		ID:     execID,
		Status: StatusPending,
	}, nil
}

// Get retrieves an execution record by ID.
func (e *Engine) Get(ctx context.Context, id string) (*ExecutionRecord, error) {
	return e.store.GetExecution(ctx, id)
}

// List retrieves execution records matching the filter.
func (e *Engine) List(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error) {
	return e.store.ListExecutions(ctx, filter)
}

// Cancel requests cancellation of an execution.
func (e *Engine) Cancel(ctx context.Context, id string) error {
	record, err := e.store.GetExecution(ctx, id)
	if err != nil {
		return err
	}

	if record.Status == StatusPending || record.Status == StatusRunning {
		record.Status = StatusCancelled
		record.LastError = "cancelled by user"
		record.CompletedAt = time.Now()
		return e.store.UpdateExecution(ctx, record)
	}

	return fmt.Errorf("execution %q is already in terminal state: %s", id, record.Status)
}

// Shutdown gracefully stops the engine.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.logger.Info("shutting down engine")
	e.stopping.Store(true)

	if e.cancelLoops != nil {
		e.cancelLoops()
	}

	// Wait for loops
	select {
	case <-e.processLoopDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case <-e.reaperLoopDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Wait for active tasks
	done := make(chan struct{})
	go func() {
		e.activeWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		e.logger.Info("shutdown complete")
		return nil
	case <-ctx.Done():
		e.logger.Warn("shutdown timeout")
		return ctx.Err()
	}
}

// createNextTask creates a task for the next step in the workflow.
func (e *Engine) createNextTask(ctx context.Context, exec *ExecutionRecord, wf WorkflowDefinition) error {
	stepList := wf.StepList()

	// Find the next step
	var nextStep StepDefinition
	for _, s := range stepList {
		step := s.(StepDefinition)
		if exec.CurrentStep == "" {
			nextStep = step
			break
		}
		// TODO: proper step sequencing based on workflow graph
	}

	if nextStep == nil {
		// No more steps - workflow is complete
		exec.Status = StatusCompleted
		exec.CompletedAt = time.Now()
		return e.store.UpdateExecution(ctx, exec)
	}

	// Get runner for this activity
	runner, ok := e.runners[nextStep.ActivityName()]
	if !ok {
		return fmt.Errorf("no runner for activity %q", nextStep.ActivityName())
	}

	// Resolve parameters
	params := make(map[string]any)
	for k, v := range nextStep.StepParameters() {
		params[k] = v // TODO: expression evaluation
	}

	// Create task spec
	spec, err := runner.ToSpec(ctx, params)
	if err != nil {
		return fmt.Errorf("create task spec: %w", err)
	}

	now := time.Now()
	taskID := fmt.Sprintf("%s_%s_1", exec.ID, nextStep.StepName())
	t := &task.Record{
		ID:           taskID,
		ExecutionID:  exec.ID,
		StepName:     nextStep.StepName(),
		ActivityName: nextStep.ActivityName(),
		Attempt:      1,
		Status:       task.StatusPending,
		Spec:         spec,
		VisibleAt:    now,
		CreatedAt:    now,
	}

	if err := e.store.CreateTask(ctx, t); err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	// Update execution
	exec.Status = StatusRunning
	exec.CurrentStep = nextStep.StepName()
	exec.StartedAt = now
	return e.store.UpdateExecution(ctx, exec)
}

// taskProcessLoop claims and processes tasks (local mode).
func (e *Engine) taskProcessLoop(ctx context.Context) {
	var sem chan struct{}
	if e.maxConcurrent > 0 {
		sem = make(chan struct{}, e.maxConcurrent)
	}

	for {
		if e.stopping.Load() {
			return
		}

		// Acquire semaphore
		if sem != nil {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}

		// Claim task
		claimed, err := e.store.ClaimTask(ctx, e.workerID)
		if err != nil {
			if sem != nil {
				<-sem
			}
			if ctx.Err() != nil {
				return
			}
			e.logger.Warn("claim task error", "error", err)
			time.Sleep(e.pollInterval)
			continue
		}

		if claimed == nil {
			if sem != nil {
				<-sem
			}
			// No work, poll again
			select {
			case <-time.After(e.pollInterval):
			case <-ctx.Done():
				return
			}
			continue
		}

		e.activeWg.Add(1)
		go func(t *task.Claimed) {
			defer e.activeWg.Done()
			defer func() {
				if sem != nil {
					<-sem
				}
			}()
			e.executeTask(ctx, t)
		}(claimed)
	}
}

// executeTask executes a task locally.
func (e *Engine) executeTask(ctx context.Context, claimed *task.Claimed) {
	e.logger.Debug("executing task", "task_id", claimed.ID, "step", claimed.StepName)

	// Start heartbeat
	hbCtx, cancelHb := context.WithCancel(ctx)
	defer cancelHb()
	go e.heartbeatLoop(hbCtx, claimed.ID)

	// Execute based on spec type
	var result *task.Result

	switch claimed.Spec.Type {
	case "inline":
		// Find runner and execute directly
		e.workflowsMu.RLock()
		runner, ok := e.runners[claimed.ActivityName]
		e.workflowsMu.RUnlock()

		if !ok {
			result = &task.Result{Success: false, Error: "no runner for step"}
		} else if executor, ok := runner.(domain.InlineExecutor); ok {
			result, _ = executor.Execute(ctx, claimed.Spec.Input)
		} else {
			result = &task.Result{Success: false, Error: "runner is not inline type"}
		}

	default:
		// For other types, we'd need an executor
		result = &task.Result{
			Success: false,
			Error:   fmt.Sprintf("unsupported task type: %s", claimed.Spec.Type),
		}
	}

	// Complete task
	if err := e.store.CompleteTask(ctx, claimed.ID, e.workerID, result); err != nil {
		e.logger.Error("failed to complete task", "task_id", claimed.ID, "error", err)
		return
	}

	// Handle task completion - advance workflow
	e.handleTaskCompletion(ctx, claimed, result)
}

// handleTaskCompletion processes a completed task and advances the workflow.
func (e *Engine) handleTaskCompletion(ctx context.Context, claimed *task.Claimed, result *task.Result) {
	exec, err := e.store.GetExecution(ctx, claimed.ExecutionID)
	if err != nil {
		e.logger.Error("failed to get execution", "execution_id", claimed.ExecutionID, "error", err)
		return
	}

	if !result.Success {
		// Task failed - fail the execution
		exec.Status = StatusFailed
		exec.LastError = result.Error
		exec.CompletedAt = time.Now()
		if err := e.store.UpdateExecution(ctx, exec); err != nil {
			e.logger.Error("failed to update execution", "execution_id", exec.ID, "error", err)
		}
		return
	}

	// Task succeeded - check for more steps
	e.workflowsMu.RLock()
	wf := e.workflows[exec.WorkflowName]
	e.workflowsMu.RUnlock()

	if wf == nil {
		e.logger.Error("workflow not found", "workflow", exec.WorkflowName)
		return
	}

	// For now, mark complete after first step
	// TODO: proper workflow graph traversal
	exec.Status = StatusCompleted
	exec.Outputs = result.Data
	exec.CompletedAt = time.Now()
	if err := e.store.UpdateExecution(ctx, exec); err != nil {
		e.logger.Error("failed to update execution", "execution_id", exec.ID, "error", err)
	}

	e.callbacks.OnExecutionCompleted(exec.ID, time.Since(exec.StartedAt), nil)
}

// heartbeatLoop sends periodic heartbeats for a task.
func (e *Engine) heartbeatLoop(ctx context.Context, taskID string) {
	ticker := time.NewTicker(e.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.store.HeartbeatTask(ctx, taskID, e.workerID); err != nil {
				e.logger.Warn("heartbeat failed", "task_id", taskID, "error", err)
			}
		}
	}
}

// reaperLoop periodically checks for stale tasks.
func (e *Engine) reaperLoop(ctx context.Context) {
	ticker := time.NewTicker(e.reaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.stopping.Load() {
				return
			}
			e.reapStaleTasks(ctx)
		}
	}
}

// reapStaleTasks finds and resets stale tasks.
func (e *Engine) reapStaleTasks(ctx context.Context) {
	cutoff := time.Now().Add(-e.heartbeatTimeout)
	staleTasks, err := e.store.ListStaleTasks(ctx, cutoff)
	if err != nil {
		e.logger.Warn("failed to list stale tasks", "error", err)
		return
	}

	for _, t := range staleTasks {
		e.logger.Info("resetting stale task", "task_id", t.ID)
		if err := e.store.ResetTask(ctx, t.ID); err != nil {
			e.logger.Warn("failed to reset task", "task_id", t.ID, "error", err)
		}
	}
}

// recoverStaleTasks recovers tasks at startup.
func (e *Engine) recoverStaleTasks(ctx context.Context) error {
	cutoff := time.Now().Add(-e.heartbeatTimeout)
	staleTasks, err := e.store.ListStaleTasks(ctx, cutoff)
	if err != nil {
		return err
	}

	if len(staleTasks) > 0 {
		e.logger.Info("recovering stale tasks", "count", len(staleTasks))
		for _, t := range staleTasks {
			if err := e.store.ResetTask(ctx, t.ID); err != nil {
				e.logger.Warn("failed to reset task", "task_id", t.ID, "error", err)
			}
		}
	}

	return nil
}

// RegisterWorkflow registers a workflow definition.
func (e *Engine) RegisterWorkflow(wf WorkflowDefinition) {
	e.workflowsMu.Lock()
	defer e.workflowsMu.Unlock()
	if e.workflows == nil {
		e.workflows = make(map[string]WorkflowDefinition)
	}
	e.workflows[wf.Name()] = wf
}

// RegisterRunner registers a runner for an activity.
func (e *Engine) RegisterRunner(activityName string, runner task.Runner) {
	e.workflowsMu.Lock()
	defer e.workflowsMu.Unlock()
	if e.runners == nil {
		e.runners = make(map[string]task.Runner)
	}
	e.runners[activityName] = runner
}

// copyMapAny creates a shallow copy of a map[string]any.
func copyMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
