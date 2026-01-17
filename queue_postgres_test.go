package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

	// Enqueue item
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	// Dequeue item
	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease.Item.ExecutionID)
	require.NotEmpty(t, lease.Token)
	require.False(t, lease.ExpiresAt.IsZero())
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
	require.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)

	// Ack the item
	err = queue.Ack(ctx, lease.Token)
	require.NoError(t, err)

	// Item should be gone - dequeue should block
	dequeueCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = queue.Dequeue(dequeueCtx)
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
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
	require.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)

	// Nack with no delay
	err = queue.Nack(ctx, lease.Token, 0)
	require.NoError(t, err)

	// Should be able to dequeue again
	lease2, err := queue.Dequeue(ctx)
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease2.Item.ExecutionID)
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
	require.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)

	// Nack with delay
	err = queue.Nack(ctx, lease.Token, 1*time.Second)
	require.NoError(t, err)

	// Should not be able to dequeue immediately
	dequeueCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_, err = queue.Dequeue(dequeueCtx)
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
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
	require.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)

	initialExpiry := lease.ExpiresAt

	// Extend the lease
	err = queue.Extend(ctx, lease.Token, 10*time.Minute)
	require.NoError(t, err)

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
	require.NoError(t, err)

	// Enqueue same item twice
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err) // Should not error due to ON CONFLICT DO NOTHING

	// Should only dequeue once
	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease.Item.ExecutionID)

	err = queue.Ack(ctx, lease.Token)
	require.NoError(t, err)

	// No more items
	dequeueCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_, err = queue.Dequeue(dequeueCtx)
	require.Error(t, err)
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
	require.NoError(t, err)

	// Enqueue multiple items
	for i := 1; i <= 3; i++ {
		err := queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-" + string(rune('0'+i))})
		require.NoError(t, err)
	}

	// Dequeue all
	seen := make(map[string]bool)
	for i := 0; i < 3; i++ {
		lease, err := queue.Dequeue(ctx)
		require.NoError(t, err)
		seen[lease.Item.ExecutionID] = true
		err = queue.Ack(ctx, lease.Token)
		require.NoError(t, err)
	}

	require.Len(t, seen, 3)
	require.True(t, seen["exec-1"])
	require.True(t, seen["exec-2"])
	require.True(t, seen["exec-3"])
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
	require.NoError(t, err)

	// Enqueue and dequeue
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease.Item.ExecutionID)

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
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease2.Item.ExecutionID)
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
	require.NoError(t, err)

	// Close the queue
	err = queue.Close()
	require.NoError(t, err)

	// Operations should fail after close
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "closed")
}
