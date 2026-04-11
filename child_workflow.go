package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/script"
)

// ChildWorkflowSpec specifies how to execute a child workflow
type ChildWorkflowSpec struct {
	WorkflowName string                 `json:"workflow_name"`
	Inputs       map[string]interface{} `json:"inputs,omitempty"`
	Timeout      time.Duration          `json:"timeout,omitempty"`
	ParentID     string                 `json:"parent_id,omitempty"` // for tracing
	Sync         bool                   `json:"sync"`                // synchronous vs asynchronous
}

// ChildWorkflowResult represents the result of a child workflow execution
type ChildWorkflowResult struct {
	Outputs     map[string]interface{} `json:"outputs"`
	Status      ExecutionStatus        `json:"status"`
	ExecutionID string                 `json:"execution_id"`
	Duration    time.Duration          `json:"duration"`
	Error       error                  `json:"error,omitempty"`
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

// DefaultChildWorkflowExecutor provides a basic implementation of ChildWorkflowExecutor
type DefaultChildWorkflowExecutor struct {
	workflowRegistry   WorkflowRegistry
	activities         []Activity
	logger             *slog.Logger
	activityLogger     ActivityLogger
	checkpointer       Checkpointer
	scriptCompiler     script.Compiler
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
}

// NewDefaultChildWorkflowExecutor creates a new DefaultChildWorkflowExecutor
func NewDefaultChildWorkflowExecutor(opts ChildWorkflowExecutorOptions) (*DefaultChildWorkflowExecutor, error) {
	if opts.WorkflowRegistry == nil {
		return nil, fmt.Errorf("workflow registry is required")
	}

	return &DefaultChildWorkflowExecutor{
		workflowRegistry:   opts.WorkflowRegistry,
		activities:         opts.Activities,
		logger:             opts.Logger,
		activityLogger:     opts.ActivityLogger,
		checkpointer:       opts.Checkpointer,
		scriptCompiler:     opts.ScriptCompiler,
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
		Error:       execErr,
	}
	if result != nil {
		cwr.Outputs = make(map[string]any, len(result.Outputs))
		for k, v := range result.Outputs {
			cwr.Outputs[k] = v
		}
	}
	return cwr, execErr
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

// ExecuteAsync starts a child workflow asynchronously
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

	// Start execution in a goroutine. Use context.Background() instead of
	// the caller's context so that the async child workflow is not cancelled
	// when the caller's context completes.
	go func() {
		defer func() {
			// Clean up completed execution after a delay to allow result retrieval
			go func() {
				time.Sleep(5 * time.Minute) // Keep results available for 5 minutes
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

		// Run the execution (errors will be captured in execution status)
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
			Error:       nil,
		}, nil
	}

	// For completed or failed executions, extract full results
	outputs := execution.GetOutputs()
	result := &ChildWorkflowResult{
		ExecutionID: execution.ID(),
		Status:      status,
		Outputs:     make(map[string]any),
	}

	// Copy outputs
	for k, v := range outputs {
		result.Outputs[k] = v
	}

	// Set error if execution failed
	if status == ExecutionStatusFailed {
		// We don't have direct access to execution error, so create a generic one
		result.Error = fmt.Errorf("child workflow execution failed")
	}

	return result, nil
}
