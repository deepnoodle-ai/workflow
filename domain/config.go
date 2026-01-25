package domain

import "time"

// StoreConfig contains configuration for store implementations.
type StoreConfig struct {
	// HeartbeatInterval is how often workers should heartbeat
	HeartbeatInterval time.Duration

	// LeaseTimeout is how long before a task is considered abandoned
	LeaseTimeout time.Duration

	// MaxAttempts is the maximum number of retry attempts for a task
	MaxAttempts int
}

// DefaultStoreConfig returns sensible defaults for store configuration.
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		HeartbeatInterval: 30 * time.Second,
		LeaseTimeout:      2 * time.Minute,
		MaxAttempts:       3,
	}
}
