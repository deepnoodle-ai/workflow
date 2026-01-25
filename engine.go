package workflow

import (
	"context"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Engine manages workflow executions with durable submission and task-based execution.
// It can run in two modes:
// - Local mode: Claims and executes tasks directly
// - Orchestrator mode: Creates tasks for remote workers to claim
type Engine struct {
	inner *engine.Engine
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
	Store     domain.Store
	Logger    *slog.Logger
	Callbacks domain.Callbacks

	// Workflows is a map of workflow name to workflow definition
	Workflows map[string]*Workflow

	// Runners maps activity names to their runners
	Runners map[string]domain.Runner

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
	// Convert workflows map
	var workflows map[string]domain.WorkflowDefinition
	if opts.Workflows != nil {
		workflows = make(map[string]domain.WorkflowDefinition, len(opts.Workflows))
		for name, wf := range opts.Workflows {
			workflows[name] = wf
		}
	}

	// Convert mode
	mode := engine.ModeLocal
	if opts.Mode == EngineModeOrchestrator {
		mode = engine.ModeOrchestrator
	}

	inner, err := engine.New(engine.Options{
		Store:             opts.Store,
		Logger:            opts.Logger,
		Callbacks:         opts.Callbacks,
		Workflows:         workflows,
		Runners:           opts.Runners,
		Mode:              mode,
		WorkerID:          opts.WorkerID,
		MaxConcurrent:     opts.MaxConcurrent,
		PollInterval:      opts.PollInterval,
		HeartbeatInterval: opts.HeartbeatInterval,
		ReaperInterval:    opts.ReaperInterval,
		HeartbeatTimeout:  opts.HeartbeatTimeout,
		ShutdownTimeout:   opts.ShutdownTimeout,
	})
	if err != nil {
		return nil, err
	}

	return &Engine{inner: inner}, nil
}

// Start begins processing workflow executions.
func (e *Engine) Start(ctx context.Context) error {
	return e.inner.Start(ctx)
}

// Submit submits a new workflow execution.
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*ExecutionHandle, error) {
	innerReq := engine.SubmitRequest{
		Workflow:    req.Workflow,
		Inputs:      req.Inputs,
		ExecutionID: req.ExecutionID,
	}
	handle, err := e.inner.Submit(ctx, innerReq)
	if err != nil {
		return nil, err
	}
	return &ExecutionHandle{
		ID:     handle.ID,
		Status: EngineExecutionStatus(handle.Status),
	}, nil
}

// Get retrieves an execution record by ID.
func (e *Engine) Get(ctx context.Context, id string) (*ExecutionRecord, error) {
	return e.inner.Get(ctx, id)
}

// List retrieves execution records matching the filter.
func (e *Engine) List(ctx context.Context, filter domain.ExecutionFilter) ([]*ExecutionRecord, error) {
	return e.inner.List(ctx, filter)
}

// Cancel requests cancellation of an execution.
func (e *Engine) Cancel(ctx context.Context, id string) error {
	return e.inner.Cancel(ctx, id)
}

// Shutdown gracefully stops the engine.
func (e *Engine) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

// RegisterWorkflow registers a workflow definition.
func (e *Engine) RegisterWorkflow(wf *Workflow) {
	e.inner.RegisterWorkflow(wf)
}

// RegisterRunner registers a runner for an activity.
func (e *Engine) RegisterRunner(activityName string, runner domain.Runner) {
	e.inner.RegisterRunner(activityName, runner)
}
