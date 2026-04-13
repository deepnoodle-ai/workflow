package worker

import (
	"context"
	"errors"
	"time"
)

// Status is the lifecycle status of a run in the queue.
//
// Terminal statuses (Completed, Failed) stop further processing.
// Suspended marks a run as dormant — waiting on a signal, sleep, or
// pause — and is not reclaimed by the reaper. Handlers re-enqueue
// suspended runs when external input arrives.
type Status string

const (
	// StatusQueued is a run waiting to be claimed.
	StatusQueued Status = "queued"
	// StatusRunning is a run actively executing under a worker lease.
	StatusRunning Status = "running"
	// StatusCompleted is a terminal success status.
	StatusCompleted Status = "completed"
	// StatusFailed is a terminal failure status.
	StatusFailed Status = "failed"
	// StatusSuspended is a non-terminal dormant status. The run is
	// waiting on external input (signal, sleep, pause). It is not
	// reclaimed by the reaper.
	StatusSuspended Status = "suspended"
	// StatusReview is a non-terminal dormant status. The run is
	// waiting for human review or approval. Like Suspended, it is
	// not reclaimed by the reaper.
	StatusReview Status = "review"
)

// ErrLeaseLost is returned by QueueStore operations that fence on
// (worker_id, attempt). It means another worker has since reclaimed
// the run, or the run has been dead-lettered by the reaper.
//
// Callers should treat ErrLeaseLost as a normal, expected condition:
// stop writing for this run and move on.
var ErrLeaseLost = errors.New("worker: lease lost")

// ErrTriggerAlreadyClaimed is returned by TriggerStore.MarkTriggerProcessing
// when a compare-and-swap finds no matching row, meaning another worker
// already claimed the trigger.
var ErrTriggerAlreadyClaimed = errors.New("worker: trigger already claimed")

// ErrWebhookAlreadyClaimed is returned by WebhookStore.MarkWebhookProcessing
// when a compare-and-swap finds no matching row, meaning another worker
// already claimed the webhook delivery.
var ErrWebhookAlreadyClaimed = errors.New("worker: webhook already claimed")

// NewRun is a run to enqueue. Spec is an opaque blob interpreted by
// the Handler, not by the worker.
//
// OrgID, ProjectID, ParentRunID, and InitiatedBy are nullable in the
// database. Empty string in the Go API means NULL in the database —
// single-tenant deployments should not invent sentinel values.
type NewRun struct {
	// ID uniquely identifies the run. Required. Must be unique
	// across the QueueStore.
	ID string

	// Spec is an opaque payload — typically JSON describing the
	// workflow definition and inputs — that the Handler consumes at
	// execution time.
	Spec []byte

	// OrgID identifies the organization owning this run. Empty
	// means the run is not scoped to an org (single-tenant).
	OrgID string

	// ProjectID identifies the project (workspace, team, board,
	// environment — whatever the consumer product calls it) that
	// owns this run. Empty means the run is not scoped to a project.
	ProjectID string

	// ParentRunID is the run that enqueued this one via the
	// trigger outbox. Empty for top-level runs.
	ParentRunID string

	// WorkflowType classifies the run (e.g., "research", "indexing").
	WorkflowType string

	// InitiatedBy identifies who or what triggered this run.
	InitiatedBy string

	// CreditCost is the credit cost for this run. Zero means no
	// credit tracking. Consumers typically default this to 1.
	CreditCost int

	// CallbackURL is an optional webhook URL notified on completion
	// or failure.
	CallbackURL string

	// Metadata is an arbitrary string map persisted alongside the
	// run. Typical use: correlation IDs, feature flags, tenant tags,
	// anything that does not earn a first-class column. Backed by
	// JSONB in the postgres store.
	Metadata map[string]string
}

// Claim is a run that has been atomically claimed by a worker and
// transitioned from StatusQueued to StatusRunning. A Claim is also
// the unit of lease fencing: QueueStore writes fence on
// (WorkerID, Attempt), and passing a *Claim into those calls gives
// the store full access to the run's metadata without an out-of-band
// lookup.
type Claim struct {
	// ID is the run's stable identifier, also used as the workflow
	// engine's ExecutionID.
	ID string

	// Spec is the opaque payload supplied when the run was enqueued.
	Spec []byte

	// Attempt is the 1-based attempt counter. First claim sets
	// Attempt = 1; each subsequent reclaim increments it.
	Attempt int

	// WorkerID is the worker that holds the lease. Populated by
	// ClaimQueued and used for fencing subsequent writes.
	WorkerID string

	// OrgID is the organization that owns this run.
	OrgID string

	// ProjectID is the project that owns this run.
	ProjectID string

	// ParentRunID is the parent run that enqueued this one, or
	// empty for top-level runs.
	ParentRunID string

	// WorkflowType classifies the run.
	WorkflowType string

	// InitiatedBy identifies who or what triggered this run.
	InitiatedBy string

	// CreditCost is the credit cost for this run.
	CreditCost int

	// CallbackURL is the webhook URL to notify on terminal status.
	CallbackURL string

	// Metadata carries the arbitrary tag map supplied at enqueue.
	Metadata map[string]string
}

