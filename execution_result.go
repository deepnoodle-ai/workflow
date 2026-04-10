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

	// Suspension describes the durable wait(s) that caused the execution
	// to end without completing. Populated when Status is
	// ExecutionStatusSuspended (and in future phases, Paused). nil
	// otherwise.
	Suspension *SuspensionInfo
}

// SuspensionReason classifies why an execution ended in a dormant state.
type SuspensionReason string

const (
	// SuspensionReasonWaitingSignal means one or more paths are parked
	// on a workflow.Wait or a declarative WaitSignal step.
	SuspensionReasonWaitingSignal SuspensionReason = "waiting_signal"
	// SuspensionReasonSleeping means one or more paths are parked on a
	// durable Sleep (Phase 2 — reserved).
	SuspensionReasonSleeping SuspensionReason = "sleeping"
	// SuspensionReasonPaused means one or more paths were paused by an
	// operator or a Pause step (Phase 1 — reserved).
	SuspensionReasonPaused SuspensionReason = "paused"
)

// SuspensionInfo describes why an execution ended dormant and what
// external input would move it forward. Consumers use this to decide
// how to schedule a resume — e.g., enqueue a signal listener,
// schedule a wake-up at WakeAt, or wait for an operator unpause.
type SuspensionInfo struct {
	// Reason is the dominant reason for the suspension. When multiple
	// paths are suspended for different reasons, the dominant one is
	// reported; SuspendedPaths has the full breakdown.
	Reason SuspensionReason

	// SuspendedPaths is one entry per hard-suspended path.
	SuspendedPaths []SuspendedPath

	// Topics is the union of signal topics any suspended path is
	// waiting on. Convenience for consumers that just want to know
	// "which channels should deliver into me?".
	Topics []string

	// WakeAt is the earliest absolute deadline across all suspended
	// paths (signal timeouts or sleep wake-ups). Zero if no path has a
	// deadline.
	WakeAt time.Time
}

// SuspendedPath describes a single path's suspension state.
type SuspendedPath struct {
	PathID   string
	StepName string
	Reason   SuspensionReason
	Topic    string    // set for waiting_signal
	WakeAt   time.Time // zero if no deadline
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

// Suspended returns true if the execution ended hard-suspended on a
// durable wait (signal-wait or durable sleep). The caller is
// responsible for scheduling resume when the external trigger arrives
// (use Suspension.Topics to subscribe to signals, Suspension.WakeAt
// to schedule a wall-clock resume).
func (r *ExecutionResult) Suspended() bool {
	return r.Status == ExecutionStatusSuspended
}

// Paused returns true if the execution ended dormant on an explicit
// pause trigger (PausePath call or declarative Pause step). The
// caller must clear the pause via UnpausePath / UnpausePathInCheckpoint
// before calling Resume.
func (r *ExecutionResult) Paused() bool {
	return r.Status == ExecutionStatusPaused
}

// NeedsResume returns true if the execution ended in a dormant state
// that requires an external trigger (signal delivery, wall-clock
// wake, or operator unpause) before it can continue. Equivalent to
// r.Suspended() || r.Paused().
func (r *ExecutionResult) NeedsResume() bool {
	return r.Suspended() || r.Paused()
}
