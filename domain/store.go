package domain

import "context"

// Store is the unified interface for all persistence operations.
// It composes ExecutionRepository, TaskRepository, and EventLog.
type Store interface {
	ExecutionRepository
	TaskRepository
	EventLog
	SchemaMigrator
}

// SchemaMigrator is implemented by stores that need schema initialization.
// This is separated from Store to allow the interface to remain clean in
// the domain layer, while implementations can provide migration support.
type SchemaMigrator interface {
	// CreateSchema initializes the storage schema.
	// For stores that don't need schema initialization (e.g., memory), this is a no-op.
	CreateSchema(ctx context.Context) error
}
