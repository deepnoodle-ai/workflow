package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoCheckpoint is returned when Resume or RunOrResume cannot find a
// checkpoint for the given execution ID. Use errors.Is to check for it.
var ErrNoCheckpoint = errors.New("no checkpoint found")

// ErrAlreadyStarted is returned when Run/Execute is called on an Execution
// that has already been started.
var ErrAlreadyStarted = errors.New("execution already started")

// ErrNilExecution is returned when Runner.Run receives a nil *Execution.
var ErrNilExecution = errors.New("execution must not be nil")

// ErrInvalidHeartbeatInterval is returned when a HeartbeatConfig has a
// non-positive Interval.
var ErrInvalidHeartbeatInterval = errors.New("heartbeat interval must be positive")

// ErrNilHeartbeatFunc is returned when a HeartbeatConfig has a nil Func.
var ErrNilHeartbeatFunc = errors.New("heartbeat func must not be nil")

// Structural validation sentinels. All are reported as ValidationProblem
// fields on *ValidationError when workflow.New runs.
var (
	// ErrDuplicateStepName is reported when two steps share a name.
	ErrDuplicateStepName = errors.New("workflow: duplicate step name")
	// ErrEmptyStepName is reported when a step has no name.
	ErrEmptyStepName = errors.New("workflow: empty step name")
	// ErrUnknownStartStep is reported when Options.StartAt names a step
	// that does not exist in the workflow.
	ErrUnknownStartStep = errors.New("workflow: start step not found")
	// ErrUnknownEdgeTarget is reported when an edge points at a step
	// that does not exist in the workflow.
	ErrUnknownEdgeTarget = errors.New("workflow: edge destination not found")
	// ErrUnknownCatchTarget is reported when a catch handler points at
	// a step that does not exist in the workflow.
	ErrUnknownCatchTarget = errors.New("workflow: catch destination not found")
	// ErrUnknownJoinBranch is reported when JoinConfig.Branches names
	// a branch that no upstream edge declares.
	ErrUnknownJoinBranch = errors.New("workflow: join branch not found")
	// ErrInvalidStepKind is reported when a step mixes multiple step
	// kinds (activity/join/wait_signal/sleep/pause).
	ErrInvalidStepKind = errors.New("workflow: conflicting step kinds")
	// ErrInvalidModifier is reported when a modifier field (Retry,
	// Catch) is attached to a step kind that cannot use it.
	ErrInvalidModifier = errors.New("workflow: modifier not allowed on step kind")
	// ErrInvalidRetryConfig is reported when a RetryConfig has
	// nonsensical bounds (negative retries, MaxDelay < BaseDelay, etc.).
	ErrInvalidRetryConfig = errors.New("workflow: invalid retry config")
	// ErrInvalidSleepConfig is reported when a SleepConfig has a
	// non-positive Duration.
	ErrInvalidSleepConfig = errors.New("workflow: invalid sleep config")
	// ErrInvalidWaitConfig is reported when a WaitSignalConfig has a
	// missing topic, non-positive timeout, or dangling OnTimeout.
	ErrInvalidWaitConfig = errors.New("workflow: invalid wait_signal config")
	// ErrReservedBranchName is reported when a named branch uses the
	// reserved name "main".
	ErrReservedBranchName = errors.New("workflow: branch name 'main' is reserved")
	// ErrDuplicateBranchName is reported when two edges declare the
	// same branch name.
	ErrDuplicateBranchName = errors.New("workflow: duplicate branch name")
	// ErrUnknownActivity is reported when a step references an activity
	// name that is not registered on the ActivityRegistry passed to
	// NewExecution. Surfaced as a ValidationProblem on *ValidationError.
	ErrUnknownActivity = errors.New("workflow: activity not registered")
	// ErrInvalidTemplate is reported when a parameter template or
	// WaitSignalConfig.Topic template fails to parse or compile.
	ErrInvalidTemplate = errors.New("workflow: invalid template")
	// ErrInvalidExpression is reported when an edge condition or
	// parameter script expression fails to compile.
	ErrInvalidExpression = errors.New("workflow: invalid expression")
	// ErrInvalidStorePath is reported when a Store field
	// (Step.Store, WaitSignalConfig.Store, CatchConfig.Store,
	// Output.Variable) is given with a leading "state." prefix. Store
	// fields must be bare variable names.
	ErrInvalidStorePath = errors.New("workflow: store field must be a bare variable name")
)

