package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
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
		asyncExecutions:    make(map[string]*Execution),
		asyncExecutionsMtx: sync.RWMutex{},
	}, nil
}

// ExecuteSync runs a child workflow synchronously
func (e *DefaultChildWorkflowExecutor) ExecuteSync(ctx context.Context, spec *ChildWorkflowSpec) (*ChildWorkflowResult, error) {
	startTime := time.Now()

	// Get the workflow from registry
	workflow, exists := e.workflowRegistry.Get(spec.WorkflowName)
	if !exists {
		return nil, fmt.Errorf("workflow %q not found in registry", spec.WorkflowName)
	}

	// Create execution options for the child workflow
	executionOpts := ExecutionOptions{
		Workflow:       workflow,
		Inputs:         spec.Inputs,
		Activities:     e.activities,
		ActivityLogger: e.activityLogger,
		Checkpointer:   e.checkpointer,
		Logger:         e.logger,
	}

	// Create and run the child execution
	execution, err := NewExecution(executionOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create child execution: %w", err)
	}

	// Apply timeout if specified
	execCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	// Run the execution
	err = execution.Run(execCtx)
	duration := time.Since(startTime)

	// Prepare result
	result := &ChildWorkflowResult{
		ExecutionID: execution.ID(),
		Status:      execution.Status(),
		Duration:    duration,
		Error:       err,
	}

	// Extract outputs from execution state
	outputs := execution.GetOutputs()
	result.Outputs = make(map[string]interface{})
	for k, v := range outputs {
		result.Outputs[k] = v
	}

	return result, err
}

// ExecuteAsync starts a child workflow asynchronously
func (e *DefaultChildWorkflowExecutor) ExecuteAsync(ctx context.Context, spec *ChildWorkflowSpec) (*ChildWorkflowHandle, error) {
	// Get the workflow from registry
	workflow, exists := e.workflowRegistry.Get(spec.WorkflowName)
	if !exists {
		return nil, fmt.Errorf("workflow %q not found in registry", spec.WorkflowName)
	}

	// Create execution options for the child workflow
	executionOpts := ExecutionOptions{
		Workflow:       workflow,
		Inputs:         spec.Inputs,
		Activities:     e.activities,
		ActivityLogger: e.activityLogger,
		Checkpointer:   e.checkpointer,
		Logger:         e.logger,
	}

	// Create the child execution
	execution, err := NewExecution(executionOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create child execution: %w", err)
	}

	// Track the async execution
	e.asyncExecutionsMtx.Lock()
	e.asyncExecutions[execution.ID()] = execution
	e.asyncExecutionsMtx.Unlock()

	// Start execution in a goroutine
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

		execCtx := ctx
		if spec.Timeout > 0 {
			var cancel context.CancelFunc
			execCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
			defer cancel()
		}

		// Run the execution (errors will be captured in execution status)
		execution.Run(execCtx)
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
			Outputs:     make(map[string]interface{}),
			Error:       nil,
		}, nil
	}

	// For completed or failed executions, extract full results
	outputs := execution.GetOutputs()
	result := &ChildWorkflowResult{
		ExecutionID: execution.ID(),
		Status:      status,
		Outputs:     make(map[string]interface{}),
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
