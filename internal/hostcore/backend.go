// Package hostcore is the durable host's policy, shared by every backend:
// how an event becomes state, step records and jobs ([Store]), and how a
// worker claims, runs, settles and gives back jobs ([Worker]). A backend
// implements the storage contract ([Backend] and [Tx]) with its own SQL and
// wraps the core in its public API.
//
// The contract is internal on purpose: it changes with the backends, which
// release together with the root module.
package hostcore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
)

// Lease names one claim of a job: its row and the fence the claim took.
type Lease struct{ JobID, Fence int64 }

// ErrLeaseLost: a fenced write changed no row because a newer claim holds
// the job, or because the existence check failed. The worker adds its
// backend's prefix where the error reaches a caller.
var ErrLeaseLost = errors.New("lease lost")

// ErrJobGone: a fenced write changed no row because the job no longer
// exists. It matches ErrLeaseLost, so code that only needs to know the
// claim is over treats both alike. The worker reports a job found gone
// after its lease ran out as ErrLeaseLost: another claim may have settled
// it.
var ErrJobGone = fmt.Errorf("%w: the job is gone", ErrLeaseLost)

// Claimed is a job a claim leased.
type Claimed struct {
	Lease
	workflow.Job
	DefHash string    // the run's definition
	At      time.Time // when the claim was made, no later than the store's clock; the lease runs from here

	ran     time.Duration // how long the handler ran
	renewed time.Time     // when the heartbeat last extended the lease
	// request marks a claim whose context is a request (RunOnce, Execute),
	// not a Run's. A request that ends cuts the claim off and counts it
	// toward MaxClaims, and its heartbeat runs until the handler returns.
	request bool
}

// ClaimRequest is what a claim asks for.
type ClaimRequest struct {
	Owner  string        // the worker's ID, for lease_owner
	Queues []string      // "timer", "step:<kind>"
	Limit  int           // at most this many jobs
	Lease  time.Duration // the lease each claim takes
	DueBy  time.Time     // zero: due now. RunOnce passes the time it started.
}

// JobStatus is a job as JobStatus read it, without a lock.
type JobStatus struct {
	Found        bool
	Queue        string
	RunAt        time.Time
	LeaseExpires time.Time // zero: no lease
	Now          time.Time // the store's clock when it read the job
}

// JobKind is the jobs.kind of the job ref names: "timer" or "step".
func JobKind(ref durable.JobRef) string {
	if ref.Timer {
		return "timer"
	}
	return "step"
}

// Backend is the store's database outside a transaction.
//
// The contract, which every backend keeps:
//
//  1. A fenced write (Renew, Release, Postpone, Tx.DeleteJob) that changes
//     no row returns ErrJobGone when no row has the job's ID, and
//     ErrLeaseLost when the row has another fence or the check fails. Job
//     IDs are never reused, so a missing ID stays missing, and the check
//     can run as a separate statement after the write.
//  2. Every claim adds 1 to claims and to fence. A fence never shrinks.
//  3. Claim returns jobs whose run_at is at or before both the store's
//     clock and DueBy, and whose lease is free, from Queues only, earliest
//     run_at then ID first, at most Limit. Two concurrent claims never
//     return the same job. ClaimKey is exclusive with Claim and itself.
//  4. Tx.LockRun serializes every writer of one run until commit.
//  5. Tx.InsertStep and Tx.InsertJob skip a row whose key exists.
//  6. A read inside Write goes through the Tx, never the pool.
//  7. Release with refund subtracts 1 from claims; without, it only
//     clears the lease.
//
// Methods return database errors as they are; the core adds context.
type Backend interface {
	// Write runs fn in a transaction and commits if fn returns nil.
	Write(ctx context.Context, fn func(Tx) error) error

	// Claim leases due jobs (rule 3).
	Claim(ctx context.Context, req ClaimRequest) ([]Claimed, error)
	// ClaimKey leases the job ref names if it is due, its lease is free,
	// and its queue is in req.Queues. It returns nil, nil when it cannot.
	ClaimKey(ctx context.Context, ref durable.JobRef, req ClaimRequest) (*Claimed, error)
	// JobStatus reads the job ref names, to say why ClaimKey could not
	// claim it.
	JobStatus(ctx context.Context, ref durable.JobRef) (JobStatus, error)
	// Now is the clock that leases and due times use.
	Now(ctx context.Context) (time.Time, error)

	// Renew extends a lease to d from now (rule 1).
	Renew(ctx context.Context, l Lease, d time.Duration) error
	// Release clears a lease (rules 1 and 7).
	Release(ctx context.Context, l Lease, refund bool) error
	// Postpone clears a lease and makes the job due d from now (rule 1).
	Postpone(ctx context.Context, l Lease, d time.Duration) error

	// Unstorable reports whether err means the database refused a value,
	// so the worker fails the step with invalid_output instead of retrying.
	Unstorable(err error) bool

	// DefinitionDoc returns the document stored under hash, or nil, nil.
	DefinitionDoc(ctx context.Context, hash string) ([]byte, error)
	// GetRun returns durable.ErrRunNotFound for a missing run.
	GetRun(ctx context.Context, runID string) (*durable.Run, error)
	Steps(ctx context.Context, runID string) ([]durable.StepRecord, error)
	Events(ctx context.Context, runID string) ([]workflow.Event, error)
	QueueStats(ctx context.Context) ([]durable.QueueStat, error)

	// ListRuns returns at most q.Limit runs that match q, after q.After
	// when it is set, newest created_at first, then ID descending.
	ListRuns(ctx context.Context, q RunQuery) ([]durable.RunSummary, error)
	// DeleteEndedRuns deletes, in one transaction, at most limit runs whose
	// status is terminal and whose updated_at and finished_at are before
	// before, with their steps, events and jobs. It returns how many runs
	// it deleted.
	DeleteEndedRuns(ctx context.Context, before time.Time, limit int) (int, error)
}

