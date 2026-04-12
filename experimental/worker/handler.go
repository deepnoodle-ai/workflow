package worker

import (
	"context"

	"github.com/deepnoodle-ai/workflow"
)

// HandlerContext carries everything a Handler needs to execute a
// claimed run. The worker constructs it once per claim, wires in
// any pre-fenced stores from HandlerStores, and hands the whole
// bundle to Handle.
//
// All store fields are optional. They are populated only when the
// worker's Config.Stores factory is set and returns a non-nil
// value for that concern. A Handler backed by an in-memory engine
// can ignore the store fields entirely.
type HandlerContext struct {
	// Claim is the run being executed.
	Claim *Claim

	// Checkpointer is a lease-fenced workflow.Checkpointer scoped
	// to this claim. Writes that fail the (WorkerID, Attempt) fence
	// return worker.ErrLeaseLost.
	Checkpointer workflow.Checkpointer

	// ProgressStore is a lease-fenced workflow.StepProgressStore
	// scoped to this claim.
	ProgressStore workflow.StepProgressStore

	// ActivityLogger is a workflow.ActivityLogger scoped to this
	// claim. Activity log writes are append-only and not fenced.
	ActivityLogger workflow.ActivityLogger

	// SignalStore is a workflow.SignalStore for signal/wait
	// coordination. Shared across runs; not fenced.
	SignalStore workflow.SignalStore
}

// HandlerStores is an optional factory that the worker uses to
// build a HandlerContext's store fields from a Claim. Backing
// stores that speak the workflow engine's persistence interfaces
// (postgres, sqlite) implement this — the memstore does not.
//
// Each method returns nil when the factory does not support that
// concern; the worker propagates the nil into HandlerContext so
// the Handler can check for availability.
type HandlerStores interface {
	NewCheckpointer(claim *Claim) workflow.Checkpointer
	NewStepProgressStore(claim *Claim) workflow.StepProgressStore
	NewActivityLogger(claim *Claim) workflow.ActivityLogger
}

// Handler executes a claimed run. Implementations are responsible for
// materializing the workflow engine from the opaque Spec bytes,
// choosing run vs. resume based on claim.Attempt, and reporting the
// final status back as an Outcome.
//
// A typical implementation:
//
//  1. Unmarshal hc.Claim.Spec into a workflow definition and inputs.
//  2. Build a *workflow.Execution with hc.Checkpointer, hc.ProgressStore,
//     hc.ActivityLogger, and any activities the consumer registers.
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
	Handle(ctx context.Context, hc *HandlerContext) Outcome
}

// HandlerFunc adapts a plain function to the Handler interface.
type HandlerFunc func(ctx context.Context, hc *HandlerContext) Outcome

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, hc *HandlerContext) Outcome {
	return f(ctx, hc)
}
