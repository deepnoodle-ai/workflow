package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/engine"
)

// Engine types - re-exported from internal/engine for backwards compatibility.
//
// These types provide the public API for the workflow engine. Internal code
// should use internal/engine directly to avoid import cycles and enable
// cleaner package boundaries.
//
// Type aliases are used where the internal type can be used directly without
// modification. Concrete types are defined where the public API differs from
// the internal representation (e.g., SubmitRequest uses *Workflow instead of
// the WorkflowDefinition interface).

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
// This is defined locally to use the concrete *Workflow type for backwards compatibility.
type SubmitRequest struct {
	Workflow    *Workflow
	Inputs      map[string]any
	ExecutionID string // optional override
}

// ExecutionHandle is returned after submitting a workflow execution.
type ExecutionHandle = engine.ExecutionHandle
