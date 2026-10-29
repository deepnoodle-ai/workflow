// Package hosttest is the test suite every durable host backend runs. It
// drives the shared core (internal/hostcore) through a [Harness] that each
// backend module implements in its own tests:
//
//	func TestSuite(t *testing.T) { hosttest.Run(t, harness{}) }
//
// It cannot import a backend, since the root module does not depend on
// them. A backend that differs declares how in [Caps], and the suite
// asserts the declared behaviour.
package hosttest

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

// Caps are what a backend declares where it differs (PRD 007's
// differences table).
type Caps struct {
	// RowLocks: a run lock blocks only that run (Postgres). False: it
	// blocks every write to the database (SQLite).
	RowLocks bool
	// CancelEndsLockWait: a cancelled context ends a wait for a lock.
	// False for a driver where a write waits up to BusyTimeout.
	CancelEndsLockWait bool
	// BusyTimeout bounds a write's wait for a lock when CancelEndsLockWait
	// is false.
	BusyTimeout time.Duration
}

// Options configure a store a harness opens.
type Options struct {
	Limits   workflow.Limits
	NewRunID func() string
	// OnFinished, when set, is the store's finish hook. The harness calls
	// it in the transaction that finishes the run.
	OnFinished func(ctx context.Context, f durable.Finished) error
	// OnFinishedSQL, when set, is the store's WithOnFinishedSQL hook.
	OnFinishedSQL func(ctx context.Context, tx durable.SQLTx, f durable.Finished) error
}

// JobRow is an outbox row as a test reads it.
type JobRow struct {
	ID           int64
	StepID       workflow.StepID
	Kind         string // "step" or "timer"
	StepKind     string
	Claims       int
	Fence        int64
	LeaseOwner   string    // empty: not leased
	LeaseExpires time.Time // zero: not leased
	RunAt        time.Time
}

// Harness is one backend under test.
type Harness interface {
	// Name names the backend in subtest names: "postgres".
	Name() string
	Caps() Caps
	// Open returns a migrated store on a fresh database: a new schema on
	// Postgres, a new file on SQLite. It skips t when the backend is not
	// available.
	Open(t *testing.T, kinds workflow.Kinds, opt Options) *Env
	// Reopen returns another store on env's database, with its own kinds
	// and options and its own connections: a second process, or a worker
	// on another version of the code.
	Reopen(t *testing.T, env *Env, kinds workflow.Kinds, opt Options) *Env

	// Test access to rows the API does not show.
	//
	// Jobs lists a run's jobs, by StepID then kind.
	Jobs(t *testing.T, env *Env, runID string) []JobRow
	// Count counts the rows of table ("definitions", "runs", "steps",
	// "events" or "jobs") that belong to runID, or all rows when runID is
	// empty.
	Count(t *testing.T, env *Env, table, runID string) int
	// ExpireLeases makes the leases of a run's jobs expired.
	ExpireLeases(t *testing.T, env *Env, runID string)
	// Backdate makes a run's jobs due ago before now.
	Backdate(t *testing.T, env *Env, runID string, ago time.Duration)
	// HoldRunLock locks the run on a separate connection, as a slow
	// application transaction would, until release is called. On SQLite
	// that holds the database's write lock.
	HoldRunLock(t *testing.T, env *Env, runID string) (release func())
	// CallerTx opens an application transaction (a SQLTx) and the
	// hostcore.Tx on it. The harness rolls it back when the test ends if
	// neither commit nor rollback ran.
	CallerTx(
		t *testing.T, env *Env,
	) (tx durable.SQLTx, htx hostcore.Tx, commit, rollback func() error)

	// Worker returns the backend's own Worker for w, so that the suite
	// runs what users run. Each time the Worker syncs, it first takes w's
	// fields and w.Hooks() (w.Store is an Env from this harness, or nil),
	// then runs its own sync, then writes back to w each default that
	// changed: ID, Concurrency, Lease, Poll, MaxClaims and Logger.
	Worker(w *Worker) BackendWorker

	// TxOn is the hostcore.Tx on the application's transaction tx, as the
	// SQLTx methods (StartSQLTx, DeliverSQLTx) use it.
	TxOn(env *Env, tx durable.SQLTx) (hostcore.Tx, error)
}

// Env is one store under test: the backend, the shared core on it, and
// the methods of a backend's public Store.
type Env struct {
	Backend hostcore.Backend
	Store   *hostcore.Store
	// Handle is the harness's own value, such as the backend's Store.
	Handle any

	h Harness
}

func (e *Env) write(
	ctx context.Context, fn func(tx hostcore.Tx) (*durable.Result, error),
) (*durable.Result, error) {
	var res *durable.Result
	err := e.Backend.Write(ctx, func(tx hostcore.Tx) (err error) {
		res, err = fn(tx)
		return err
	})
	return res, err
}

// Start starts a run in its own transaction.
func (e *Env) Start(ctx context.Context, run durable.NewRun) (*durable.Result, error) {
	return e.write(ctx, func(tx hostcore.Tx) (*durable.Result, error) {
		return e.Store.Start(ctx, tx, run)
	})
}

// Deliver applies an event in its own transaction.
func (e *Env) Deliver(
	ctx context.Context, runID string, ev workflow.Event,
) (*durable.Result, error) {
	return e.write(ctx, func(tx hostcore.Tx) (*durable.Result, error) {
		return e.Store.Deliver(ctx, tx, runID, ev)
	})
}

// Cancel cancels a run in its own transaction.
func (e *Env) Cancel(ctx context.Context, runID, reason string) (*durable.Result, error) {
	return e.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

func (e *Env) GetRun(ctx context.Context, runID string) (*durable.Run, error) {
	return e.Backend.GetRun(ctx, runID)
}

func (e *Env) Steps(ctx context.Context, runID string) ([]durable.StepRecord, error) {
	return e.Backend.Steps(ctx, runID)
}

func (e *Env) Events(ctx context.Context, runID string) ([]workflow.Event, error) {
	return e.Backend.Events(ctx, runID)
}

func (e *Env) QueueStats(ctx context.Context) ([]durable.QueueStat, error) {
	return e.Backend.QueueStats(ctx)
}

// caps are the backend's declared differences.
func (e *Env) caps() Caps { return e.h.Caps() }

// reopen is Harness.Reopen with env's harness.
func (e *Env) reopen(t *testing.T, kinds workflow.Kinds, opt Options) *Env {
	t.Helper()
	o := e.h.Reopen(t, e, kinds, opt)
	o.h = e.h
	return o
}

// open opens a store with the suite's kinds.
func open(t *testing.T, h Harness) *Env {
	t.Helper()
	return openWith(t, h, kinds, Options{})
}

func openWith(t *testing.T, h Harness, k workflow.Kinds, opt Options) *Env {
	t.Helper()
	e := h.Open(t, k, opt)
	e.h = h
	return e
}

// Run runs every test of the suite against h, each as a parallel subtest.
// Each opens its own database, and most of their time is lease and poll
// waits, so they overlap well.
func Run(t *testing.T, h Harness) {
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fn(t, h)
		})
	}
}
