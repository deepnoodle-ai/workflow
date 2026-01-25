package domain

import "context"

// Store is the unified interface for all persistence operations.
// It composes ExecutionRepository, TaskRepository, and EventRepository.
type Store interface {
	ExecutionRepository
	TaskRepository
	EventRepository

	// CreateSchema initializes the storage schema (for implementations that need it).
	CreateSchema(ctx context.Context) error
}
