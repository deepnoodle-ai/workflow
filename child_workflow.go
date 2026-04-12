package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
)

// ChildWorkflowSpec specifies how to execute a child workflow.
//
// Whether the child runs synchronously or asynchronously is selected
// at the call site by invoking ChildWorkflowExecutor.ExecuteSync or
// ExecuteAsync — there is no flag on the spec.
type ChildWorkflowSpec struct {
	WorkflowName string                 `json:"workflow_name"`
	Inputs       map[string]interface{} `json:"inputs,omitempty"`
	Timeout      time.Duration          `json:"timeout,omitempty"`
	ParentID     string                 `json:"parent_id,omitempty"` // for tracing
}

// ChildWorkflowResult represents the result of a child workflow execution.
//
// The execution error is the second return value from
// ExecuteSync/GetResult — it is not duplicated on this struct.
type ChildWorkflowResult struct {
	Outputs     map[string]interface{} `json:"outputs"`
	Status      ExecutionStatus        `json:"status"`
	ExecutionID string                 `json:"execution_id"`
	Duration    time.Duration          `json:"duration"`
}

// ChildWorkflowHandle represents an asynchronous child workflow execution
type ChildWorkflowHandle struct {
	ExecutionID  string `json:"execution_id"`
	WorkflowName string `json:"workflow_name"`
}

// ChildWorkflowExecutor manages child workflow executions
type ChildWorkflowExecutor interface {
	// ExecuteSync runs a child workflow synchronously and waits for completion
	ExecuteSync(ctx context.Context, spec *ChildWorkflowSpec) (*ChildWorkflowResult, error)

	// ExecuteAsync starts a child workflow asynchronously and returns immediately
	ExecuteAsync(ctx context.Context, spec *ChildWorkflowSpec) (*ChildWorkflowHandle, error)

	// GetResult retrieves the result of an asynchronous execution
	GetResult(ctx context.Context, handle *ChildWorkflowHandle) (*ChildWorkflowResult, error)
}

// WorkflowRegistry manages a collection of workflow definitions
type WorkflowRegistry interface {
	// Register adds a workflow to the registry
	Register(workflow *Workflow) error

	// Get retrieves a workflow by name
	Get(name string) (*Workflow, bool)

	// List returns all registered workflow names
	List() []string
}

// MemoryWorkflowRegistry implements WorkflowRegistry using in-memory storage
type MemoryWorkflowRegistry struct {
	workflows map[string]*Workflow
}

// NewMemoryWorkflowRegistry creates a new in-memory workflow registry
func NewMemoryWorkflowRegistry() *MemoryWorkflowRegistry {
	return &MemoryWorkflowRegistry{
		workflows: make(map[string]*Workflow),
	}
}

// Register adds a workflow to the registry
func (r *MemoryWorkflowRegistry) Register(workflow *Workflow) error {
	if workflow == nil {
		return fmt.Errorf("workflow cannot be nil")
	}
	if workflow.Name() == "" {
		return fmt.Errorf("workflow name cannot be empty")
	}

	r.workflows[workflow.Name()] = workflow
	return nil
}

// Get retrieves a workflow by name
func (r *MemoryWorkflowRegistry) Get(name string) (*Workflow, bool) {
	workflow, exists := r.workflows[name]
	return workflow, exists
}

// List returns all registered workflow names
func (r *MemoryWorkflowRegistry) List() []string {
	names := make([]string, 0, len(r.workflows))
	for name := range r.workflows {
		names = append(names, name)
	}
	return names
}

// defaultAsyncCleanupTimeout is the default window during which an
// asynchronously launched child workflow's result remains retrievable
// via GetResult after it completes. Consumers can override via
// ChildWorkflowExecutorOptions.CleanupTimeout.
const defaultAsyncCleanupTimeout = time.Hour

// DefaultChildWorkflowExecutor provides a basic implementation of ChildWorkflowExecutor
type DefaultChildWorkflowExecutor struct {
	workflowRegistry   WorkflowRegistry
	activities         []Activity
	logger             *slog.Logger
	activityLogger     ActivityLogger
	checkpointer       Checkpointer
	scriptCompiler     script.Compiler
	cleanupTimeout     time.Duration
	asyncExecutions    map[string]*Execution // Track async executions by ID
	asyncExecutionsMtx sync.RWMutex          // Protect concurrent access to async executions
}

