package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryQueue_EnqueueDequeue(t *testing.T) {
	ctx := context.Background()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})
	defer queue.Close()

	// Enqueue an item
	err := queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)

	// Dequeue the item
	lease, err := queue.Dequeue(ctx)
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease.Item.ExecutionID)
	require.NotEmpty(t, lease.Token)
	require.True(t, lease.ExpiresAt.After(time.Now()))
}

func TestMemoryQueue_Ack(t *testing.T) {
	ctx := context.Background()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})
	defer queue.Close()

	// Enqueue and dequeue
	queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	lease, _ := queue.Dequeue(ctx)

	// Ack should succeed
	err := queue.Ack(ctx, lease.Token)
	require.NoError(t, err)

	// Ack same token again should fail
	err = queue.Ack(ctx, lease.Token)
	require.Error(t, err)
}

func TestMemoryQueue_Nack(t *testing.T) {
	ctx := context.Background()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})
	defer queue.Close()

	// Enqueue and dequeue
	queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	lease1, _ := queue.Dequeue(ctx)

	// Nack with short delay
	err := queue.Nack(ctx, lease1.Token, 50*time.Millisecond)
	require.NoError(t, err)

	// Wait for item to become visible again
	time.Sleep(100 * time.Millisecond)

	// Should be able to dequeue again
	ctx2, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	lease2, err := queue.Dequeue(ctx2)
	require.NoError(t, err)
	require.Equal(t, "exec-1", lease2.Item.ExecutionID)
}

func TestMemoryQueue_Extend(t *testing.T) {
	ctx := context.Background()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   100 * time.Millisecond,
	})
	defer queue.Close()

	// Enqueue and dequeue
	queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	lease, _ := queue.Dequeue(ctx)

	// Extend the lease
	err := queue.Extend(ctx, lease.Token, 5*time.Minute)
	require.NoError(t, err)

	// Extend with unknown token should fail
	err = queue.Extend(ctx, "unknown-token", 5*time.Minute)
	require.Error(t, err)
}

func TestMemoryQueue_Close(t *testing.T) {
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})

	// Close should succeed
	err := queue.Close()
	require.NoError(t, err)

	// Close again should be idempotent
	err = queue.Close()
	require.NoError(t, err)

	// Enqueue after close should fail
	err = queue.Enqueue(context.Background(), WorkItem{ExecutionID: "exec-1"})
	require.Error(t, err)
}

func TestMemoryQueue_DequeueBlocking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})
	defer queue.Close()

	// Dequeue on empty queue should block until context expires
	_, err := queue.Dequeue(ctx)
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}

func TestMemoryQueue_DuplicateEnqueue(t *testing.T) {
	ctx := context.Background()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})
	defer queue.Close()

	// Enqueue same item twice
	err := queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err)
	err = queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-1"})
	require.NoError(t, err) // Should not error, just be idempotent

	// Should only get one item
	lease1, _ := queue.Dequeue(ctx)
	queue.Ack(ctx, lease1.Token)

	// Second dequeue should block (no more items)
	ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = queue.Dequeue(ctx2)
	require.Error(t, err)
}

func TestMemoryQueue_MultipleItems(t *testing.T) {
	ctx := context.Background()
	queue := NewMemoryQueue(MemoryQueueOptions{
		WorkerID:   "worker-1",
		BufferSize: 10,
		LeaseTTL:   5 * time.Minute,
	})
	defer queue.Close()

	// Enqueue multiple items
	for i := 1; i <= 3; i++ {
		err := queue.Enqueue(ctx, WorkItem{ExecutionID: "exec-" + string(rune('0'+i))})
		require.NoError(t, err)
	}

	// Dequeue all items
	seen := make(map[string]bool)
	for i := 0; i < 3; i++ {
		lease, err := queue.Dequeue(ctx)
		require.NoError(t, err)
		seen[lease.Item.ExecutionID] = true
		queue.Ack(ctx, lease.Token)
	}

	require.Len(t, seen, 3)
	require.True(t, seen["exec-1"])
	require.True(t, seen["exec-2"])
	require.True(t, seen["exec-3"])
}