// Tx is one transaction. JSON values are encoded by the core; a nil slice
// is SQL NULL. A zero time.Time is NULL too, except where a method says
// otherwise.
type Tx interface {
	// LockRun locks the run's row until commit and reads its state and
	// definition hash. It returns durable.ErrRunNotFound for a missing run.
	LockRun(ctx context.Context, runID string) (state []byte, defHash string, err error)
	// DefinitionDoc returns the document stored under hash, or nil, nil.
	DefinitionDoc(ctx context.Context, hash string) ([]byte, error)
	// InsertDefinition stores a definition, unless its hash is stored.
	InsertDefinition(ctx context.Context, hash, name string, doc []byte) error
	// InsertRun inserts a run with event_seq 1. It returns false, and
	// changes nothing, when a run with this ID exists.
	InsertRun(ctx context.Context, r RunRow) (bool, error)
	// UpdateRun stores a run's new state, adds 1 to its event_seq, and
	// returns the new event_seq.
	UpdateRun(ctx context.Context, runID string, u RunUpdate) (seq int64, err error)
	InsertEvent(ctx context.Context, runID string, e EventRow) error
	// InsertStep inserts a dispatched step record (rule 5).
	InsertStep(ctx context.Context, runID string, s StepRow) error
	// SettleStep settles a step record that is still dispatched.
	SettleStep(ctx context.Context, runID string, s StepSettle) error
	// CancelSteps cancels the dispatched step record stepID, or every
	// dispatched record of the run when stepID is empty.
	CancelSteps(ctx context.Context, runID string, stepID workflow.StepID, at time.Time) error
	// InsertJob inserts a job (rule 5). A zero RunAt means now, by the
	// store's clock.
	InsertJob(ctx context.Context, runID string, j JobRow) error
	// DeleteJobs deletes the jobs of stepID, or every job of the run when
	// stepID is empty.
	DeleteJobs(ctx context.Context, runID string, stepID workflow.StepID) error
	// DeleteJob deletes the job of a claim (rule 1).
	DeleteJob(ctx context.Context, l Lease) error
	// JobsWritten says, once per transaction that wrote jobs, that
	// workers may have work. Postgres notifies on commit.
	JobsWritten(ctx context.Context, runID string) error
	// StatusChanged says a run's status changed. Postgres notifies on
	// commit; a backend without notifications does nothing.
	StatusChanged(ctx context.Context, runID string, status workflow.RunStatus) error
	// OnFinished calls the application's finish hook, if any, with the
	// transaction this Tx runs on.
	OnFinished(ctx context.Context, f durable.Finished) error
}

// RunRow is a new run.
type RunRow struct {
	ID             string
	DefinitionHash string
	Workflow       string
	Status         workflow.RunStatus
	State          []byte
	Error          []byte
	Outputs        []byte
	Metadata       []byte
	At             time.Time // created_at and updated_at
	FinishedAt     time.Time
}

// RunUpdate is a run's new state.
type RunUpdate struct {
	Status     workflow.RunStatus
	State      []byte
	Error      []byte
	Outputs    []byte
	FinishedAt time.Time
	At         time.Time // updated_at
}

// EventRow is one entry of a run's event log.
type EventRow struct {
	Seq    int64
	Kind   workflow.EventKind
	StepID workflow.StepID // empty is NULL
	At     time.Time
	Event  []byte
}

// StepRow is a dispatched step attempt.
type StepRow struct {
	StepID       workflow.StepID
	Step         string
	Kind         string // the step's kind
	Index        int
	Attempt      int
	Params       []byte
	Item         []byte
	NotBefore    time.Time
	DispatchedAt time.Time
}

// StepSettle is what became of a step attempt.
type StepSettle struct {
	StepID workflow.StepID
	Status durable.StepStatus
	Output []byte
	Error  []byte
	At     time.Time
}

// JobRow is an outbox job.
type JobRow struct {
	StepID   workflow.StepID
	Kind     string // "step" or "timer"
	StepKind string
	Command  []byte
	RunAt    time.Time // zero: now, by the store's clock
}
