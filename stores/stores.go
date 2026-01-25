// Package stores provides factory functions for creating store implementations.
// This package contains the concrete implementations and infrastructure dependencies,
// keeping the main workflow package free of such dependencies.
package stores

import (
	"context"
	"database/sql"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/internal/memory"
	"github.com/deepnoodle-ai/workflow/internal/postgres"
)

// NewMemoryStore creates an in-memory store for testing and development.
// The store is not durable and loses all data when the process exits.
func NewMemoryStore() domain.Store {
	return memory.NewStore()
}

// PostgresStoreOption configures a PostgreSQL store.
type PostgresStoreOption func(*postgresStoreConfig)

type postgresStoreConfig struct {
	config domain.StoreConfig
}

// WithStoreConfig sets custom store configuration.
func WithStoreConfig(config domain.StoreConfig) PostgresStoreOption {
	return func(c *postgresStoreConfig) {
		c.config = config
	}
}

// NewPostgresStore creates a PostgreSQL-backed store for production use.
// The db connection must be opened and configured by the caller.
// Call CreateSchema() on the returned store to initialize database tables.
func NewPostgresStore(db *sql.DB, opts ...PostgresStoreOption) domain.Store {
	cfg := &postgresStoreConfig{
		config: domain.DefaultStoreConfig(),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	return postgres.NewStore(postgres.StoreOptions{
		DB:     db,
		Config: cfg.config,
	})
}

// CreateSchema initializes the database schema for stores that support it.
// Returns nil if the store doesn't require schema initialization (e.g., memory store).
func CreateSchema(ctx context.Context, store domain.Store) error {
	if migrator, ok := store.(domain.SchemaMigrator); ok {
		return migrator.CreateSchema(ctx)
	}
	return nil
}
