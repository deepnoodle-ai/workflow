package engine

import (
	"time"
)

// ExecutionStatus represents the engine-level execution state.
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "pending"
	StatusRunning   ExecutionStatus = "running"
	StatusCompleted ExecutionStatus = "completed"
	StatusFailed    ExecutionStatus = "failed"
	StatusCancelled ExecutionStatus = "cancelled"
)

// ExecutionRecord represents the persistent state of a workflow execution.
type ExecutionRecord struct {
	ID           string
	WorkflowName string
	Status       ExecutionStatus
	Inputs       map[string]any
	Outputs      map[string]any
	CurrentStep  string // current step being executed
	CreatedAt    time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	LastError    string
	CheckpointID string
}

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

// ExecutionFilter specifies criteria for listing executions.
type ExecutionFilter struct {
	WorkflowName string
	Statuses     []ExecutionStatus
	Limit        int
	Offset       int
}
