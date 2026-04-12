package postgres

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/template"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaTemplate string

// DefaultSchema is the Postgres schema used when WithSchema is not
// supplied. Matches the behavior of the default search_path on a
// fresh Postgres install.
const DefaultSchema = "public"

// Store is a Postgres-backed implementation of the worker QueueStore
// and the workflow engine's persistence interfaces. Construct with
// New and call Migrate once on startup to ensure the schema is in
// place.
type Store struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	schema string
}

// Option configures a Store.
type Option func(*Store)

// WithLogger attaches a structured logger. Defaults to a discard logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Store) { s.logger = l }
}

// WithSchema selects the Postgres schema (namespace) that will hold
// the store's tables. Defaults to "public". The schema name is
// validated as a simple SQL identifier (letters, digits, underscore,
// starting with a letter or underscore) to rule out injection, and
// then used verbatim as a quoted identifier in every query.
//
// Migrate will run `CREATE SCHEMA IF NOT EXISTS` on the selected
// schema before creating tables, so the schema does not need to
// exist in advance.
func WithSchema(schema string) Option {
	return func(s *Store) { s.schema = schema }
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
		schema: DefaultSchema,
	}
	for _, opt := range opts {
		opt(s)
	}
	if err := validateIdentifier(s.schema); err != nil {
		panic(fmt.Sprintf("postgres: invalid schema name: %v", err))
	}
	return s
}

// Pool returns the underlying pgxpool.Pool for queries the high-level
// API does not cover. Consumers are responsible for not breaking the
// store's invariants (lease fencing, status transitions, etc.).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Schema returns the configured Postgres schema name.
func (s *Store) Schema() string { return s.schema }

// t returns a schema-qualified, sanitized identifier for the given
// table name (e.g., `"public"."workflow_runs"`). Use in every raw
// SQL string via fmt.Sprintf.
func (s *Store) t(table string) string {
	return pgx.Identifier{s.schema, table}.Sanitize()
}

// Migrate applies the schema to the database. Idempotent: safe to
// call on every startup.
func (s *Store) Migrate(ctx context.Context) error {
	tmpl, err := template.New("schema").Parse(schemaTemplate)
	if err != nil {
		return fmt.Errorf("postgres: parse schema template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"Schema": pgx.Identifier{s.schema}.Sanitize(),
	}); err != nil {
		return fmt.Errorf("postgres: execute schema template: %w", err)
	}
	if _, err := s.pool.Exec(ctx, buf.String()); err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	return nil
}

// validateIdentifier enforces a conservative rule: the identifier
// must be ASCII letters, digits, or underscore, and must start with
// a letter or underscore. This is stricter than Postgres allows
// (unquoted identifiers lowercase) but safe against injection.
func validateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("empty identifier")
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return fmt.Errorf("invalid character %q at position %d", r, i)
		}
	}
	// Reject reserved characters the double-quote sanitizer would
	// otherwise blindly escape.
	if strings.ContainsAny(name, `"`) {
		return fmt.Errorf("identifier must not contain quote characters")
	}
	return nil
}
