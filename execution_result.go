package workflow

import "time"

// ExecutionResult contains the outcome of a workflow execution.
// When returned from Execute/ExecuteOrResume, it is always non-nil if error is nil.
type ExecutionResult struct {
	// WorkflowName identifies which workflow was executed.
	WorkflowName string

	// Status is the final execution status.
	Status ExecutionStatus

	// Outputs contains the workflow's output values, keyed by output name.
	// Empty if the workflow failed before producing outputs.
	Outputs map[string]any

	// Error is the classified workflow error, if the execution failed.
	// nil when Status is ExecutionStatusCompleted.
	Error *WorkflowError

	// Timing contains execution duration measurements.
	Timing ExecutionTiming

	// FollowUps contains follow-up workflow specs produced by completion hooks.
	// Empty when no hooks are configured or the execution did not complete
	// successfully.
	FollowUps []FollowUpSpec
}

// FollowUpSpec describes a workflow that should be triggered after a
// successful execution. It is a descriptor, not an execution request —
// the consumer is responsible for persisting and processing these.
type FollowUpSpec struct {
	// WorkflowName identifies which workflow to trigger.
	WorkflowName string

	// Inputs are the input values for the follow-up workflow.
	Inputs map[string]any

	// Metadata is arbitrary data the consumer can use for routing,
	// deduplication, or prioritization. The library does not inspect it.
	Metadata map[string]any
}

// ExecutionTiming captures wall-clock timing for the execution.
type ExecutionTiming struct {
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
}

// Completed returns true if the execution finished successfully.
func (r *ExecutionResult) Completed() bool {
	return r.Status == ExecutionStatusCompleted
}

// Failed returns true if the execution finished with an error.
func (r *ExecutionResult) Failed() bool {
	return r.Status == ExecutionStatusFailed
}
