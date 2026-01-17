package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// PostgresQueue implements WorkQueue using PostgreSQL.
type PostgresQueue struct {
	db           *sql.DB
	workerID     string
	pollInterval time.Duration
	leaseTTL     time.Duration

	mu     sync.Mutex
	closed bool
}

// PostgresQueueOptions contains configuration for PostgresQueue.
type PostgresQueueOptions struct {
	DB           *sql.DB
	WorkerID     string
	PollInterval time.Duration // default 100ms
	LeaseTTL     time.Duration // default 5min
}

// NewPostgresQueue creates a new PostgresQueue.
func NewPostgresQueue(opts PostgresQueueOptions) *PostgresQueue {
	pollInterval := opts.PollInterval
	if pollInterval == 0 {
		pollInterval = 100 * time.Millisecond
	}
	leaseTTL := opts.LeaseTTL
	if leaseTTL == 0 {
		leaseTTL = 5 * time.Minute
	}

	return &PostgresQueue{
		db:           opts.DB,
		workerID:     opts.WorkerID,
		pollInterval: pollInterval,
		leaseTTL:     leaseTTL,
	}
}

// CreateSchema creates the workflow_queue table and indexes.
// This should be called during application setup.
func (q *PostgresQueue) CreateSchema(ctx context.Context) error {
	schema := `
		CREATE TABLE IF NOT EXISTS workflow_queue (
			id            SERIAL PRIMARY KEY,
			execution_id  TEXT NOT NULL UNIQUE,
			status        TEXT NOT NULL DEFAULT 'pending',
			visible_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			locked_by     TEXT,
			locked_until  TIMESTAMPTZ,
			attempt       INTEGER NOT NULL DEFAULT 0,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_queue_pending ON workflow_queue(visible_at)
			WHERE status = 'pending';
		CREATE INDEX IF NOT EXISTS idx_queue_stale ON workflow_queue(locked_until)
			WHERE status = 'processing';
	`
	_, err := q.db.ExecContext(ctx, schema)
	return err
}

// Enqueue adds an item to the queue.
func (q *PostgresQueue) Enqueue(ctx context.Context, item WorkItem) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return fmt.Errorf("queue is closed")
	}
	q.mu.Unlock()

	// Use INSERT ... ON CONFLICT DO NOTHING to handle duplicates
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO workflow_queue (execution_id, status, visible_at)
		VALUES ($1, 'pending', NOW())
		ON CONFLICT (execution_id) DO NOTHING
	`, item.ExecutionID)
	return err
}

// Dequeue claims the next available item. Blocks until available or ctx cancelled.
func (q *PostgresQueue) Dequeue(ctx context.Context) (Lease, error) {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return Lease{}, fmt.Errorf("queue is closed")
		}
		q.mu.Unlock()

		// First, reap any stale leases
		q.reapStaleLeases(ctx)

		// Try to claim an item
		var lease Lease
		var id int64
		leaseTTLInterval := fmt.Sprintf("%d seconds", int(q.leaseTTL.Seconds()))

		err := q.db.QueryRowContext(ctx, `
			UPDATE workflow_queue
			SET status = 'processing',
				locked_by = $1,
				locked_until = NOW() + $2::interval,
				attempt = attempt + 1
			WHERE id = (
				SELECT id FROM workflow_queue
				WHERE status = 'pending' AND visible_at <= NOW()
				ORDER BY created_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			RETURNING id, execution_id, locked_until
		`, q.workerID, leaseTTLInterval).Scan(&id, &lease.Item.ExecutionID, &lease.ExpiresAt)

		if err == nil {
			// Successfully claimed an item
			lease.Token = strconv.FormatInt(id, 10)
			return lease, nil
		}

		if err != sql.ErrNoRows {
			return Lease{}, fmt.Errorf("dequeue: %w", err)
		}

		// No work available, poll after interval
		select {
		case <-time.After(q.pollInterval):
			continue
		case <-ctx.Done():
			return Lease{}, ctx.Err()
		}
	}
}

// Ack acknowledges successful processing. Removes the item from the queue.
func (q *PostgresQueue) Ack(ctx context.Context, token string) error {
	id, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	_, err = q.db.ExecContext(ctx, `
		DELETE FROM workflow_queue WHERE id = $1 AND locked_by = $2
	`, id, q.workerID)
	return err
}

// Nack returns the item to the queue for retry after the specified delay.
func (q *PostgresQueue) Nack(ctx context.Context, token string, delay time.Duration) error {
	id, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	delayInterval := fmt.Sprintf("%d seconds", int(delay.Seconds()))
	_, err = q.db.ExecContext(ctx, `
		UPDATE workflow_queue
		SET status = 'pending',
			locked_by = NULL,
			locked_until = NULL,
			visible_at = NOW() + $2::interval
		WHERE id = $1 AND locked_by = $3
	`, id, delayInterval, q.workerID)
	return err
}

// Extend extends the lease TTL for long-running work.
func (q *PostgresQueue) Extend(ctx context.Context, token string, ttl time.Duration) error {
	id, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	ttlInterval := fmt.Sprintf("%d seconds", int(ttl.Seconds()))
	_, err = q.db.ExecContext(ctx, `
		UPDATE workflow_queue
		SET locked_until = NOW() + $2::interval
		WHERE id = $1 AND locked_by = $3 AND status = 'processing'
	`, id, ttlInterval, q.workerID)
	return err
}

// Close releases resources.
func (q *PostgresQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	return nil
}

// reapStaleLeases moves expired leases back to pending.
func (q *PostgresQueue) reapStaleLeases(ctx context.Context) {
	_, _ = q.db.ExecContext(ctx, `
		UPDATE workflow_queue
		SET status = 'pending',
			locked_by = NULL,
			locked_until = NULL
		WHERE status = 'processing' AND locked_until < NOW()
	`)
}

// Verify PostgresQueue implements WorkQueue.
var _ WorkQueue = (*PostgresQueue)(nil)
