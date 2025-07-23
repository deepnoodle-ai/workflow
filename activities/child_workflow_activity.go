package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// ChildWorkflowInput defines the input parameters for the child workflow activity
type ChildWorkflowInput struct {
	WorkflowName string                 `json:"workflow_name"`
	Sync         bool                   `json:"sync"`
	Inputs       map[string]interface{} `json:"inputs"`
	Timeout      float64                `json:"timeout"`
	ParentID     string                 `json:"parent_id"`
}

// ChildWorkflowOutput defines the output of the child workflow activity
type ChildWorkflowOutput struct {
	Outputs      map[string]interface{} `json:"outputs,omitempty"`
	Status       string                 `json:"status,omitempty"`
	ExecutionID  string                 `json:"execution_id"`
	Duration     float64                `json:"duration,omitempty"`
	Success      bool                   `json:"success,omitempty"`
	Async        bool                   `json:"async,omitempty"`
	WorkflowName string                 `json:"workflow_name,omitempty"`
}

// ChildWorkflowActivity can be used to execute child workflows
type ChildWorkflowActivity struct {
	executor workflow.ChildWorkflowExecutor
}

// NewChildWorkflowActivity creates a new ChildWorkflowActivity that can be used to execute child workflows
func NewChildWorkflowActivity(executor workflow.ChildWorkflowExecutor) workflow.Activity {
	return workflow.NewTypedActivity(&ChildWorkflowActivity{
		executor: executor,
	})
}

// Name returns the activity name
func (c *ChildWorkflowActivity) Name() string {
	return "workflow.child"
}

// Execute runs the child workflow activity
func (c *ChildWorkflowActivity) Execute(ctx workflow.Context, params ChildWorkflowInput) (ChildWorkflowOutput, error) {
	// Validate workflow name (required)
	if params.WorkflowName == "" {
		return ChildWorkflowOutput{}, fmt.Errorf("child workflow activity requires 'workflow_name' parameter")
	}

	// Initialize inputs if nil
	inputs := params.Inputs
	if inputs == nil {
		inputs = make(map[string]interface{})
	}

	// Parse timeout (optional)
	timeout := time.Duration(params.Timeout) * time.Second

	// Create child workflow spec
	spec := &workflow.ChildWorkflowSpec{
		WorkflowName: params.WorkflowName,
		Inputs:       inputs,
		Timeout:      timeout,
		ParentID:     params.ParentID,
		Sync:         params.Sync,
	}

	// Execute based on sync flag
	if params.Sync {
		return c.executeSync(ctx, spec)
	} else {
		return c.executeAsync(ctx, spec)
	}
}

// executeSync runs the child workflow synchronously
func (c *ChildWorkflowActivity) executeSync(ctx context.Context, spec *workflow.ChildWorkflowSpec) (ChildWorkflowOutput, error) {
	result, err := c.executor.ExecuteSync(ctx, spec)
	if err != nil {
		return ChildWorkflowOutput{}, fmt.Errorf("child workflow execution failed: %w", err)
	}

	// For synchronous execution, we return the result
	return ChildWorkflowOutput{
		Outputs:     result.Outputs,
		Status:      string(result.Status),
		ExecutionID: result.ExecutionID,
		Duration:    result.Duration.Seconds(),
		Success:     result.Status == workflow.ExecutionStatusCompleted,
	}, nil
}

// executeAsync starts the child workflow asynchronously
func (c *ChildWorkflowActivity) executeAsync(ctx context.Context, spec *workflow.ChildWorkflowSpec) (ChildWorkflowOutput, error) {
	handle, err := c.executor.ExecuteAsync(ctx, spec)
	if err != nil {
		return ChildWorkflowOutput{}, fmt.Errorf("failed to start child workflow: %w", err)
	}

	// For asynchronous execution, we return the handle for later reference
	return ChildWorkflowOutput{
		ExecutionID:  handle.ExecutionID,
		WorkflowName: handle.WorkflowName,
		Async:        true,
	}, nil
}
