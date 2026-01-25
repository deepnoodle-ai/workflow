package engine

import (
	"github.com/deepnoodle-ai/workflow/domain"
)

// Re-export domain types for backward compatibility.
// New code should import directly from domain package.

// ExecutionStatus represents the engine-level execution state.
type ExecutionStatus = domain.ExecutionStatus

const (
	StatusPending   = domain.ExecutionStatusPending
	StatusRunning   = domain.ExecutionStatusRunning
	StatusCompleted = domain.ExecutionStatusCompleted
	StatusFailed    = domain.ExecutionStatusFailed
	StatusCancelled = domain.ExecutionStatusCancelled
)

// ExecutionRecord represents the persistent state of a workflow execution.
type ExecutionRecord = domain.ExecutionRecord

// ExecutionFilter specifies criteria for listing executions.
type ExecutionFilter = domain.ExecutionFilter

// RecoveryMode determines how the engine handles orphaned executions at startup.
type RecoveryMode string

const (
	// RecoveryResume attempts to resume orphaned executions from their last checkpoint.
	RecoveryResume RecoveryMode = "resume"
	// RecoveryFail marks orphaned executions as failed.
	RecoveryFail RecoveryMode = "fail"
)

// WorkflowDefinition is the interface that workflow definitions must implement
// for the engine to execute them. This avoids import cycles by not depending
// on the concrete workflow.Workflow type.
type WorkflowDefinition interface {
	Name() string
	// StepList returns workflow steps. Each element must implement StepDefinition.
	StepList() []any
}

// StepDefinition is the interface for workflow steps.
type StepDefinition interface {
	StepName() string
	ActivityName() string
	StepParameters() map[string]any
}

// SubmitRequest contains the parameters for submitting a new workflow execution.
type SubmitRequest struct {
	Workflow    WorkflowDefinition
	Inputs      map[string]any
	ExecutionID string // optional override
}

// ExecutionHandle is returned after submitting a workflow execution.
type ExecutionHandle struct {
	ID     string
	Status ExecutionStatus
}
