package workflow

import (
	"context"
	"errors"
)

// Job is a step attempt as a host hands it to a [Handler]: the dispatch
// command and the run it belongs to. The local and postgres hosts both call
// handlers with a Job, so the same handler runs in tests and in
// production.
type Job struct {
	// RunID is the host's ID for the run. A StepID is unique only within
	// its run, so a key for work outside the host needs both: RunID with
	// StepID for this attempt, RunID with StepID.Unit() for an idempotency
	// key that a retry keeps.
	RunID string
	// Claims is how many times a host has handed this attempt to a
	// handler, this time included. More than one means an earlier call may
	// have done part of the work: the postgres host reclaims the job of a
	// worker that lost its lease, and the local host dispatches the work in
	// flight again when it resumes a saved run.
	Claims int
	Command
}

// Handler runs one step attempt and returns its output. Return a
// *[StepError] to set the error type and whether it is retryable; any other
// error is reported as a retryable error of type "error" (see
// [StepErrorOf]). A step is retried only if it declares retry. Return
// [ErrDeferred] when the result will be delivered later.
//
// ctx is cancelled when the attempt is no longer wanted: the run was
// cancelled or failed, the step timed out, or the host lost its claim.
// An attempt can run more than once (see Job.Claims), so make its effects
// idempotent by the keys Job describes.
type Handler func(ctx context.Context, job Job) (any, error)

// ErrDeferred tells the host that a handler handed the step off and its
// result arrives later, as a StepCompleted or StepFailed event the host
// delivers: an agent turn on another service, a human approval, a webhook.
// The step stays in flight until then, or until its timeout fires.
var ErrDeferred = errors.New("workflow: the step completes later")

// StepErrorOf is the StepError a host reports for a handler's error: a
// *StepError in err's chain as it is, and anything else as a retryable
// error of type "error".
func StepErrorOf(err error) *StepError {
	var se *StepError
	if errors.As(err, &se) {
		return se
	}
	return &StepError{Type: "error", Message: err.Error(), Retryable: true}
}
