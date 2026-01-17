package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestPostgresQueue_EnqueueAndDequeue(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue item
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	// Dequeue item
	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)
	assert.Equal(t, lease.Item.ExecutionID, "exec-1")
	assert.NotEmpty(t, lease.Token)
	assert.False(t, lease.ExpiresAt.IsZero())
}

func TestPostgresQueue_Ack(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)

	// Ack the item
	err = queue.Ack(ctx, lease.Token)
	assert.NoError(t, err)

	// Item should be gone - dequeue should block
	dequeueCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = queue.Dequeue(dequeueCtx)
	assert.Error(t, err)
	assert.Equal(t, err, context.DeadlineExceeded)
}

func TestPostgresQueue_Nack(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)

	// Nack with no delay
	err = queue.Nack(ctx, lease.Token, 0)
	assert.NoError(t, err)

	// Should be able to dequeue again
	lease2, err := queue.Dequeue(ctx)
	assert.NoError(t, err)
	assert.Equal(t, lease2.Item.ExecutionID, "exec-1")
}

func TestPostgresQueue_NackWithDelay(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)

	// Nack with delay
	err = queue.Nack(ctx, lease.Token, 1*time.Second)
	assert.NoError(t, err)

	// Should not be able to dequeue immediately
	dequeueCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = queue.Dequeue(dequeueCtx)
	assert.Error(t, err)
	assert.Equal(t, err, context.DeadlineExceeded)
}

func TestPostgresQueue_Extend(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     1 * time.Second, // Short TTL for testing
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)

	initialExpiry := lease.ExpiresAt

	// Extend the lease
	err = queue.Extend(ctx, lease.Token, 10*time.Minute)
	assert.NoError(t, err)

	// The lease should be extended (we can verify by checking the DB directly)
	// For now, just verify no error occurred
	_ = initialExpiry // Could verify expiry changed
}

func TestPostgresQueue_DuplicateEnqueue(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue same item twice
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err) // Should not error due to ON CONFLICT DO NOTHING

	// Should only dequeue once
	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)
	assert.Equal(t, lease.Item.ExecutionID, "exec-1")

	err = queue.Ack(ctx, lease.Token)
	assert.NoError(t, err)

	// No more items
	dequeueCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = queue.Dequeue(dequeueCtx)
	assert.Error(t, err)
}

func TestPostgresQueue_MultipleItems(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue multiple items
	for i := 1; i <= 3; i++ {
		err := queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-" + string(rune('0'+i))})
		assert.NoError(t, err)
	}

	// Dequeue all
	seen := make(map[string]bool)
	for i := 0; i < 3; i++ {
		lease, err := queue.Dequeue(ctx)
		assert.NoError(t, err)
		seen[lease.Item.ExecutionID] = true
		err = queue.Ack(ctx, lease.Token)
		assert.NoError(t, err)
	}

	assert.Len(t, seen, 3)
	assert.True(t, seen["exec-1"])
	assert.True(t, seen["exec-2"])
	assert.True(t, seen["exec-3"])
}

func TestPostgresQueue_LeaseExpiry(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     100 * time.Millisecond, // Very short for testing
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	assert.NoError(t, err)
	assert.Equal(t, lease.Item.ExecutionID, "exec-1")

	// Wait for lease to expire
	time.Sleep(200 * time.Millisecond)

	// Create a second worker queue instance
	queue2 := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-2",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	// Second worker should be able to claim the item after reap
	lease2, err := queue2.Dequeue(ctx)
	assert.NoError(t, err)
	assert.Equal(t, lease2.Item.ExecutionID, "exec-1")
}

func TestPostgresQueue_Close(t *testing.T) {
	db, cleanup := setupPostgres(t)
	defer cleanup()

	ctx := context.Background()
	queue := NewPostgresQueue(PostgresQueueOptions{
		DB:           db,
		WorkerID:     "worker-1",
		PollInterval: 10 * time.Millisecond,
		LeaseTTL:     5 * time.Minute,
	})

	err := queue.CreateSchema(ctx)
	assert.NoError(t, err)

	// Close the queue
	err = queue.Close()
	assert.NoError(t, err)

	// Operations should fail after close
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}