// ChildWorkflowExecutorOptions configures a DefaultChildWorkflowExecutor
type ChildWorkflowExecutorOptions struct {
	WorkflowRegistry WorkflowRegistry
	Activities       []Activity
	Logger           *slog.Logger
	ActivityLogger   ActivityLogger
	Checkpointer     Checkpointer
	// ScriptCompiler is the scripting engine used by child executions.
	// When nil, child executions fall back to DefaultScriptCompiler
	// (github.com/deepnoodle-ai/expr). Set this to override with a
	// different engine.
	ScriptCompiler script.Compiler
	// CleanupTimeout is how long to retain an async child workflow's
	// result in memory after completion before evicting it from the
	// in-flight map. GetResult on an evicted handle returns an error.
	// Zero (the default) means one hour. Negative values disable
	// cleanup entirely (results are retained for the lifetime of the
	// process — useful in tests, dangerous in long-running services).
	CleanupTimeout time.Duration
}

// NewDefaultChildWorkflowExecutor creates a new DefaultChildWorkflowExecutor
func NewDefaultChildWorkflowExecutor(opts ChildWorkflowExecutorOptions) (*DefaultChildWorkflowExecutor, error) {
	if opts.WorkflowRegistry == nil {
		return nil, fmt.Errorf("workflow registry is required")
	}
	cleanup := opts.CleanupTimeout
	if cleanup == 0 {
		cleanup = defaultAsyncCleanupTimeout
	}
	return &DefaultChildWorkflowExecutor{
		workflowRegistry:   opts.WorkflowRegistry,
		activities:         opts.Activities,
		logger:             opts.Logger,
		activityLogger:     opts.ActivityLogger,
		checkpointer:       opts.Checkpointer,
		scriptCompiler:     opts.ScriptCompiler,
		cleanupTimeout:     cleanup,
		asyncExecutions:    make(map[string]*Execution),
		asyncExecutionsMtx: sync.RWMutex{},
	}, nil
}

// ExecuteSync runs a child workflow synchronously
func (e *DefaultChildWorkflowExecutor) ExecuteSync(ctx context.Context, spec *ChildWorkflowSpec) (*ChildWorkflowResult, error) {
	startTime := time.Now()

	workflow, exists := e.workflowRegistry.Get(spec.WorkflowName)
	if !exists {
		return nil, fmt.Errorf("workflow %q not found in registry", spec.WorkflowName)
	}

	execution, err := e.newChildExecution(workflow, spec)
	if err != nil {
		return nil, err
	}

	// Apply timeout if specified
	execCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	result, execErr := execution.Execute(execCtx)
	duration := time.Since(startTime)

	cwr := &ChildWorkflowResult{
		ExecutionID: execution.ID(),
		Status:      execution.Status(),
		Duration:    duration,
	}
	if result != nil {
		cwr.Outputs = make(map[string]any, len(result.Outputs))
		for k, v := range result.Outputs {
			cwr.Outputs[k] = v
		}
	}
	if execErr != nil {
		return cwr, execErr
	}
	if result != nil && result.Status == ExecutionStatusFailed {
		if result.Error != nil {
			return cwr, result.Error
		}
		return cwr, fmt.Errorf("child workflow execution failed")
	}
	return cwr, nil
}

// newChildExecution builds an Execution for a child workflow using the
// executor's shared infrastructure (activities, checkpointer, ...).
func (e *DefaultChildWorkflowExecutor) newChildExecution(wf *Workflow, spec *ChildWorkflowSpec) (*Execution, error) {
	reg := NewActivityRegistry()
	for _, a := range e.activities {
		if err := reg.Register(a); err != nil {
			return nil, fmt.Errorf("child registry: %w", err)
		}
	}
	opts := []ExecutionOption{
		WithInputs(spec.Inputs),
	}
	if e.activityLogger != nil {
		opts = append(opts, WithActivityLogger(e.activityLogger))
	}
	if e.checkpointer != nil {
		opts = append(opts, WithCheckpointer(e.checkpointer))
	}
	if e.logger != nil {
		opts = append(opts, WithLogger(e.logger))
	}
	if e.scriptCompiler != nil {
		opts = append(opts, WithScriptCompiler(e.scriptCompiler))
	}
	exec, err := NewExecution(wf, reg, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create child execution: %w", err)
	}
	return exec, nil
}

