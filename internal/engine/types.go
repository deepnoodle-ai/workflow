package engine

import "github.com/deepnoodle-ai/workflow/domain"

// SubmitRequest contains the parameters for submitting a new workflow execution.
type SubmitRequest struct {
	Workflow    domain.WorkflowDefinition
	Inputs      map[string]any
	ExecutionID string // optional override
}

// ExecutionHandle is returned after submitting a workflow execution.
type ExecutionHandle struct {
	ID     string
	Status domain.ExecutionStatus
}
