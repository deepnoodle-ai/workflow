package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/domain"
)

// WorkflowTool wraps a workflow as a tool that can be invoked by an agent.
// This enables the "workflow below agent" perspective where agents can
// trigger complex workflows as part of their reasoning.
type WorkflowTool struct {
	name        string
	description string
	workflow    *workflow.Workflow
	engine      *workflow.Engine
	schema      *ToolSchema
	async       bool
	timeout     time.Duration
}

// WorkflowToolOptions configures a WorkflowTool.
type WorkflowToolOptions struct {
	// Name is the tool name (defaults to workflow name).
	Name string

	// Description for the LLM (defaults to workflow description).
	Description string

	// Async determines if the workflow is executed asynchronously.
	// If true, the tool returns immediately with an execution ID.
	// If false (default), the tool waits for workflow completion.
	Async bool

	// Timeout for synchronous execution (default: 5 minutes).
	Timeout time.Duration

	// Schema overrides the auto-generated schema.
	Schema *ToolSchema
}

// NewWorkflowTool creates a tool that executes a workflow.
func NewWorkflowTool(wf *workflow.Workflow, engine *workflow.Engine, opts WorkflowToolOptions) *WorkflowTool {
	name := opts.Name
	if name == "" {
		name = wf.Name()
	}

	description := opts.Description
	if description == "" {
		description = fmt.Sprintf("Execute the %s workflow", wf.Name())
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	schema := opts.Schema
	if schema == nil {
		schema = generateSchemaFromWorkflow(wf)
	}

	return &WorkflowTool{
		name:        name,
		description: description,
		workflow:    wf,
		engine:      engine,
		schema:      schema,
		async:       opts.Async,
		timeout:     timeout,
	}
}

// Name returns the tool name.
func (t *WorkflowTool) Name() string {
	return t.name
}

// Description returns the tool description.
func (t *WorkflowTool) Description() string {
	return t.description
}

// Schema returns the tool schema.
func (t *WorkflowTool) Schema() *ToolSchema {
	return t.schema
}

// Execute runs the workflow and returns the result.
func (t *WorkflowTool) Execute(ctx context.Context, args map[string]any) (*ToolResult, error) {
	// Submit the workflow
	handle, err := t.engine.Submit(ctx, workflow.SubmitRequest{
		Workflow: t.workflow,
		Inputs:   args,
	})
	if err != nil {
		return &ToolResult{
			Error:   fmt.Sprintf("failed to submit workflow: %v", err),
			Success: false,
		}, nil
	}

	// For async execution, return immediately with execution ID
	if t.async {
		return &ToolResult{
			Output: fmt.Sprintf(`{"execution_id": %q, "status": "submitted"}`, handle.ID),
			Success: true,
		}, nil
	}

	// Wait for completion with timeout
	execCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	// Poll for completion
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-execCtx.Done():
			return &ToolResult{
				Output:  fmt.Sprintf(`{"execution_id": %q, "status": "timeout"}`, handle.ID),
				Error:   "workflow execution timed out",
				Success: false,
			}, nil
		case <-ticker.C:
			status, err := t.engine.Get(ctx, handle.ID)
			if err != nil {
				return &ToolResult{
					Error:   fmt.Sprintf("failed to get workflow status: %v", err),
					Success: false,
				}, nil
			}

			switch status.Status {
			case domain.ExecutionStatusCompleted:
				// Marshal the outputs
				outputJSON, err := json.Marshal(status.Outputs)
				if err != nil {
					return &ToolResult{
						Error:   fmt.Sprintf("failed to marshal outputs: %v", err),
						Success: false,
					}, nil
				}
				return &ToolResult{
					Output:  string(outputJSON),
					Success: true,
				}, nil

			case domain.ExecutionStatusFailed:
				return &ToolResult{
					Output:  fmt.Sprintf(`{"execution_id": %q, "status": "failed"}`, handle.ID),
					Error:   status.LastError,
					Success: false,
				}, nil

			case domain.ExecutionStatusRunning, domain.ExecutionStatusPending:
				// Continue polling
				continue
			}
		}
	}
}

// generateSchemaFromWorkflow creates a schema from workflow inputs.
func generateSchemaFromWorkflow(wf *workflow.Workflow) *ToolSchema {
	schema := NewObjectSchema()

	// If the workflow has defined inputs, use them
	inputs := wf.Inputs()
	for _, inputDef := range inputs {
		prop := &Property{
			Type:        inferTypeFromDefault(inputDef.Default),
			Description: inputDef.Description,
		}
		schema.AddProperty(inputDef.Name, prop)
		if inputDef.IsRequired() {
			schema.AddRequired(inputDef.Name)
		}
	}

	return schema
}

// inferTypeFromDefault infers the JSON schema type from a default value.
func inferTypeFromDefault(val any) string {
	if val == nil {
		return "string" // Default to string
	}

	switch val.(type) {
	case string:
		return "string"
	case int, int32, int64, float32, float64:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "string"
	}
}

// WorkflowStatusTool is a tool for checking workflow execution status.
// Useful when using async workflows.
type WorkflowStatusTool struct {
	engine *workflow.Engine
}

// NewWorkflowStatusTool creates a tool for checking workflow status.
func NewWorkflowStatusTool(engine *workflow.Engine) *WorkflowStatusTool {
	return &WorkflowStatusTool{engine: engine}
}

// Name returns the tool name.
func (t *WorkflowStatusTool) Name() string {
	return "check_workflow_status"
}

// Description returns the tool description.
func (t *WorkflowStatusTool) Description() string {
	return "Check the status of an async workflow execution"
}

// Schema returns the tool schema.
func (t *WorkflowStatusTool) Schema() *ToolSchema {
	return NewObjectSchema().
		AddProperty("execution_id", StringProperty("The workflow execution ID")).
		AddRequired("execution_id")
}

// Execute checks the workflow status.
func (t *WorkflowStatusTool) Execute(ctx context.Context, args map[string]any) (*ToolResult, error) {
	execID, ok := args["execution_id"].(string)
	if !ok {
		return &ToolResult{
			Error:   "execution_id is required",
			Success: false,
		}, nil
	}

	status, err := t.engine.Get(ctx, execID)
	if err != nil {
		return &ToolResult{
			Error:   fmt.Sprintf("failed to get status: %v", err),
			Success: false,
		}, nil
	}

	result := map[string]any{
		"execution_id": execID,
		"status":       string(status.Status),
	}

	if status.Status == domain.ExecutionStatusCompleted {
		result["outputs"] = status.Outputs
	}
	if status.LastError != "" {
		result["error"] = status.LastError
	}

	outputJSON, err := json.Marshal(result)
	if err != nil {
		return &ToolResult{
			Error:   fmt.Sprintf("failed to marshal result: %v", err),
			Success: false,
		}, nil
	}

	return &ToolResult{
		Output:  string(outputJSON),
		Success: true,
	}, nil
}

// Verify interface compliance.
var _ Tool = (*WorkflowTool)(nil)
var _ Tool = (*WorkflowStatusTool)(nil)
