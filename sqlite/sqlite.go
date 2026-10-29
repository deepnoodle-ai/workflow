// Package sqlite is a durable host for workflows on SQLite, with the same
// API and behaviour as the postgres module: develop on a SQLite file, run
// on Postgres in production, with one code path.
//
// The store takes a *sql.DB and imports no driver. It is tested with
// modernc.org/sqlite and github.com/mattn/go-sqlite3. Open the database
// with a busy timeout, and WAL for processes that share the file:
//
//	file:dev.db?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate
//
// Each event advances a run in one short transaction, as on Postgres: lock
// the run, call [workflow.Apply], store the new state, step records and
// event, and write the commands to an outbox (the jobs table). Workers
// claim jobs, run the step handlers, and deliver the results as new events.
// Several processes on one host can share the file. SQLite has one writer
// for the whole database, so a run lock blocks every write until commit.
//
// The types and errors shared with every backend are package durable's;
// this package re-exports each one under the same name.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/hostcore"
)

//go:embed schema.sql
var schemaSQL string

// migrations are the schema's versions, in order: migrations[i] takes the
// schema from version i to i+1. Append; never edit one that has shipped.
var migrations = []string{schemaSQL}

var schemaRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Store persists runs and their outbox in a SQLite database.
type Store struct {
	db     *sql.DB
	kinds  workflow.Kinds
	schema string // the table prefix, without its underscore
	limits workflow.Limits

	onFinishedSQL func(ctx context.Context, tx SQLTx, f Finished) error
	newRunID      func() string

	core    *hostcore.Store
	backend sqliteBackend
	hub     hub

	checkMu sync.Mutex
	checked bool
}

// Option configures a Store.
type Option func(*Store)

// WithSchema puts the tables under the prefix name_ instead of
// "workflow_", so they do not collide with the application's own tables.
// The name follows the postgres module's rule for a schema name.
func WithSchema(name string) Option { return func(s *Store) { s.schema = name } }

// WithLimits sets the state limits for every definition the store loads.
// The store loads each definition itself, from its document, so the limits
// a caller loaded NewRun.Definition with do not apply.
func WithLimits(l workflow.Limits) Option { return func(s *Store) { s.limits = l } }

// WithRunIDs sets how Start makes the ID of a run whose NewRun.ID is
// empty. The default is a UUIDv7 string, so IDs sort by start time.
func WithRunIDs(fn func() string) Option { return func(s *Store) { s.newRunID = fn } }

// WithOnFinishedSQL calls fn in the transaction that moves a run to
// completed, failed or cancelled, once per run, on every path that
// finishes a run: a worker's settle or abort, Start, Deliver, Cancel and
// the SQLTx methods. tx is the caller's own value when a SQLTx method
// finished the run, else the *sql.Tx the store opened. Use it to record
// the outcome, or to resume a parent run with DeliverSQLTx, atomically
// with it. An error from fn rolls the event back, so return one only when
// the transaction itself failed; a worker retries its settle.
//
// The postgres module has the same option, so one hook serves both.
func WithOnFinishedSQL(fn func(ctx context.Context, tx SQLTx, f Finished) error) Option {
	return func(s *Store) { s.onFinishedSQL = fn }
}

// New returns a Store on db. kinds validates every definition the store
// loads. New does no I/O; Migrate and Worker.Run check the database.
func New(db *sql.DB, kinds workflow.Kinds, opts ...Option) (*Store, error) {
	s := &Store{db: db, kinds: kinds, schema: "workflow"}
	for _, o := range opts {
		o(s)
	}
	if !schemaRE.MatchString(s.schema) {
		return nil, fmt.Errorf("sqlite: invalid schema name %q", s.schema)
	}
	s.core = &hostcore.Store{
		Kinds:    s.kinds,
		Limits:   s.limits,
		NewRunID: s.newRunID,
		Prefix:   "sqlite",
	}
	s.backend = sqliteBackend{s: s}
	return s, nil
}

func (s *Store) sql(q string) string { return strings.ReplaceAll(q, "{{p}}", s.schema+"_") }

