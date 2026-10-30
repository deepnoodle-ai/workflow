// Package postgres is a durable host for workflows on Postgres.
//
// No process holds a run. Each event advances a run in one short
// transaction: lock the run row, call [workflow.Apply], store the new state,
// upsert step records, and write the commands to an outbox (the jobs
// table). Workers claim jobs after commit, run the step handlers, and
// deliver the results as new events. Any worker can advance any run, a run
// can wait for days between events, and a crash loses nothing: an expired
// lease makes the job claimable again.
//
// Every *Tx method takes the caller's transaction, so an application can
// start a run, or deliver a step result, atomically with its own writes.
// StartTx, DeliverTx and CancelTx take a pgx.Tx; StartSQLTx, DeliverSQLTx
// and CancelSQLTx take a database/sql transaction ([SQLTx]). The methods
// without Tx open their own transaction.
package postgres

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"hash/fnv"
	"regexp"
	"runtime"
	"strings"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schemaSQL string

var schemaRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Store persists runs and their outbox.
type Store struct {
	pool   *pgxpool.Pool
	kinds  workflow.Kinds
	schema string
	limits workflow.Limits

	onFinished    func(ctx context.Context, tx pgx.Tx, f Finished) error
	onFinishedSQL func(ctx context.Context, tx SQLTx, f Finished) error
	newRunID      func() string

	core    *hostcore.Store // the persistence policy, shared with every backend
	backend pgBackend
	// sqlDB is set when the store's only finish hook is WithOnFinishedSQL:
	// the store then opens its transactions through database/sql on its
	// own pool, so that hook runs on every path.
	sqlDB *sql.DB
}

// Option configures a Store.
type Option func(*Store)

// WithSchema puts the tables in a schema other than "workflow".
func WithSchema(name string) Option { return func(s *Store) { s.schema = name } }

// WithLimits sets the state limits for every definition the store loads.
// The store loads each definition itself, from its document, so the limits
// a caller loaded NewRun.Definition with do not apply.
func WithLimits(l workflow.Limits) Option { return func(s *Store) { s.limits = l } }

// WithOnFinished calls fn in the transaction that moves a run to completed,
// failed or cancelled, once per run. Use it to record the outcome or enqueue
// a notification atomically with it. An error from fn rolls the event back,
// so return one only when the transaction itself failed; a worker retries
// its settle, and the event is applied once fn succeeds. An error that
// wraps one of Apply's rejections is not retried: the worker logs it and
// records the finish without it. A store with fn refuses StartSQLTx,
// DeliverSQLTx and CancelSQLTx until it also has [WithOnFinishedSQL],
// since a run can finish in their transaction.
//
// For code that also runs on the sqlite module, use only
// [WithOnFinishedSQL]: one hook then runs on every path, on both backends.
func WithOnFinished(fn func(ctx context.Context, tx pgx.Tx, f Finished) error) Option {
	return func(s *Store) { s.onFinished = fn }
}

// WithRunIDs sets how Start makes the ID of a run whose NewRun.ID is
// empty. The default is a UUIDv7 string, so IDs sort by start time.
func WithRunIDs(fn func() string) Option { return func(s *Store) { s.newRunID = fn } }

// New returns a Store. kinds validates every definition the store loads.
func New(pool *pgxpool.Pool, kinds workflow.Kinds, opts ...Option) (*Store, error) {
	s := &Store{pool: pool, kinds: kinds, schema: "workflow"}
	for _, o := range opts {
		o(s)
	}
	if !schemaRE.MatchString(s.schema) {
		return nil, fmt.Errorf("postgres: invalid schema name %q", s.schema)
	}
	if s.onFinishedSQL != nil && s.onFinished == nil {
		// The one hook runs on every path, so the store opens its own
		// transactions through database/sql too.
		s.sqlDB = stdlib.OpenDBFromPool(pool)
		// A *sql.DB keeps a goroutine until Close, and a Store has no
		// Close: close it once nothing can reach the store.
		runtime.AddCleanup(s, func(db *sql.DB) { _ = db.Close() }, s.sqlDB)
	}
	s.core = &hostcore.Store{
		Kinds:    s.kinds,
		Limits:   s.limits,
		NewRunID: s.newRunID,
		Prefix:   "postgres",
	}
	s.backend = pgBackend{s: s}
	return s, nil
}

