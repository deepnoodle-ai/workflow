package workflow

import (
	"context"
	"time"
)

// WorkItem represents an item in the work queue.
type WorkItem struct {
	ExecutionID string
}

// Lease represents a claimed work item with an expiration time.
type Lease struct {
	Item      WorkItem
	Token     string    // opaque lease identifier
	ExpiresAt time.Time
}

// WorkQueue provides at-least-once delivery with explicit lease management.
type WorkQueue interface {
	// Enqueue adds an item to the queue.
	Enqueue(ctx context.Context, item WorkItem) error

	// Dequeue claims the next available item. Blocks until available or ctx cancelled.
	// The returned lease must be Ack'd, Nack'd, or will expire.
	Dequeue(ctx context.Context) (Lease, error)

	// Ack acknowledges successful processing. Removes the item from the queue.
	Ack(ctx context.Context, token string) error

	// Nack returns the item to the queue for retry after the specified delay.
	Nack(ctx context.Context, token string, delay time.Duration) error

	// Extend extends the lease TTL for long-running work.
	Extend(ctx context.Context, token string, ttl time.Duration) error

	// Close releases resources.
	Close() error
}
