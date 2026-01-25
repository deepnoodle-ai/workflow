package workflow

import "github.com/deepnoodle-ai/workflow/domain"

// Engine types for workflow execution state and lifecycle management.

// EngineExecutionStatus represents the engine-level execution state.
type EngineExecutionStatus = domain.ExecutionStatus

const (
	EngineStatusPending   = domain.ExecutionStatusPending
	EngineStatusRunning   = domain.ExecutionStatusRunning
	EngineStatusCompleted = domain.ExecutionStatusCompleted
	EngineStatusFailed    = domain.ExecutionStatusFailed
	EngineStatusCancelled = domain.ExecutionStatusCancelled
)

// ExecutionRecord represents the persistent state of a workflow execution.
type ExecutionRecord = domain.ExecutionRecord

// RecoveryMode determines how the engine handles orphaned executions at startup.
type RecoveryMode string

const (
	RecoveryResume RecoveryMode = "resume"
	RecoveryFail   RecoveryMode = "fail"
)

// SubmitRequest contains the parameters for submitting a new workflow execution.
type SubmitRequest struct {
	Workflow    *Workflow
	Inputs      map[string]any
	ExecutionID string // optional override
}

// ExecutionHandle is returned after submitting a workflow execution.
type ExecutionHandle struct {
	ID     string
	Status EngineExecutionStatus
}