// migrations are the schema's versions, in order: migrations[i] takes the
// schema from version i to i+1. Append; never edit one that has shipped.
var migrations = []string{schemaSQL, runsCreatedSQL}

// runsCreatedSQL is migration 2: the index ListRuns pages by. Migrate
// runs in a transaction, so the build locks the runs table against writes
// while it runs. On a large table, build the index first with CREATE
// INDEX CONCURRENTLY under the same name; this migration then does nothing.
const runsCreatedSQL = `
CREATE INDEX IF NOT EXISTS runs_created ON {{schema}}.runs (created_at, id);`

// Migrate brings the schema up to date. It is safe to call from every
// process at startup: migrations run in one transaction under an advisory
// lock, each at most once, and are recorded in schema_migrations. Migrate
// fails if the database is at a newer version than this package knows.
func (s *Store) Migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Transaction-scoped, so it is released on commit or rollback and
		// works through a transaction-mode pooler.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`,
			migrateLockKey(s.schema)); err != nil {
			return fmt.Errorf("postgres: migrate lock: %w", err)
		}

		if _, err := tx.Exec(ctx, s.sql(`
			CREATE SCHEMA IF NOT EXISTS {{schema}};
			CREATE TABLE IF NOT EXISTS {{schema}}.schema_migrations (
			    version    integer PRIMARY KEY,
			    applied_at timestamptz NOT NULL DEFAULT now()
			)`)); err != nil {
			return fmt.Errorf("postgres: migrate: %w", err)
		}

		var version int
		if err := tx.QueryRow(ctx,
			s.sql(`SELECT coalesce(max(version), 0) FROM {{schema}}.schema_migrations`)).
			Scan(&version); err != nil {
			return fmt.Errorf("postgres: migrate: %w", err)
		}
		if version > len(migrations) {
			return fmt.Errorf("postgres: schema %s is at version %d; this package knows %d",
				s.schema, version, len(migrations))
		}

		for v := version; v < len(migrations); v++ {
			if _, err := tx.Exec(ctx, s.sql(migrations[v])); err != nil {
				return fmt.Errorf("postgres: migration %d: %w", v+1, err)
			}
			record := s.sql(`INSERT INTO {{schema}}.schema_migrations (version) VALUES ($1)`)
			if _, err := tx.Exec(ctx, record, v+1); err != nil {
				return fmt.Errorf("postgres: migration %d: %w", v+1, err)
			}
		}
		return nil
	})
}

// migrateLockKey is the advisory lock that serializes Migrate per schema.
func migrateLockKey(schema string) int64 {
	h := fnv.New64a()
	h.Write([]byte("deepnoodle-ai/workflow/postgres.Migrate:" + schema))
	return int64(h.Sum64())
}

func (s *Store) sql(q string) string { return strings.ReplaceAll(q, "{{schema}}", s.schema) }

// NotifyChannel is the channel the store notifies when it writes jobs.
func (s *Store) NotifyChannel() string { return s.schema + "_jobs" }

// RunsChannel is the channel the store notifies, on commit, when a run's
// status changes. The payload is JSON: {"run_id": "...", "status": "failed"}.
// Notifications are best effort (a listener that is not connected misses
// them); read the run with GetRun, or use WithOnFinished for a guarantee.
func (s *Store) RunsChannel() string { return s.schema + "_runs" }

// Start starts a run in its own transaction.
func (s *Store) Start(ctx context.Context, run NewRun) (*Result, error) {
	var res *Result
	err := s.backend.Write(ctx, func(tx hostcore.Tx) error {
		var err error
		res, err = s.core.Start(ctx, tx, run)
		return err
	})
	return res, err
}

// StartTx starts a run in the caller's transaction. The commands in the
// result are already in the outbox; they run after the caller commits.
// Starting a run whose ID exists with the same definition returns its state,
// no commands and Applied false.
func (s *Store) StartTx(ctx context.Context, tx pgx.Tx, run NewRun) (*Result, error) {
	if err := s.allowPgxTx(); err != nil {
		return nil, err
	}
	return s.core.Start(ctx, s.txOn(tx), run)
}

