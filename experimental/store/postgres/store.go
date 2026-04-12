package postgres

import (
	"context"
	_ "embed"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// Store is a Postgres-backed implementation of the worker QueueStore
// and the workflow engine's persistence interfaces. Construct with
// New and call Migrate once on startup to ensure the schema is in
// place.
type Store struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// Option configures a Store.
type Option func(*Store)

// WithLogger attaches a structured logger. Defaults to a discard logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Store) { s.logger = l }
}

// New constructs a Store bound to the given pgx pool. The pool's
// lifecycle is owned by the caller. Panics if pool is nil.
func New(pool *pgxpool.Pool, opts ...Option) *Store {
	if pool == nil {
		panic("postgres: nil pool")
	}
	s := &Store{
		pool:   pool,
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
	_, err := s.pool.Exec(ctx, schemaSQL)
	return err
}