// Migrate checks the database (the package doc gives the DSN), then brings the
// schema up to date. It is safe to call from every process at startup:
// migrations run in one transaction under the database's write lock, each
// at most once, and are recorded in schema_migrations. Migrate fails if
// the database is at a newer version than this package knows.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	if _, err := s.exec(ctx, s.db, `CREATE TABLE IF NOT EXISTS {{p}}schema_migrations (
	    version    INTEGER PRIMARY KEY,
	    applied_at TEXT NOT NULL
	) STRICT`); err != nil {
		return fmt.Errorf("sqlite: migrate: %w", err)
	}
	return s.backend.Write(ctx, func(htx hostcore.Tx) error {
		t := htx.(*sqliteTx)
		// A write that changes no row takes the write lock, so concurrent
		// Migrate calls run one at a time.
		if _, err := t.exec(ctx,
			`UPDATE {{p}}schema_migrations SET version = version WHERE version < 0`); err != nil {
			return fmt.Errorf("sqlite: migrate lock: %w", err)
		}
		var version int
		if err := t.readRow(ctx,
			`SELECT coalesce(max(version), 0) FROM {{p}}schema_migrations`).Scan(&version); err != nil {
			return fmt.Errorf("sqlite: migrate: %w", err)
		}
		if version > len(migrations) {
			return fmt.Errorf("sqlite: tables %s_ are at version %d; this package knows %d",
				s.schema, version, len(migrations))
		}
		for v := version; v < len(migrations); v++ {
			if _, err := t.exec(ctx, migrations[v]); err != nil {
				return fmt.Errorf("sqlite: migration %d: %w", v+1, err)
			}
			if _, err := t.exec(ctx,
				`INSERT INTO {{p}}schema_migrations (version, applied_at) VALUES (?1, ?2)`,
				v+1, fmtTime(now())); err != nil {
				return fmt.Errorf("sqlite: migration %d: %w", v+1, err)
			}
		}
		return nil
	})
}

// check refuses a database the store cannot run on: SQLite older than
// 3.37, no busy timeout, or an in-memory database on a pool that can open
// a second connection (which would be a second, empty database). It runs
// once per store, until it passes.
func (s *Store) check(ctx context.Context) error {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checked {
		return nil
	}
	var version string
	if err := s.db.QueryRowContext(ctx, `SELECT sqlite_version()`).Scan(&version); err != nil {
		return fmt.Errorf("sqlite: read the SQLite version: %w", err)
	}
	if !atLeast(version, 3, 37) {
		return fmt.Errorf("sqlite: SQLite %s is too old; the store needs 3.37 or newer "+
			"(RETURNING and STRICT tables)", version)
	}
	var busy int
	if err := s.db.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil {
		return fmt.Errorf("sqlite: read busy_timeout: %w", err)
	}
	if busy <= 0 {
		return errors.New("sqlite: the database has no busy timeout, so two writes that overlap " +
			"fail with SQLITE_BUSY; set one in the DSN, for example _busy_timeout=5000")
	}
	memory, err := s.inMemory(ctx)
	if err != nil {
		return err
	}
	if memory && s.db.Stats().MaxOpenConnections != 1 {
		return errors.New("sqlite: an in-memory database needs db.SetMaxOpenConns(1), " +
			"since each new connection opens a new, empty database; or use a file")
	}
	s.checked = true
	return nil
}

// inMemory reports whether the main database has no file.
func (s *Store) inMemory(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA database_list`)
	if err != nil {
		return false, fmt.Errorf("sqlite: read database_list: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			seq        int
			name, file string
		)
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return false, err
		}
		if name == "main" {
			return file == "", nil
		}
	}
	return false, rows.Err()
}

// atLeast reports whether version ("3.45.1") is major.minor or newer.
func atLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || ma == major && mi >= minor
}

// Start starts a run in its own transaction. Starting a run whose ID
// exists with the same definition returns its state, no commands and
// Applied false.
func (s *Store) Start(ctx context.Context, run NewRun) (*Result, error) {
	var res *Result
	err := s.backend.Write(ctx, func(tx hostcore.Tx) (err error) {
		res, err = s.core.Start(ctx, tx, run)
		return err
	})
	return res, err
}

// StartSQLTx starts a run in the caller's database/sql transaction on the
// store's database. The commands in the result are already in the outbox;
// they run after the caller commits.
//
// The transaction holds the database's write lock from its first write
// until Commit, and that blocks every write, the workers' included. Keep
// it short and make no network calls inside it. Open it with
// _txlock=immediate in the DSN: a transaction that reads before its first
// write can fail with SQLITE_BUSY when it writes.
func (s *Store) StartSQLTx(ctx context.Context, tx SQLTx, run NewRun) (*Result, error) {
	return s.core.Start(ctx, s.callerTx(tx), run)
}

// Deliver applies an event in its own transaction: it locks the run, calls
// Apply, and stores the state, step records and jobs. Delivering an event
// twice is safe. A duplicate changes nothing, returns no commands and has
// Applied false.
func (s *Store) Deliver(ctx context.Context, runID string, ev workflow.Event) (*Result, error) {
	var res *Result
	err := s.backend.Write(ctx, func(tx hostcore.Tx) (err error) {
		res, err = s.core.Deliver(ctx, tx, runID, ev)
		return err
	})
	return res, err
}

// DeliverSQLTx is [Store.Deliver] in the caller's database/sql
// transaction. See [Store.StartSQLTx] for how long to hold it.
func (s *Store) DeliverSQLTx(
	ctx context.Context, tx SQLTx, runID string, ev workflow.Event,
) (*Result, error) {
	return s.core.Deliver(ctx, s.callerTx(tx), runID, ev)
}

// Cancel cancels a run in its own transaction. Applied is false if the run
// had already finished, or was already cancelled: a cancel decides the
// outcome once, and the finally steps it started run to the end. To stop
// them too, deliver a cancel event with Force.
func (s *Store) Cancel(ctx context.Context, runID, reason string) (*Result, error) {
	return s.Deliver(ctx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
}

// CancelSQLTx is [Store.Cancel] in the caller's database/sql transaction.
func (s *Store) CancelSQLTx(ctx context.Context, tx SQLTx, runID, reason string) (*Result, error) {
	return s.DeliverSQLTx(ctx, tx, runID, workflow.Event{Kind: workflow.EventCancel, Reason: reason})
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

// QueueStats reports, for each queue that has at least one job in the
// outbox, how many jobs are due and how long the oldest has waited: the
// queue's lag. A queue whose jobs are all in the future or claimed has Due
// zero; a queue with no jobs is not listed. The result is sorted by Kind,
// then StepKind.
func (s *Store) QueueStats(ctx context.Context) ([]QueueStat, error) {
	return s.backend.QueueStats(ctx)
}