// ExecuteAsync starts a child workflow asynchronously and returns a
// handle immediately. The child runs in a detached goroutine that
// uses context.Background(), so the caller's context cancellation
// does not propagate.
//
// # Async vs. checkpoint semantics
//
// The async execution map is in-process state. It is NOT part of the
// parent execution's checkpoint:
//
//   - If the parent process restarts while an async child is running,
//     the child goroutine dies with the process. The parent's resumed
//     execution will hold a ChildWorkflowHandle that no longer
//     resolves: GetResult returns "not found".
//   - If the parent execution checkpoints and resumes in the same
//     process, the handle still resolves until cleanupTimeout elapses
//     after the child completes.
//
// In short: async children are best-effort, single-process. Workflows
// that need durable child orchestration across restarts should use
// ExecuteSync (the parent execution waits, the checkpoint captures
// the child's outputs in state) or model the child as a separate
// top-level execution coordinated via signals.
//
// TODO(v1.1): persist async-child handles to the checkpointer so that
// resumed parents can re-attach.
func (e *DefaultChildWorkflowExecutor) ExecuteAsync(ctx context.Context, spec *ChildWorkflowSpec) (*ChildWorkflowHandle, error) {
	workflow, exists := e.workflowRegistry.Get(spec.WorkflowName)
	if !exists {
		return nil, fmt.Errorf("workflow %q not found in registry", spec.WorkflowName)
	}

	execution, err := e.newChildExecution(workflow, spec)
	if err != nil {
		return nil, err
	}

	// Track the async execution
	e.asyncExecutionsMtx.Lock()
	e.asyncExecutions[execution.ID()] = execution
	e.asyncExecutionsMtx.Unlock()

	cleanup := e.cleanupTimeout

	// Start execution in a goroutine. Use context.Background() instead of
	// the caller's context so that the async child workflow is not cancelled
	// when the caller's context completes.
	go func() {
		defer func() {
			if cleanup < 0 {
				return
			}
			go func() {
				time.Sleep(cleanup)
				e.asyncExecutionsMtx.Lock()
				delete(e.asyncExecutions, execution.ID())
				e.asyncExecutionsMtx.Unlock()
			}()
		}()

		execCtx := context.Background()
		if spec.Timeout > 0 {
			var cancel context.CancelFunc
			execCtx, cancel = context.WithTimeout(execCtx, spec.Timeout)
			defer cancel()
		}

		execution.Execute(execCtx)
	}()

	return &ChildWorkflowHandle{
		ExecutionID:  execution.ID(),
		WorkflowName: spec.WorkflowName,
	}, nil
}

// GetResult retrieves the result of an asynchronous execution
func (e *DefaultChildWorkflowExecutor) GetResult(ctx context.Context, handle *ChildWorkflowHandle) (*ChildWorkflowResult, error) {
	if handle == nil {
		return nil, fmt.Errorf("handle cannot be nil")
	}

	// Look up the async execution
	e.asyncExecutionsMtx.RLock()
	execution, exists := e.asyncExecutions[handle.ExecutionID]
	e.asyncExecutionsMtx.RUnlock()

	if !exists {
		return nil, fmt.Errorf("async execution %q not found or has expired", handle.ExecutionID)
	}

	// Check execution status
	status := execution.Status()

	// For running executions, return current status without outputs
	if status == ExecutionStatusRunning || status == ExecutionStatusPending {
		return &ChildWorkflowResult{
			ExecutionID: execution.ID(),
			Status:      status,
			Duration:    0, // Duration not available until completion
			Outputs:     make(map[string]any),
		}, nil
	}

	// For completed or failed executions, extract full results
	outputs := execution.GetOutputs()
	result := &ChildWorkflowResult{
		ExecutionID: execution.ID(),
		Status:      status,
		Outputs:     make(map[string]any, len(outputs)),
	}
	for k, v := range outputs {
		result.Outputs[k] = v
	}

	if status == ExecutionStatusFailed {
		return result, fmt.Errorf("child workflow execution failed")
	}
	return result, nil
}
