// Package client provides a simple interface for workflow clients to interact
// with a workflow server. Clients only need to know about workflows and results,
// not internal implementation details like tasks, paths, or checkpoints.
package client

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// Client provides a simple interface for interacting with a workflow service.
// This is the primary interface for applications that want to submit and
// monitor workflow executions.
type Client interface {
	// Submit starts a new workflow execution.
	Submit(ctx context.Context, wf *workflow.Workflow, inputs map[string]any) (string, error)

	// Get retrieves the current status of an execution.
	Get(ctx context.Context, id string) (*Status, error)

	// Cancel requests cancellation of an execution.
	Cancel(ctx context.Context, id string) error

	// Wait blocks until the execution completes and returns the result.
	Wait(ctx context.Context, id string) (*Result, error)

	// List returns executions matching the filter.
	List(ctx context.Context, filter ListFilter) ([]*Status, error)
}

// Status represents the current state of a workflow execution.
type Status struct {
	ID           string
	WorkflowName string
	State        State
	CurrentStep  string
	Error        string
	CreatedAt    time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
}

// State represents the execution state.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Result contains the final output of a completed workflow execution.
type Result struct {
	ID           string
	WorkflowName string
	State        State
	Outputs      map[string]any
	Error        string
	Duration     time.Duration
}

// ListFilter specifies criteria for listing executions.
type ListFilter struct {
	WorkflowName string
	States       []State
	Limit        int
	Offset       int
}

// SubmitOptions contains optional parameters for Submit.
type SubmitOptions struct {
	// ExecutionID allows specifying a custom execution ID.
	// If empty, one will be generated.
	ExecutionID string
}
