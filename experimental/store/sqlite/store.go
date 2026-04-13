package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

//go:embed schema.sql
var schemaSQL string

const timeFormat = "2006-01-02T15:04:05.000+00:00"

// Store is a SQLite-backed implementation of the worker QueueStore
// and the workflow engine's persistence interfaces.
//
// # Coexistence with consumer tables
//
// Unlike the postgres store, sqlite has no schema namespacing
// escape hatch — SQLite does not support schemas, and the library
// tables (workflow_runs, workflow_step_progress, workflow_events,
// etc.) are always unqualified. A consumer that uses SQLite for
// its own persistence and needs those same table names for its
// own data (or is worried about a future name collision) should
// hand the library a **dedicated *sql.DB** pointing at a separate
// .db file:
//
//	libDB, _ := sql.Open("sqlite", "library_workflow.db")
//	store := sqlite.New(libDB)
//
//	appDB, _ := sql.Open("sqlite", "app.db") // your own tables
//
// Alternatively, use `ATTACH DATABASE 'app.db' AS app` on the
// library DB and scope every consumer query with `app.`.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

// Option configures a Store.
type Option func(*Store)

// WithLogger attaches a structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Store) { s.logger = l }
}

// New constructs a Store bound to the given *sql.DB. The DB's
// lifecycle is owned by the caller. Panics if db is nil.
func New(db *sql.DB, opts ...Option) *Store {
	if db == nil {
		panic("sqlite: nil db")
	}
	s := &Store{
		db:     db,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Migrate applies the schema to the database. Idempotent: safe to
// call on every startup.
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, schemaSQL)
	return err
}

// NewCheckpointer returns a lease-fenced workflow.Checkpointer for
// the given claim. Panics if claim is nil.
func (s *Store) NewCheckpointer(claim *worker.Claim) workflow.Checkpointer {
	if claim == nil {
		panic("sqlite: nil claim")
	}
	return &leasedCheckpointer{store: s, claim: claim}
}

// NewStepProgressStore returns a workflow.StepProgressStore backed
// by this Store. The current implementation ignores the claim.
func (s *Store) NewStepProgressStore(_ *worker.Claim) workflow.StepProgressStore {
	return s
}

// NewActivityLogger returns a workflow.ActivityLogger backed by
// this Store.
func (s *Store) NewActivityLogger(_ *worker.Claim) workflow.ActivityLogger {
	return s
}

// DB returns the underlying *sql.DB for queries the high-level API
// does not cover.
func (s *Store) DB() *sql.DB { return s.db }

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeFormat)
}

func nullableTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(timeFormat)
	return &s
}

func parseTime(s string) time.Time {
	for _, layout := range []string{timeFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func generateID(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
