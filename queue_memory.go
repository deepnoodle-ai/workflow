package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.jetify.com/typeid"
)

// MemoryQueue is an in-memory implementation of WorkQueue for testing and single-process deployments.
type MemoryQueue struct {
	mu        sync.Mutex
	items     map[string]*memoryQueueItem
	pending   chan string // channel of execution IDs ready for processing
	workerID  string
	leaseTTL  time.Duration
	closed    bool
	closeChan chan struct{}
}

type memoryQueueItem struct {
	executionID string
	lockedBy    string
	lockedUntil time.Time
	visibleAt   time.Time
	token       string
}

// MemoryQueueOptions configures a new MemoryQueue.
type MemoryQueueOptions struct {
	WorkerID   string
	BufferSize int
	LeaseTTL   time.Duration
}

// NewMemoryQueue creates a new in-memory queue.
func NewMemoryQueue(opts MemoryQueueOptions) *MemoryQueue {
	if opts.BufferSize == 0 {
		opts.BufferSize = 100
	}
	if opts.LeaseTTL == 0 {
		opts.LeaseTTL = 5 * time.Minute
	}
	q := &MemoryQueue{
		items:     make(map[string]*memoryQueueItem),
		pending:   make(chan string, opts.BufferSize),
		workerID:  opts.WorkerID,
		leaseTTL:  opts.LeaseTTL,
		closeChan: make(chan struct{}),
	}
	go q.reaperLoop()
	return q
}

// Enqueue adds an item to the queue.
func (q *MemoryQueue) Enqueue(ctx context.Context, item WorkItem) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return fmt.Errorf("queue is closed")
	}

	// Check if item already exists
	if _, exists := q.items[item.ExecutionID]; exists {
		// Item already in queue, no need to add again
		return nil
	}

	q.items[item.ExecutionID] = &memoryQueueItem{
		executionID: item.ExecutionID,
		visibleAt:   time.Now(),
	}

	// Non-blocking send to the channel
	select {
	case q.pending <- item.ExecutionID:
	default:
		// Channel is full, item is still tracked in map
	}

	return nil
}

// Dequeue claims the next available item. Blocks until available or ctx cancelled.
func (q *MemoryQueue) Dequeue(ctx context.Context) (Lease, error) {
	for {
		select {
		case <-ctx.Done():
			return Lease{}, ctx.Err()
		case <-q.closeChan:
			return Lease{}, fmt.Errorf("queue is closed")
		case execID := <-q.pending:
			q.mu.Lock()
			item, exists := q.items[execID]
			if !exists {
				q.mu.Unlock()
				continue // Item was removed (acked), try again
			}

			// Check if item is visible and not locked
			now := time.Now()
			if item.visibleAt.After(now) || (item.lockedBy != "" && item.lockedUntil.After(now)) {
				// Item not visible or still locked, put it back
				q.mu.Unlock()
				select {
				case q.pending <- execID:
				default:
				}
				// Small sleep to prevent tight loop
				time.Sleep(10 * time.Millisecond)
				continue
			}

			// Generate lease token
			token := q.generateToken()
			item.lockedBy = q.workerID
			item.lockedUntil = now.Add(q.leaseTTL)
			item.token = token

			lease := Lease{
				Item:      WorkItem{ExecutionID: execID},
				Token:     token,
				ExpiresAt: item.lockedUntil,
			}
			q.mu.Unlock()
			return lease, nil
		}
	}
}

// Ack acknowledges successful processing. Removes the item from the queue.
func (q *MemoryQueue) Ack(ctx context.Context, token string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for id, item := range q.items {
		if item.token == token && item.lockedBy == q.workerID {
			delete(q.items, id)
			return nil
		}
	}
	return fmt.Errorf("lease token %q not found or not owned by this worker", token)
}

// Nack returns the item to the queue for retry after the specified delay.
func (q *MemoryQueue) Nack(ctx context.Context, token string, delay time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, item := range q.items {
		if item.token == token && item.lockedBy == q.workerID {
			item.lockedBy = ""
			item.lockedUntil = time.Time{}
			item.token = ""
			item.visibleAt = time.Now().Add(delay)

			// Re-enqueue after delay
			go func(execID string, delay time.Duration) {
				time.Sleep(delay)
				q.mu.Lock()
				defer q.mu.Unlock()
				if q.closed {
					return
				}
				if item, exists := q.items[execID]; exists && item.lockedBy == "" {
					select {
					case q.pending <- execID:
					default:
					}
				}
			}(item.executionID, delay)

			return nil
		}
	}
	return fmt.Errorf("lease token %q not found or not owned by this worker", token)
}

// Extend extends the lease TTL for long-running work.
func (q *MemoryQueue) Extend(ctx context.Context, token string, ttl time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, item := range q.items {
		if item.token == token && item.lockedBy == q.workerID {
			item.lockedUntil = time.Now().Add(ttl)
			return nil
		}
	}
	return fmt.Errorf("lease token %q not found or not owned by this worker", token)
}

// Close releases resources.
func (q *MemoryQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return nil
	}

	q.closed = true
	close(q.closeChan)
	return nil
}

// reaperLoop periodically checks for expired leases and re-enqueues items.
func (q *MemoryQueue) reaperLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-q.closeChan:
			return
		case <-ticker.C:
			q.reapExpiredLeases()
		}
	}
}

// reapExpiredLeases finds items with expired leases and makes them available again.
func (q *MemoryQueue) reapExpiredLeases() {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	for execID, item := range q.items {
		if item.lockedBy != "" && item.lockedUntil.Before(now) {
			// Lease expired, clear lock and re-enqueue
			item.lockedBy = ""
			item.lockedUntil = time.Time{}
			item.token = ""

			select {
			case q.pending <- execID:
			default:
			}
		}
	}
}

// generateToken creates a unique token for a lease.
func (q *MemoryQueue) generateToken() string {
	id, err := typeid.WithPrefix("lease")
	if err != nil {
		panic(err)
	}
	return id.String()
}
