package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/task"
)

// Task types - re-exported from internal/task for backwards compatibility.
// New code should use internal/task directly.

// TaskStatus represents the status of a task.
type TaskStatus = task.Status

const (
	TaskStatusPending   = task.StatusPending
	TaskStatusRunning   = task.StatusRunning
	TaskStatusCompleted = task.StatusCompleted
	TaskStatusFailed    = task.StatusFailed
)

// TaskRecord represents a unit of work for workers to execute.
type TaskRecord = task.Record

// TaskSpec defines what a worker should execute.
type TaskSpec = task.Spec

// TaskResult is the result reported by a worker after execution.
type TaskResult = task.Result

// ClaimedTask is returned to workers when they successfully claim a task.
type ClaimedTask = task.Claimed
