package domain

import (
	"context"
	"time"
)

// ExecutionStatus represents the state of a workflow execution.
type ExecutionStatus string

const (
	ExecutionStatusPending   ExecutionStatus = "pending"
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusWaiting   ExecutionStatus = "waiting"
	ExecutionStatusCompleted ExecutionStatus = "completed"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusCancelled ExecutionStatus = "cancelled"
)

// ExecutionRecord represents the persistent state of a workflow execution.
type ExecutionRecord struct {
	ID           string
	WorkflowName string
	Status       ExecutionStatus
	Inputs       map[string]any
	Outputs      map[string]any
	CurrentStep  string // current step being executed (deprecated, use StateData)
	CreatedAt    time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	LastError    string
	CheckpointID string

	// StateData contains serialized execution state (JSON) for multi-step workflows.
	// Includes PathStates, JoinStates, StepOutputs, and PathCounter.
	StateData []byte
}

// ExecutionFilter specifies criteria for listing executions.
type ExecutionFilter struct {
	WorkflowName string
	Statuses     []ExecutionStatus
	Limit        int
	Offset       int
}

// ExecutionRepository defines operations for persisting execution records.
type ExecutionRepository interface {
	CreateExecution(ctx context.Context, record *ExecutionRecord) error
	GetExecution(ctx context.Context, id string) (*ExecutionRecord, error)
	UpdateExecution(ctx context.Context, record *ExecutionRecord) error
	ListExecutions(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error)
}
