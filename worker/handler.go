package worker

import "context"

// Handler executes a claimed run. Implementations are responsible for
// materializing the workflow engine from the opaque Spec bytes,
// choosing run vs. resume based on claim.Attempt, and reporting the
// final status back as an Outcome.
//
// A typical implementation:
//
//  1. Unmarshal claim.Spec into a workflow definition and inputs.
//  2. Build a *workflow.Execution with a Checkpointer, activities, etc.
//  3. Call exec.Run(ctx) on the first attempt or exec.Resume(ctx, id)
//     on subsequent attempts (falling back to Run on ErrNoCheckpoint).
//  4. Classify the returned result into an Outcome:
//     - ExecutionStatusCompleted -> StatusCompleted
//     - ExecutionStatusFailed    -> StatusFailed (set ErrorMessage)
//     - ExecutionStatusSuspended/Paused -> StatusSuspended
//
// The ctx passed to Handle is scoped to the run. It is cancelled when:
//   - The worker's parent context is cancelled.
//   - The run timeout elapses.
//   - The heartbeat goroutine detects lease loss.
//
// Handlers must respect ctx cancellation and return promptly.
// Handlers should not call QueueStore methods directly — the worker
// takes care of status persistence.
type Handler interface {
	Handle(ctx context.Context, claim *Claim) Outcome
}

// HandlerFunc adapts a plain function to the Handler interface.
type HandlerFunc func(ctx context.Context, claim *Claim) Outcome

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, claim *Claim) Outcome {
	return f(ctx, claim)
}
