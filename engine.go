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

// Engine manages workflow executions with durable submission and task-based execution.
// It can run in two modes:
// - Local mode: Claims and executes tasks directly
// - Orchestrator mode: Creates tasks for remote workers to claim
type Engine struct {
	store     ExecutionStore
	logger    *slog.Logger
	callbacks EngineCallbacks

	// Workflow and activity configuration
	workflowsMu sync.RWMutex
	workflows   map[string]*Workflow
	runners     map[string]Runner // activity name -> runner

	// Engine configuration
	workerID          string
	mode              EngineMode
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

// EngineMode determines how the engine processes tasks.
type EngineMode string

const (
	// EngineModeLocal claims and executes tasks directly in-process.
	EngineModeLocal EngineMode = "local"

	// EngineModeOrchestrator only creates tasks; workers claim them externally.
	EngineModeOrchestrator EngineMode = "orchestrator"
)

// EngineOptions configures a new Engine.
type EngineOptions struct {
	Store     ExecutionStore
	Logger    *slog.Logger
	Callbacks EngineCallbacks

	// Workflows is a map of workflow name to workflow definition
	Workflows map[string]*Workflow

	// Runners maps activity names to their runners
	Runners map[string]Runner

	// Mode determines how tasks are processed
	Mode EngineMode

	WorkerID          string        // unique identifier for this engine instance
	MaxConcurrent     int           // max concurrent tasks (local mode only)
	PollInterval      time.Duration // how often to poll for tasks (default 1s)
	HeartbeatInterval time.Duration // default 30s
	ReaperInterval    time.Duration // default 30s
	HeartbeatTimeout  time.Duration // default 2m
	ShutdownTimeout   time.Duration // default 30s
}

// NewEngine creates a new workflow engine.
func NewEngine(opts EngineOptions) (*Engine, error) {
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
		opts.Callbacks = &BaseEngineCallbacks{}
	}
	if opts.Mode == "" {
		opts.Mode = EngineModeLocal
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
	if e.mode == EngineModeLocal {
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
		execID = NewExecutionID()
	}

	now := time.Now()
	record := &ExecutionRecord{
		ID:           execID,
		WorkflowName: req.Workflow.Name(),
		Status:       EngineStatusPending,
		Inputs:       copyMapAny(req.Inputs),
		CreatedAt:    now,
	}

	if err := e.store.CreateExecution(ctx, record); err != nil {
		return nil, fmt.Errorf("create execution: %w", err)
	}

	// Register workflow
	e.workflowsMu.Lock()
	if e.workflows == nil {
		e.workflows = make(map[string]*Workflow)
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
		Status: EngineStatusPending,
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

	if record.Status == EngineStatusPending || record.Status == EngineStatusRunning {
		record.Status = EngineStatusCancelled
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
func (e *Engine) createNextTask(ctx context.Context, exec *ExecutionRecord, wf *Workflow) error {
	// Find the entry step (first step with no dependencies)
	var nextStep *Step
	for _, step := range wf.steps {
		// For now, just pick the first step or the one after currentStep
		if exec.CurrentStep == "" {
			nextStep = step
			break
		}
		// TODO: proper step sequencing based on workflow graph
	}

	if nextStep == nil {
		// No more steps - workflow is complete
		exec.Status = EngineStatusCompleted
		exec.CompletedAt = time.Now()
		return e.store.UpdateExecution(ctx, exec)
	}

	// Get runner for this activity
	runner, ok := e.runners[nextStep.Activity]
	if !ok {
		return fmt.Errorf("no runner for activity %q", nextStep.Activity)
	}

	// Resolve parameters
	params := make(map[string]any)
	for k, v := range nextStep.Parameters {
		params[k] = v // TODO: expression evaluation
	}

	// Create task spec
	spec, err := runner.ToSpec(ctx, params)
	if err != nil {
		return fmt.Errorf("create task spec: %w", err)
	}

	now := time.Now()
	taskID := fmt.Sprintf("%s_%s_1", exec.ID, nextStep.Name)
	task := &TaskRecord{
		ID:           taskID,
		ExecutionID:  exec.ID,
		StepName:     nextStep.Name,
		ActivityName: nextStep.Activity,
		Attempt:      1,
		Status:       TaskStatusPending,
		Spec:         spec,
		VisibleAt:    now,
		CreatedAt:    now,
	}

	if err := e.store.CreateTask(ctx, task); err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	// Update execution
	exec.Status = EngineStatusRunning
	exec.CurrentStep = nextStep.Name
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
		task, err := e.store.ClaimTask(ctx, e.workerID)
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

		if task == nil {
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
		go func(task *ClaimedTask) {
			defer e.activeWg.Done()
			defer func() {
				if sem != nil {
					<-sem
				}
			}()
			e.executeTask(ctx, task)
		}(task)
	}
}

// executeTask executes a task locally.
func (e *Engine) executeTask(ctx context.Context, task *ClaimedTask) {
	e.logger.Debug("executing task", "task_id", task.ID, "step", task.StepName)

	// Start heartbeat
	hbCtx, cancelHb := context.WithCancel(ctx)
	defer cancelHb()
	go e.heartbeatLoop(hbCtx, task.ID)

	// Execute based on spec type
	var result *TaskResult

	switch task.Spec.Type {
	case "inline":
		// Find runner and execute directly
		e.workflowsMu.RLock()
		runner, ok := e.runners[task.ActivityName]
		e.workflowsMu.RUnlock()

		if !ok {
			result = &TaskResult{Success: false, Error: "no runner for step"}
		} else if inlineRunner, ok := runner.(*InlineRunner); ok {
			result, _ = inlineRunner.Execute(ctx, task.Spec.Input)
		} else {
			result = &TaskResult{Success: false, Error: "runner is not inline type"}
		}

	default:
		// For other types, we'd need an executor
		result = &TaskResult{
			Success: false,
			Error:   fmt.Sprintf("unsupported task type: %s", task.Spec.Type),
		}
	}

	// Complete task
	if err := e.store.CompleteTask(ctx, task.ID, e.workerID, result); err != nil {
		e.logger.Error("failed to complete task", "task_id", task.ID, "error", err)
		return
	}

	// Handle task completion - advance workflow
	e.handleTaskCompletion(ctx, task, result)
}

// handleTaskCompletion processes a completed task and advances the workflow.
func (e *Engine) handleTaskCompletion(ctx context.Context, task *ClaimedTask, result *TaskResult) {
	exec, err := e.store.GetExecution(ctx, task.ExecutionID)
	if err != nil {
		e.logger.Error("failed to get execution", "execution_id", task.ExecutionID, "error", err)
		return
	}

	if !result.Success {
		// Task failed - fail the execution
		exec.Status = EngineStatusFailed
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
	exec.Status = EngineStatusCompleted
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

	for _, task := range staleTasks {
		e.logger.Info("resetting stale task", "task_id", task.ID)
		if err := e.store.ResetTask(ctx, task.ID); err != nil {
			e.logger.Warn("failed to reset task", "task_id", task.ID, "error", err)
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
		for _, task := range staleTasks {
			if err := e.store.ResetTask(ctx, task.ID); err != nil {
				e.logger.Warn("failed to reset task", "task_id", task.ID, "error", err)
			}
		}
	}

	return nil
}

// RegisterWorkflow registers a workflow definition.
func (e *Engine) RegisterWorkflow(wf *Workflow) {
	e.workflowsMu.Lock()
	defer e.workflowsMu.Unlock()
	if e.workflows == nil {
		e.workflows = make(map[string]*Workflow)
	}
	e.workflows[wf.Name()] = wf
}

// RegisterRunner registers a runner for an activity.
func (e *Engine) RegisterRunner(activityName string, runner Runner) {
	e.workflowsMu.Lock()
	defer e.workflowsMu.Unlock()
	if e.runners == nil {
		e.runners = make(map[string]Runner)
	}
	e.runners[activityName] = runner
}
