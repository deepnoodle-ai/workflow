package activities

import (
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// ChildWorkflowInput defines the input parameters for the child workflow activity.
//
// The activity always runs the child synchronously: the parent step
// blocks until the child completes (or times out) and the result is
// stored under Step.Store. Workflows that need fire-and-forget
// behavior should call ChildWorkflowExecutor.ExecuteAsync directly
// from a custom activity.
type ChildWorkflowInput struct {
	WorkflowName string                 `json:"workflow_name"`
	Inputs       map[string]interface{} `json:"inputs"`
	Timeout      time.Duration          `json:"timeout"`
	ParentID     string                 `json:"parent_id"`
}

// ChildWorkflowActivity executes a registered child workflow synchronously.
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
func (c *ChildWorkflowActivity) Execute(ctx workflow.Context, params ChildWorkflowInput) (map[string]any, error) {
	if params.WorkflowName == "" {
		return nil, fmt.Errorf("child workflow activity requires 'workflow_name' parameter")
	}

	inputs := params.Inputs
	if inputs == nil {
		inputs = make(map[string]interface{})
	}

	spec := &workflow.ChildWorkflowSpec{
		WorkflowName: params.WorkflowName,
		Inputs:       inputs,
		Timeout:      params.Timeout,
		ParentID:     params.ParentID,
	}

	result, err := c.executor.ExecuteSync(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("child workflow execution failed: %w", err)
	}

	return map[string]any{
		"outputs":      result.Outputs,
		"status":       string(result.Status),
		"execution_id": result.ExecutionID,
		"duration":     result.Duration.Seconds(),
		"success":      result.Status == workflow.ExecutionStatusCompleted,
	}, nil
}