// Outcome is the terminal (or dormant) state a Handler reports back
// to the worker after executing a claim.
type Outcome struct {
	// Status is the final status to persist. Must be one of
	// StatusCompleted, StatusFailed, StatusSuspended, StatusReview.
	Status Status

	// Result is an optional opaque blob persisted alongside the
	// status. Typical use: JSON outputs, SuspensionInfo, etc.
	Result []byte

	// ErrorMessage is the human-readable failure reason. Set when
	// Status == StatusFailed; ignored otherwise.
	ErrorMessage string

	// Triggers lists child runs to enqueue via the outbox pattern
	// after the run completes. Ignored if no TriggerStore is
	// configured on the Worker.
	Triggers []NewRun
}

// DeadLetteredRun is a run transitioned from running to failed by
// the reaper after exhausting its retry budget. Returned from
// DeadLetterStale so the worker can refund credits inline.
type DeadLetteredRun struct {
	ID           string
	OrgID        string
	WorkflowType string
	CreditCost   int
}

// FailedRun is a credit-tracking failed run returned by
// ListFailedWithCredits. Used by the reconcile loop as a backstop
// for DeadLetterStale's inline refund.
type FailedRun struct {
	ID           string
	OrgID        string
	WorkflowType string
	CreditCost   int
}

// QueueStore is the persistence contract a backing store must satisfy
// to power a Worker. Implementations are free to use any database,
// message bus, or in-memory structure — the worker only depends on
// this interface.
//
// Concurrency contract:
//
//   - ClaimQueued must be atomic: two workers calling it concurrently
//     must never receive the same run.
//   - Heartbeat, SaveCheckpoint, and Complete must fence on
//     (WorkerID, Attempt). Writes that fail the fencing check must
//     return ErrLeaseLost.
//   - ReclaimStale and DeadLetterStale must honor excludeIDs: runs
//     whose IDs appear in the list must not be transitioned, even if
//     their heartbeats look stale. This protects against DB write
//     contention where a heartbeat write is delayed but the run is
//     in fact still healthy in-process.
type QueueStore interface {
	// Enqueue inserts a new run in StatusQueued with attempt = 0.
	// Returns an error if a run with the same ID already exists.
	Enqueue(ctx context.Context, run NewRun) error

	// ClaimQueued atomically claims the oldest available StatusQueued
	// run for the given worker, transitioning it to StatusRunning
	// and incrementing its attempt counter.
	//
	// Returns (nil, nil) when no queued runs are available.
	ClaimQueued(ctx context.Context, workerID string) (*Claim, error)

	// Heartbeat refreshes the lease on a claimed run. Must fence on
	// (claim.WorkerID, claim.Attempt) and status == StatusRunning.
	Heartbeat(ctx context.Context, claim *Claim) error

	// Complete writes the terminal or dormant status for a claimed
	// run. Must fence on (claim.WorkerID, claim.Attempt).
	Complete(ctx context.Context, claim *Claim, outcome Outcome) error

	// ReclaimStale transitions StatusRunning runs whose heartbeats
	// are older than staleBefore back to StatusQueued, for runs with
	// attempt < maxAttempts. Runs whose IDs appear in excludeIDs are
	// never transitioned.
	//
	// Returns the number of runs reclaimed.
	ReclaimStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) (int, error)

	// DeadLetterStale transitions StatusRunning runs whose heartbeats
	// are older than staleBefore to StatusFailed, for runs with
	// attempt >= maxAttempts. Runs whose IDs appear in excludeIDs
	// are never transitioned.
	//
	// Returns metadata for each dead-lettered run. The worker uses
	// this to emit observability events and refund credits inline.
	DeadLetterStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) ([]DeadLetteredRun, error)

	// ListFailedWithCredits returns failed runs that were debited
	// but have not yet been refunded. Used by the credit reconcile
	// loop as a backstop for DeadLetterStale's inline refund.
	//
	// Implementations that do not track credits can return an empty
	// slice — the reconcile loop will do nothing.
	ListFailedWithCredits(ctx context.Context, limit int) ([]FailedRun, error)
}
