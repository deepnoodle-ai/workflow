package domain

import (
	"context"
	"time"
)

// TaskStatus represents the status of a task.
type TaskStatus string

const (
	TaskStatusPending   TaskStatus = "pending"
	TaskStatusRunning   TaskStatus = "running"
	TaskStatusCompleted TaskStatus = "completed"
	TaskStatusFailed    TaskStatus = "failed"
)

// TaskRecord represents a unit of work for workers to execute.
// Tasks are created by the engine when a workflow step needs to run.
type TaskRecord struct {
	// ID uniquely identifies this task (format: {execution_id}_{path_id}_{step}_{attempt})
	ID string

	// ExecutionID links to the parent workflow execution
	ExecutionID string

	// PathID identifies the execution path (for multi-step workflows)
	// Single-step workflows use "main" as the path ID.
	PathID string

	// StepName is the workflow step this task executes
	StepName string

	// ActivityName is the activity to execute
	ActivityName string

	// Attempt number (1-based, increments on retry)
	Attempt int

	// Status of the task
	Status TaskStatus

	// Spec defines what the worker should execute
	Spec *TaskSpec

	// WorkerID of the worker that claimed this task
	WorkerID string

	// VisibleAt controls when this task can be claimed (for retry delays)
	VisibleAt time.Time

	// LastHeartbeat from the worker
	LastHeartbeat time.Time

	// Result from the worker
	Result *TaskResult

	// Timestamps
	CreatedAt   time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

// TaskSpec defines what a worker should execute.
// Workers receive this and execute accordingly without needing workflow SDK.
type TaskSpec struct {
	// Type of execution: "container", "process", "http", "inline"
	Type string

	// Container execution
	Image   string
	Command []string

	// Process execution
	Program string
	Args    []string
	Dir     string

	// HTTP execution
	URL     string
	Method  string
	Headers map[string]string
	Body    string

	// Common
	Env     map[string]string
	Timeout time.Duration

	// Input data (JSON-serializable)
	Input map[string]any
}

// TaskResult is the result reported by a worker after execution.
type TaskResult struct {
	// Success indicates whether the task completed successfully
	Success bool

	// Output from the task (stdout for processes/containers)
	Output string

	// Error message if failed
	Error string

	// ExitCode for process/container execution
	ExitCode int

	// Data is structured output from the task
	Data map[string]any
}

// TaskClaimed is returned to workers when they successfully claim a task.
type TaskClaimed struct {
	// Task details
	ID           string
	ExecutionID  string
	PathID       string
	StepName     string
	ActivityName string
	Attempt      int
	Spec         *TaskSpec

	// Lease information
	HeartbeatInterval time.Duration
	LeaseExpiresAt    time.Time
}

// TaskRepository defines operations for persisting and distributing tasks.
type TaskRepository interface {
	CreateTask(ctx context.Context, t *TaskRecord) error
	ClaimTask(ctx context.Context, workerID string) (*TaskClaimed, error)
	CompleteTask(ctx context.Context, taskID, workerID string, result *TaskResult) error
	ReleaseTask(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error
	HeartbeatTask(ctx context.Context, taskID, workerID string) error
	GetTask(ctx context.Context, id string) (*TaskRecord, error)

	// Recovery operations
	ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*TaskRecord, error)
	ResetTask(ctx context.Context, taskID string) error
}
