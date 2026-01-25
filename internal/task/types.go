package task

import (
	"time"
)

// Status represents the status of a task.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// Record represents a unit of work for workers to execute.
// Tasks are created by the engine when a workflow step needs to run.
type Record struct {
	// ID uniquely identifies this task (format: {execution_id}_{step}_{attempt})
	ID string `json:"id"`

	// ExecutionID links to the parent workflow execution
	ExecutionID string `json:"execution_id"`

	// StepName is the workflow step this task executes
	StepName string `json:"step_name"`

	// ActivityName is the activity to execute
	ActivityName string `json:"activity_name"`

	// Attempt number (1-based, increments on retry)
	Attempt int `json:"attempt"`

	// Status of the task
	Status Status `json:"status"`

	// Spec defines what the worker should execute
	Spec *Spec `json:"spec"`

	// WorkerID of the worker that claimed this task
	WorkerID string `json:"worker_id,omitempty"`

	// VisibleAt controls when this task can be claimed (for retry delays)
	VisibleAt time.Time `json:"visible_at"`

	// LastHeartbeat from the worker
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`

	// Result from the worker
	Result *Result `json:"result,omitempty"`

	// Timestamps
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// Spec defines what a worker should execute.
// Workers receive this and execute accordingly without needing workflow SDK.
type Spec struct {
	// Type of execution: "container", "process", "http", "inline"
	Type string `json:"type"`

	// Container execution
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`

	// Process execution
	Program string   `json:"program,omitempty"`
	Args    []string `json:"args,omitempty"`
	Dir     string   `json:"dir,omitempty"`

	// HTTP execution
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`

	// Common
	Env     map[string]string `json:"env,omitempty"`
	Timeout time.Duration     `json:"timeout,omitempty"`

	// Input data (JSON-serializable)
	Input map[string]any `json:"input,omitempty"`
}

// Result is the result reported by a worker after execution.
type Result struct {
	// Success indicates whether the task completed successfully
	Success bool `json:"success"`

	// Output from the task (stdout for processes/containers)
	Output string `json:"output,omitempty"`

	// Error message if failed
	Error string `json:"error,omitempty"`

	// ExitCode for process/container execution
	ExitCode int `json:"exit_code,omitempty"`

	// Data is structured output from the task
	Data map[string]any `json:"data,omitempty"`
}

// Claimed is returned to workers when they successfully claim a task.
type Claimed struct {
	// Task details
	ID           string `json:"id"`
	ExecutionID  string `json:"execution_id"`
	StepName     string `json:"step_name"`
	ActivityName string `json:"activity_name"`
	Attempt      int    `json:"attempt"`
	Spec         *Spec  `json:"spec"`

	// Lease information
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`
	LeaseExpiresAt    time.Time     `json:"lease_expires_at"`
}
