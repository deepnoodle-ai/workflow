package task

import (
	"github.com/deepnoodle-ai/workflow/domain"
)

// Re-export domain task types for backward compatibility.
// New code should import directly from domain package.

// Status represents the status of a task.
type Status = domain.TaskStatus

const (
	StatusPending   = domain.TaskStatusPending
	StatusRunning   = domain.TaskStatusRunning
	StatusCompleted = domain.TaskStatusCompleted
	StatusFailed    = domain.TaskStatusFailed
)

// Record represents a unit of work for workers to execute.
type Record = domain.TaskRecord

// Spec defines what a worker should execute.
type Spec = domain.TaskSpec

// Result is the result reported by a worker after execution.
type Result = domain.TaskResult

// Claimed is returned to workers when they successfully claim a task.
type Claimed = domain.TaskClaimed
