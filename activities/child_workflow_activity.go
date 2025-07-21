package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// ChildWorkflowActivity executes child workflows as an activity
type ChildWorkflowActivity struct {
	executor workflow.ChildWorkflowExecutor
}

// NewChildWorkflowActivity creates a new ChildWorkflowActivity
func NewChildWorkflowActivity(executor workflow.ChildWorkflowExecutor) *ChildWorkflowActivity {
	return &ChildWorkflowActivity{
		executor: executor,
	}
}

// Name returns the activity name
func (c *ChildWorkflowActivity) Name() string {
	return "workflow.child"
}

// Execute runs the child workflow activity
func (c *ChildWorkflowActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	// Extract workflow name (required)
	workflowName, ok := params["workflow_name"].(string)
	if !ok || workflowName == "" {
		return nil, fmt.Errorf("child workflow activity requires 'workflow_name' parameter")
	}

	// Extract sync flag (default to true for synchronous execution)
	sync := true
	if syncParam, exists := params["sync"]; exists {
		if syncBool, ok := syncParam.(bool); ok {
			sync = syncBool
		}
	}

	// Extract inputs (optional)
	var inputs map[string]interface{}
	if inputsParam, exists := params["inputs"]; exists {
		if inputsMap, ok := inputsParam.(map[string]interface{}); ok {
			inputs = inputsMap
		} else if inputsMap, ok := inputsParam.(map[string]any); ok {
			// Convert map[string]any to map[string]interface{}
			inputs = make(map[string]interface{})
			for k, v := range inputsMap {
				inputs[k] = v
			}
		}
	}
	if inputs == nil {
		inputs = make(map[string]interface{})
	}

	// Extract timeout (optional)
	var timeout time.Duration
	if timeoutParam, exists := params["timeout"]; exists {
		switch t := timeoutParam.(type) {
		case string:
			var err error
			timeout, err = time.ParseDuration(t)
			if err != nil {
				return nil, fmt.Errorf("invalid timeout format: %w", err)
			}
		case time.Duration:
			timeout = t
		case int:
			timeout = time.Duration(t) * time.Second
		case int64:
			timeout = time.Duration(t) * time.Second
		case float64:
			timeout = time.Duration(t) * time.Second
		}
	}

	// Extract parent ID for tracing (optional)
	parentID := ""
	if parentIDParam, exists := params["parent_id"]; exists {
		if parentIDStr, ok := parentIDParam.(string); ok {
			parentID = parentIDStr
		}
	}

	// Create child workflow spec
	spec := &workflow.ChildWorkflowSpec{
		WorkflowName: workflowName,
		Inputs:       inputs,
		Timeout:      timeout,
		ParentID:     parentID,
		Sync:         sync,
	}

	// Execute based on sync flag
	if sync {
		return c.executeSync(ctx, spec)
	} else {
		return c.executeAsync(ctx, spec)
	}
}

// executeSync runs the child workflow synchronously
func (c *ChildWorkflowActivity) executeSync(ctx context.Context, spec *workflow.ChildWorkflowSpec) (any, error) {
	result, err := c.executor.ExecuteSync(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("child workflow execution failed: %w", err)
	}

	// For synchronous execution, we return the result which can include:
	// - outputs: the child workflow outputs
	// - status: execution status
	// - execution_id: for tracing
	// - duration: how long it took
	return map[string]interface{}{
		"outputs":      result.Outputs,
		"status":       string(result.Status),
		"execution_id": result.ExecutionID,
		"duration":     result.Duration.String(),
		"success":      result.Status == workflow.ExecutionStatusCompleted,
	}, nil
}

// executeAsync starts the child workflow asynchronously
func (c *ChildWorkflowActivity) executeAsync(ctx context.Context, spec *workflow.ChildWorkflowSpec) (any, error) {
	handle, err := c.executor.ExecuteAsync(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("failed to start child workflow: %w", err)
	}

	// For asynchronous execution, we return the handle for later reference
	return map[string]interface{}{
		"execution_id":  handle.ExecutionID,
		"workflow_name": handle.WorkflowName,
		"async":         true,
	}, nil
}
