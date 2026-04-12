package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"io"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

//go:embed schema.sql
var schemaSQL string

const timeFormat = "2006-01-02T15:04:05.000+00:00"

// Store is a SQLite-backed implementation of the worker QueueStore
// and the workflow engine's persistence interfaces.
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

// NewCheckpointer returns a lease-fenced Checkpointer for the given
// claim.
func (s *Store) NewCheckpointer(lease worker.Lease) *leasedCheckpointer {
	return &leasedCheckpointer{store: s, lease: lease}
}

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

func generateID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
