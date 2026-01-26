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
// - Embedded mode: Claims and executes tasks directly in-process
// - Distributed mode: Creates tasks for remote workers to claim
type Engine struct {
	inner *engine.Engine
}

// EngineMode determines how the engine processes tasks.
type EngineMode = domain.EngineMode

const (
	// EngineModeEmbedded claims and executes tasks directly in-process.
	EngineModeEmbedded = domain.EngineModeEmbedded

	// EngineModeDistributed only creates tasks; workers claim them externally.
	EngineModeDistributed = domain.EngineModeDistributed
)

// EngineOptions configures a new Engine.
type EngineOptions struct {
	Store     domain.Store
	Logger    *slog.Logger
	Callbacks domain.Callbacks

	// Registry provides workflows and activities. If provided, workflows and runners
	// will be built from the registry. Can be used together with Workflows and Runners
	// for additional definitions.
	Registry *Registry

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
	// Build workflows map from Registry and/or explicit Workflows
	var workflows map[string]domain.WorkflowDefinition
	if opts.Registry != nil || opts.Workflows != nil {
		workflows = make(map[string]domain.WorkflowDefinition)
		// Add workflows from Registry first
		if opts.Registry != nil {
			for _, wf := range opts.Registry.Workflows() {
				workflows[wf.Name()] = wf
			}
		}
		// Add/override with explicit Workflows
		for name, wf := range opts.Workflows {
			workflows[name] = wf
		}
	}

	// Build runners map from Registry and/or explicit Runners
	runners := opts.Runners
	if opts.Registry != nil {
		if runners == nil {
			runners = make(map[string]domain.Runner)
		}
		for _, activity := range opts.Registry.Activities() {
			// Check if runner already exists (explicit Runners take precedence)
			if _, exists := runners[activity.Name()]; !exists {
				// Check if the activity provides its own Runner
				if runnable, ok := activity.(RunnableActivity); ok {
					runners[activity.Name()] = runnable.Runner()
				} else {
					// Wrap inline activities with ActivityRunner
					runners[activity.Name()] = NewActivityRunner(activity)
				}
			}
		}
	}

	inner, err := engine.New(engine.Options{
		Store:             opts.Store,
		Logger:            opts.Logger,
		Callbacks:         opts.Callbacks,
		Workflows:         workflows,
		Runners:           runners,
		Mode:              opts.Mode,
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
		Status: handle.Status,
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

// InternalEngine returns the internal engine for use by HTTP server.
// This is intended for internal use when building the server component.
func (e *Engine) InternalEngine() *engine.Engine {
	return e.inner
}
