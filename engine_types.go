package workflow

import (
	"time"
)

// EngineExecutionStatus represents the engine-level execution state.
// This is distinct from the library's ExecutionStatus which tracks
// internal workflow state (paths, steps). The engine maps between them:
//   - Pending/Running map to the engine dispatching work
//   - Completed/Failed/Cancelled map from workflow completion
type EngineExecutionStatus string

const (
	EngineStatusPending   EngineExecutionStatus = "pending"
	EngineStatusRunning   EngineExecutionStatus = "running"
	EngineStatusCompleted EngineExecutionStatus = "completed"
	EngineStatusFailed    EngineExecutionStatus = "failed"
	EngineStatusCancelled EngineExecutionStatus = "cancelled"
)

// ExecutionRecord represents the persistent state of a workflow execution.
type ExecutionRecord struct {
	ID            string
	WorkflowName  string
	Status        EngineExecutionStatus
	Inputs        map[string]any
	Outputs       map[string]any
	Attempt       int       // fencing token for distributed execution
	WorkerID      string    // which worker owns this execution
	LastHeartbeat time.Time // liveness signal from worker
	DispatchedAt  time.Time // when dispatch mode handed off to worker
	CreatedAt     time.Time
	StartedAt     time.Time
	CompletedAt   time.Time
	LastError     string
	CheckpointID  string
}

// RecoveryMode determines how the engine handles orphaned executions at startup.
type RecoveryMode string

const (
	// RecoveryResume attempts to resume orphaned executions from their last checkpoint.
	RecoveryResume RecoveryMode = "resume"
	// RecoveryFail marks orphaned executions as failed.
	RecoveryFail RecoveryMode = "fail"
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
