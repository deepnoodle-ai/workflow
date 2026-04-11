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
	// SuspensionReasonWaitingSignal means one or more branches are parked
	// on a workflow.Wait or a declarative WaitSignal step.
	SuspensionReasonWaitingSignal SuspensionReason = "waiting_signal"
	// SuspensionReasonSleeping means one or more branches are parked on a
	// durable Sleep (Phase 2 — reserved).
	SuspensionReasonSleeping SuspensionReason = "sleeping"
	// SuspensionReasonPaused means one or more branches were paused by an
	// operator or a Pause step (Phase 1 — reserved).
	SuspensionReasonPaused SuspensionReason = "paused"
)

// SuspensionInfo describes why an execution ended dormant and what
// external input would move it forward. Consumers use this to decide
// how to schedule a resume — e.g., enqueue a signal listener,
// schedule a wake-up at WakeAt, or wait for an operator unpause.
type SuspensionInfo struct {
	// Reason is the dominant reason for the suspension. When multiple
	// branches are suspended for different reasons, the dominant one is
	// reported; SuspendedBranches has the full breakdown.
	Reason SuspensionReason

	// SuspendedBranches is one entry per hard-suspended branch.
	SuspendedBranches []SuspendedBranch

	// Topics is the union of signal topics any suspended branch is
	// waiting on. Convenience for consumers that just want to know
	// "which channels should deliver into me?".
	Topics []string

	// WakeAt is the earliest absolute deadline across all suspended
	// branches (signal timeouts or sleep wake-ups). Zero if no branch has a
	// deadline.
	WakeAt time.Time
}

// SuspendedBranch describes a single branch's suspension state.
type SuspendedBranch struct {
	BranchID      string
	StepName    string
	Reason      SuspensionReason
	Topic       string    // set for waiting_signal
	WakeAt      time.Time // zero if no deadline
	PauseReason string    // set for paused
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
// pause trigger (PauseBranch call or declarative Pause step). The
// caller must clear the pause via UnpauseBranch / UnpauseBranchInCheckpoint
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

// Output returns the raw output value at key and whether it was
// present. Returns (nil, false) when the result has no outputs map
// or the key is missing.
func (r *ExecutionResult) Output(key string) (any, bool) {
	if r == nil || r.Outputs == nil {
		return nil, false
	}
	v, ok := r.Outputs[key]
	return v, ok
}

// OutputString returns the output at key as a string and whether the
// type assertion succeeded. Returns ("", false) if the key is missing
// or the value is not a string.
func (r *ExecutionResult) OutputString(key string) (string, bool) {
	v, ok := r.Output(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// OutputInt returns the output at key as an int and whether the
// conversion succeeded. Recognises Go's numeric types (int, int32,
// int64, float32, float64) so values that round-tripped through JSON
// (where numbers come back as float64) work as expected. Returns
// (0, false) if the key is missing or the value is not numeric.
//
// Float values are truncated to int; precision loss is the caller's
// responsibility — use OutputAs[float64] when fractional precision
// matters.
func (r *ExecutionResult) OutputInt(key string) (int, bool) {
	v, ok := r.Output(key)
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float32:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

// OutputBool returns the output at key as a bool and whether the
// type assertion succeeded. Returns (false, false) if the key is
// missing or the value is not a bool.
func (r *ExecutionResult) OutputBool(key string) (bool, bool) {
	v, ok := r.Output(key)
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// WaitReason returns the dominant suspension reason if the execution
// is suspended, or empty string otherwise. Convenience for the common
// "what kind of resume do I need to schedule?" question.
func (r *ExecutionResult) WaitReason() SuspensionReason {
	if r == nil || r.Suspension == nil {
		return ""
	}
	return r.Suspension.Reason
}

// Topics returns the union of signal topics that suspended branches
// are waiting on, or nil if the execution is not suspended on a
// signal-wait. Consumers use this to register signal listeners
// before scheduling a resume.
func (r *ExecutionResult) Topics() []string {
	if r == nil || r.Suspension == nil {
		return nil
	}
	return r.Suspension.Topics
}

// NextWakeAt returns the earliest wall-clock deadline across all
// suspended branches and whether one is set. Returns (zero, false)
// if the execution is not suspended or no branch has a deadline.
// Consumers use this to schedule a wall-clock resume — typical use
// is `time.AfterFunc(time.Until(t), resumeFn)`.
func (r *ExecutionResult) NextWakeAt() (time.Time, bool) {
	if r == nil || r.Suspension == nil || r.Suspension.WakeAt.IsZero() {
		return time.Time{}, false
	}
	return r.Suspension.WakeAt, true
}

// OutputAs returns the output at key coerced to T and whether the
// type assertion succeeded. Generic counterpart to OutputString /
// OutputBool for arbitrary types — useful when consumers store
// custom structs in workflow outputs.
//
// Returns (zero T, false) if the key is missing or the value cannot
// be type-asserted to T. No JSON-style conversion is performed; the
// value must already be of type T (or assignable to it).
func OutputAs[T any](r *ExecutionResult, key string) (T, bool) {
	var zero T
	v, ok := r.Output(key)
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}
