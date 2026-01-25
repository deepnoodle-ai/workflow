package workflow

import "github.com/deepnoodle-ai/workflow/domain"

// Store is the unified interface for execution state, task distribution, and events.
type Store = domain.Store

// SchemaMigrator is implemented by stores that need schema initialization.
type SchemaMigrator = domain.SchemaMigrator

// ExecutionFilter specifies criteria for listing executions.
type ExecutionFilter = domain.ExecutionFilter

// StoreConfig contains common configuration for store implementations.
type StoreConfig = domain.StoreConfig

// DefaultStoreConfig returns sensible defaults.
func DefaultStoreConfig() StoreConfig {
	return domain.DefaultStoreConfig()
}
