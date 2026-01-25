package workflow

import (
	"github.com/deepnoodle-ai/workflow/internal/task"
)

// Tasks represent units of work that workers execute. Each task corresponds
// to a step in a workflow execution. The task system enables distributed
// execution by allowing workers to claim, execute, and complete tasks
// independently of the orchestrator.

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
