package workflow

import "github.com/deepnoodle-ai/workflow/domain"

// Tasks represent units of work that workers execute. Each task corresponds
// to a step in a workflow execution. The task system enables distributed
// execution by allowing workers to claim, execute, and complete tasks
// independently of the orchestrator.

// TaskStatus represents the status of a task.
type TaskStatus = domain.TaskStatus

const (
	TaskStatusPending   = domain.TaskStatusPending
	TaskStatusRunning   = domain.TaskStatusRunning
	TaskStatusCompleted = domain.TaskStatusCompleted
	TaskStatusFailed    = domain.TaskStatusFailed
)

// TaskRecord represents a unit of work for workers to execute.
type TaskRecord = domain.TaskRecord

// TaskSpec defines what a worker should execute.
type TaskSpec = domain.TaskSpec

// TaskResult is the result reported by a worker after execution.
type TaskResult = domain.TaskResult

// ClaimedTask is returned to workers when they successfully claim a task.
type ClaimedTask = domain.TaskClaimed
