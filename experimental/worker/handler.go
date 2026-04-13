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
//
// Lease fencing is **only** applied to Checkpointer. The other
// store fields are either append-only (ProgressStore,
// ActivityLogger) or globally shared (SignalStore), so they do
// not need to fence on (WorkerID, Attempt). Concrete stores may
// accept a *Claim in their factory method for symmetry and
// ignore it — that is expected, not a bug.
type HandlerContext struct {
	// Claim is the run being executed.
	Claim *Claim

	// Checkpointer is a lease-fenced workflow.Checkpointer scoped
	// to this claim. Writes that fail the (WorkerID, Attempt) fence
	// return worker.ErrLeaseLost. This is the only store in the
	// bundle that enforces lease fencing.
	Checkpointer workflow.Checkpointer

	// ProgressStore is a workflow.StepProgressStore scoped to this
	// claim. Step progress writes are derived observability data
	// and are not fenced — the "latest update wins" semantics mean
	// a stale writer cannot corrupt durable state.
	ProgressStore workflow.StepProgressStore

	// ActivityLogger is a workflow.ActivityLogger scoped to this
	// claim. Activity log writes are append-only and not fenced.
	ActivityLogger workflow.ActivityLogger

	// SignalStore is a workflow.SignalStore for signal/wait
	// coordination. Shared across runs and claims; not fenced.
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
//
// Of the three factory methods, **only NewCheckpointer is expected
// to return a lease-fenced store**. NewStepProgressStore and
// NewActivityLogger take a *Claim for API symmetry, but the
// returned implementations typically ignore the claim and return
// the shared store unchanged: step progress is write-through
// observability and activity logs are append-only, so neither
// needs fencing. SignalStore is not part of this factory at all —
// it is shared across claims and lives on Config directly.
type HandlerStores interface {
	// NewCheckpointer returns a lease-fenced checkpointer whose
	// writes must return worker.ErrLeaseLost when the claim's
	// (WorkerID, Attempt) pair no longer owns the run.
	NewCheckpointer(claim *Claim) workflow.Checkpointer

	// NewStepProgressStore returns a step progress store for the
	// claim. The claim is usually ignored; lease fencing is not
	// required because writes are idempotent replacements.
	NewStepProgressStore(claim *Claim) workflow.StepProgressStore

	// NewActivityLogger returns an activity logger for the claim.
	// The claim is usually ignored; activity logs are append-only.
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
