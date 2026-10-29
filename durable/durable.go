// Package durable holds the types that every durable host backend shares:
// what a store returns, what a worker reports, and the sentinel errors.
//
// You need this package only for code that runs on either backend. Each
// backend re-exports these names: postgres.Result is durable.Result, and
// errors.Is(err, durable.ErrRunNotFound) holds for the error any backend
// returns for a missing run.
package durable

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

var (
	// ErrRunNotFound: no run has this ID.
	ErrRunNotFound = errors.New("workflow: run not found")
	// ErrRunExists: Start was given the ID of a run that exists with a
	// different definition.
	ErrRunExists = errors.New("workflow: a run with this id exists for a different definition")
	// ErrUnstorable is returned by Start when its input or metadata hold a
	// value the store refuses: a NUL character (\u0000), which Postgres
	// cannot store as jsonb. Every backend refuses it, so a run that starts
	// on one starts on the others.
	ErrUnstorable = errors.New("workflow: value cannot be stored")
	// ErrDefinitionUnavailable means the store cannot load a run's
	// definition: its kinds lack a kind the definition uses, or the
	// document no longer validates. It is a host configuration problem.
	// Start returns it and starts nothing; Deliver returns it; a Worker
	// gives the configuration MaxClaims leases to be fixed, then fails the
	// run with type definition_unavailable.
	ErrDefinitionUnavailable = errors.New("workflow: the run's definition cannot be loaded")
	// ErrNotClaimable is the error Execute returns for a job that exists
	// but that it must not claim now: another worker holds its lease, it is
	// not due yet, this worker has no handler for its step kind, or the
	// worker is not claiming (Shutdown, MayClaim). The message says which.
	// Retry later: an HTTP handler answers 503 so that the task queue
	// retries.
	ErrNotClaimable = errors.New("workflow: the job cannot be claimed now")
	// ErrHardStop is returned by Shutdown when the context of a Run call
	// ended before Shutdown finished. That Run stopped hard: it cancelled
	// its active jobs at once instead of letting them run until the
	// Shutdown deadline. Give Run a context that you cancel only for a hard
	// stop, not the one that a SIGTERM cancels.
	ErrHardStop = errors.New("workflow: Run's context ended before Shutdown finished; " +
		"active jobs were cancelled at once (a hard stop)")
)

// ErrorTypeDefinitionUnavailable is the RunError type of a run a Worker
// failed because its definition could not be loaded.
const ErrorTypeDefinitionUnavailable = "definition_unavailable"

// EventAborted is the kind of the event log entry a Worker records when it
// fails a run that Apply cannot advance: its definition cannot be loaded,
// or a timer could not be fired MaxClaims times. It is never passed to
// Apply; Reason says why.
const EventAborted workflow.EventKind = "aborted"

// NewRun describes a run to start.
type NewRun struct {
	// ID identifies the run. Derive it from what caused the run (for
	// example "pr-42-abc123" for a pull request event) to make starting
	// idempotent; empty generates one (see the store's WithRunIDs).
	ID         string
	Definition *workflow.Definition
	// Input is the run's input, read as `input`. It is kept in the
	// run's state.
	Input    map[string]any
	Metadata map[string]any
}

// Result is what an event did to a run.
type Result struct {
	// RunID is the run's ID: NewRun.ID, or the one Start generated.
	RunID string
	// State is the run's state after the event.
	State *workflow.State
	// Commands are what Apply produced. Dispatches, timers and step
	// cancellations are already in the outbox, committed with the state;
	// workers carry them out. Do not execute them again. Terminal commands
	// (RunCompleted, RunFailed, RunCancelled) are reported to the store's
	// finish hook (and, on Postgres, to RunsChannel).
	Commands []workflow.Command
	// Applied reports whether the event changed the run. It is false for a
	// duplicate, for a result of an attempt that is no longer wanted (it
	// timed out, was cancelled or was retried), for an event on a finished
	// run, and for a Start of a run that already exists. An HTTP handler
	// that records an approval can answer "already decided" when it is
	// false.
	Applied bool
}

// Finished describes a run that has just reached a terminal status. A run
// with finally steps finishes once they are done.
type Finished struct {
	RunID   string
	Status  workflow.RunStatus // completed, failed or cancelled
	Outputs map[string]any     // completed
	Error   *workflow.RunError // failed
	Reason  string             // cancelled
	// FinallyErrors lists the finally steps that failed or did not run.
	// They do not change Status; alert on them, since cleanup did not
	// finish.
	FinallyErrors []*workflow.RunError
}

// SQLTx is a database/sql transaction on the store's database: a *sql.Tx,
// or an ORM's transaction that runs statements on one. The store passes it
// only plain argument values.
//
// An Ent *Tx satisfies SQLTx when Ent generates it with the sql/execquery
// feature, which adds ExecContext and QueryContext. Without the feature,
// passing it fails to compile with "missing method ExecContext".
//
// The store runs its statements with ExecContext and QueryContext. It never
// calls Commit or Rollback: they are in the interface so that a *sql.DB,
// which runs each statement in a transaction of its own, does not compile.
type SQLTx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Commit() error
	Rollback() error
}

// Run is a run as stored.
type Run struct {
	ID       string
	Workflow string
	// DefinitionHash is the hash of the definition the run started with.
	DefinitionHash string
	Status         workflow.RunStatus
	State          *workflow.State
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FinishedAt     *time.Time
}

// StepStatus is what became of a step attempt.
type StepStatus string

const (
	// StepDispatched: the attempt was dispatched and has not settled.
	StepDispatched StepStatus = "dispatched"
	StepCompleted  StepStatus = "completed"
	StepFailed     StepStatus = "failed"
	// StepCancelled: the run no longer wanted the attempt, because it was
	// cancelled or failed while the attempt was in flight. An attempt that
	// timed out is failed, with type timeout.
	StepCancelled StepStatus = "cancelled"
)

// StepRecord is one step attempt as stored. Unlike the run's state, which
// keeps only what the run can still read, the steps table keeps every
// attempt: it is the run's history.
type StepRecord struct {
	StepID       workflow.StepID
	Step         string
	StepKind     string // "agent", "action", ...: the step's kind
	Index        int
	Attempt      int
	Status       StepStatus
	Params       map[string]any
	Item         any
	Output       any
	Error        *workflow.StepError
	NotBefore    *time.Time
	DispatchedAt time.Time
	SettledAt    *time.Time
}
