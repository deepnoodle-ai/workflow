package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// EnqueueWebhook implements worker.WebhookStore.
func (s *Store) EnqueueWebhook(ctx context.Context, delivery *worker.WebhookDelivery) error {
	id := delivery.ID
	if id == "" {
		id = generateID("whk_")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_webhooks (
			id, run_id, url, event_type, payload, status, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, id, delivery.RunID, delivery.URL, delivery.EventType,
		delivery.Payload, "pending", delivery.CreatedAt)
	if err != nil {
		return fmt.Errorf("postgres: enqueue webhook: %w", err)
	}
	return nil
}

// ListPendingWebhooks implements worker.WebhookStore.
func (s *Store) ListPendingWebhooks(ctx context.Context, limit int) ([]*worker.WebhookDelivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, url, event_type, payload, status, attempts,
		       last_error, created_at, delivered_at
		FROM workflow_webhooks
		WHERE status = 'pending'
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pending webhooks: %w", err)
	}
	defer rows.Close()

	var out []*worker.WebhookDelivery
	for rows.Next() {
		var (
			d           worker.WebhookDelivery
			lastError   *string
			deliveredAt *time.Time
		)
		if err := rows.Scan(&d.ID, &d.RunID, &d.URL, &d.EventType, &d.Payload,
			&d.Status, &d.Attempts, &lastError, &d.CreatedAt, &deliveredAt); err != nil {
			return nil, fmt.Errorf("postgres: scan webhook: %w", err)
		}
		if lastError != nil {
			d.LastError = *lastError
		}
		if deliveredAt != nil {
			d.DeliveredAt = *deliveredAt
		}
		out = append(out, &d)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return out, nil
}

// MarkWebhookDelivered implements worker.WebhookStore.
func (s *Store) MarkWebhookDelivered(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_webhooks SET status = 'delivered', delivered_at = NOW() WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("postgres: mark webhook delivered: %w", err)
	}
	return nil
}

// IncrementWebhookAttempts implements worker.WebhookStore.
func (s *Store) IncrementWebhookAttempts(ctx context.Context, id string, lastError string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_webhooks
		SET attempts = attempts + 1, last_error = $1
		WHERE id = $2
	`, lastError, id)
	if err != nil {
		return fmt.Errorf("postgres: increment webhook attempts: %w", err)
	}
	return nil
}

// MarkWebhookFailed implements worker.WebhookStore.
func (s *Store) MarkWebhookFailed(ctx context.Context, id string, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_webhooks
		SET status = 'failed', last_error = $1
		WHERE id = $2
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("postgres: mark webhook failed: %w", err)
	}
	return nil
}
