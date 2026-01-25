package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Engine types for workflow execution state and lifecycle management.

// EngineExecutionStatus represents the engine-level execution state.
type EngineExecutionStatus = engine.ExecutionStatus

const (
	EngineStatusPending   = engine.StatusPending
	EngineStatusRunning   = engine.StatusRunning
	EngineStatusCompleted = engine.StatusCompleted
	EngineStatusFailed    = engine.StatusFailed
	EngineStatusCancelled = engine.StatusCancelled
)

// ExecutionRecord represents the persistent state of a workflow execution.
type ExecutionRecord = engine.ExecutionRecord

// RecoveryMode determines how the engine handles orphaned executions at startup.
type RecoveryMode = engine.RecoveryMode

const (
	RecoveryResume = engine.RecoveryResume
	RecoveryFail   = engine.RecoveryFail
)

// SubmitRequest contains the parameters for submitting a new workflow execution.
type SubmitRequest struct {
	Workflow    *Workflow
	Inputs      map[string]any
	ExecutionID string // optional override
}

// ExecutionHandle is returned after submitting a workflow execution.
type ExecutionHandle = engine.ExecutionHandle