// Error type constants for classification and matching
const (
	// ErrorTypeAll acts as a wildcard that matches any error except fatal errors
	ErrorTypeAll = "all"

	// ErrorTypeActivityFailed matches any error except timeouts and fatal errors
	ErrorTypeActivityFailed = "activity_failed"

	// ErrorTypeTimeout matches a timeout context canceled error
	ErrorTypeTimeout = "timeout"

	// ErrorTypeFatal indicates an execution failed due to a fatal error.
	// The approach we're taking is that by default, unknown errors are
	// classified as activity failed errors. This is because we want to
	// allow retries on unknown errors by default. If we know a specific
	// error should NOT be retried, it should have type=ErrorTypeFatal set.
	ErrorTypeFatal = "fatal_error"
)

// WorkflowError represents a structured error with classification
// It supports Go's error wrapping patterns with Unwrap() method
type WorkflowError struct {
	Type    string      `json:"type"`
	Cause   string      `json:"cause"`
	Details interface{} `json:"details,omitempty"`
	Wrapped error       `json:"-"` // Original error being wrapped
}

// Error implements the error interface
func (e *WorkflowError) Error() string {
	return fmt.Sprintf("%s: %s", e.Type, e.Cause)
}

// Unwrap implements the error unwrapping interface for Go's errors.Is and errors.As
func (e *WorkflowError) Unwrap() error {
	return e.Wrapped
}

// ErrorOutput represents the structured error information passed to catch handlers
type ErrorOutput struct {
	Error   string      `json:"Error"`
	Cause   string      `json:"Cause"`
	Details interface{} `json:"Details,omitempty"`
}

// NewWorkflowError creates a new WorkflowError with the specified type and cause.
// The type can be any user-defined string e.g. "network-error". The important
// thing is that it may be used to match against the type used in a retry config.
func NewWorkflowError(errorType, cause string) *WorkflowError {
	return &WorkflowError{
		Type:  errorType,
		Cause: cause,
	}
}

// ClassifyError attempts to classify a regular error into a WorkflowError
func ClassifyError(err error) *WorkflowError {
	// If the error is already a WorkflowError, return it
	var workflowError *WorkflowError
	if errors.As(err, &workflowError) {
		return workflowError
	}
	// ErrWaitTimeout is a first-class timeout sentinel emitted by
	// workflow.Wait and the declarative WaitSignal step. Reuse the
	// existing "timeout" classification so catch handlers with
	// ErrorEquals=["timeout"] route these the same as any other
	// timeout. This keeps consumer error handling uniform.
	if errors.Is(err, ErrWaitTimeout) {
		return &WorkflowError{
			Type:    ErrorTypeTimeout,
			Cause:   err.Error(),
			Wrapped: err,
		}
	}
	// Check for timeout patterns
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return &WorkflowError{
			Type:    ErrorTypeTimeout,
			Cause:   err.Error(),
			Wrapped: err,
		}
	}
	// Default to an activity failed error
	return &WorkflowError{
		Type:    ErrorTypeActivityFailed,
		Cause:   err.Error(),
		Wrapped: err,
	}
}

// MatchesErrorType checks if an error matches a specified error type pattern
func MatchesErrorType(err error, errorType string) bool {
	// Fence violations are never retryable or catchable
	if errors.Is(err, ErrFenceViolation) {
		return false
	}
	// Wait-unwinds are not failures — they are suspensions — and must
	// never match a retry or catch pattern. The engine also has an
	// explicit isWaitUnwind guard in executeStep/executeStepWithRetry,
	// but this keeps MatchesErrorType consistent so callers that use
	// it for custom error handling also see the bypass.
	if isWaitUnwind(err) {
		return false
	}
	wErr := ClassifyError(err)
	// Fatal errors are only matched by the ErrorTypeFatal pattern
	if wErr.Type == ErrorTypeFatal {
		return errorType == ErrorTypeFatal
	}
	// Otherwise...
	switch errorType {
	case ErrorTypeAll:
		return true
	case ErrorTypeActivityFailed:
		return wErr.Type != ErrorTypeTimeout
	default:
		// Note the intent here is to handle arbitrary error type strings, not
		// just a fixed set of types.
		return wErr.Type == errorType
	}
}

// ToErrorOutput converts a WorkflowError to ErrorOutput for catch handlers
func (e *WorkflowError) ToErrorOutput() ErrorOutput {
	return ErrorOutput{
		Error:   e.Type,
		Cause:   e.Cause,
		Details: e.Details,
	}
}