// Deliver applies an event in its own transaction. The event's StepID may
// be a step's name, such as a wait step's (see [workflow.Resolve]); every
// Deliver form resolves it and stores the full StepID.
func (s *Store) Deliver(ctx context.Context, runID string, ev workflow.Event) (*Result, error) {
	var res *Result
	err := s.backend.Write(ctx, func(tx hostcore.Tx) error {
		var err error
		res, err = s.core.Deliver(ctx, tx, runID, ev)
		return err
	})
	return res, err
}

// DeliverTx applies an event in the caller's transaction: it locks the run
// row, calls Apply, and stores the state, step records and jobs. The
// commands in the result are already in the outbox; they run after the
// caller commits.
//
// Delivering an event twice is safe. A duplicate changes nothing, returns no
// commands and has Applied false.
func (s *Store) DeliverTx(
	ctx context.Context, tx pgx.Tx, runID string, ev workflow.Event,
) (*Result, error) {
	if err := s.allowPgxTx(); err != nil {
		return nil, err
	}
	return s.core.Deliver(ctx, s.txOn(tx), runID, ev)
}

// Cancel cancels a run in its own transaction. Applied is false if the run
// had already finished, or was already cancelled: a cancel decides the
// outcome once, and the finally steps it started run to the end. To stop
// them too, deliver a cancel event with Force.
func (s *Store) Cancel(ctx context.Context, runID, reason string) (*Result, error) {
	return s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

// CancelTx is [Store.Cancel] in the caller's transaction.
func (s *Store) CancelTx(ctx context.Context, tx pgx.Tx, runID, reason string) (*Result, error) {
	return s.DeliverTx(ctx, tx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

// Pause pauses a running run in its own transaction. Steps in flight
// finish and their results are recorded, but no worker claims a step of the
// run until Resume: new steps and retries wait in the outbox. Sleeps and
// retry backoffs still run. A step timeout that fires during the pause
// uses up no attempt; Resume gives the attempt a new, full timeout. Cancel
// still works, and a run whose outcome is decided while paused leaves
// paused and runs its on_failure and finally steps. Applied is false if
// the run was not running.
func (s *Store) Pause(ctx context.Context, runID, reason string) (*Result, error) {
	return s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventPause, Reason: reason})
}

// PauseTx is [Store.Pause] in the caller's transaction.
func (s *Store) PauseTx(ctx context.Context, tx pgx.Tx, runID, reason string) (*Result, error) {
	return s.DeliverTx(ctx, tx, runID, workflow.Event{Kind: workflow.EventPause, Reason: reason})
}

// Resume resumes a paused run in its own transaction and wakes the workers
// to claim its held steps, which are already in the outbox. Result.Jobs
// lists the timeout timers it re-arms (one for each attempt whose timeout
// fired during the pause), then every running step of the run, held or
// already claimed, so that a host that pushes one request per job sends
// them again now; a running one is found leased. Result.Commands holds
// their DispatchStep commands: do not carry them out. Applied is
// false if the run was not paused.
func (s *Store) Resume(ctx context.Context, runID string) (*Result, error) {
	return s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventResume})
}

// ResumeTx is [Store.Resume] in the caller's transaction.
func (s *Store) ResumeTx(ctx context.Context, tx pgx.Tx, runID string) (*Result, error) {
	return s.DeliverTx(ctx, tx, runID, workflow.Event{Kind: workflow.EventResume})
}

// GetRun reads a run.
func (s *Store) GetRun(ctx context.Context, runID string) (*Run, error) {
	return s.backend.GetRun(ctx, runID)
}

// Steps lists a run's step attempts in dispatch order.
func (s *Store) Steps(ctx context.Context, runID string) ([]StepRecord, error) {
	return s.backend.Steps(ctx, runID)
}

// Events lists the events applied to a run, in order.
func (s *Store) Events(ctx context.Context, runID string) ([]workflow.Event, error) {
	return s.backend.Events(ctx, runID)
}
