package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow/worker"
)

// EnqueueWebhook implements worker.WebhookStore.
func (s *Store) EnqueueWebhook(ctx context.Context, delivery *worker.WebhookDelivery) error {
	id := delivery.ID
	if id == "" {
		id = generateID("whk_")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_webhooks (
			id, run_id, url, event_type, payload, status, created_at
		) VALUES (?,?,?,?,?,?,?)
	`, id, delivery.RunID, delivery.URL, delivery.EventType,
		delivery.Payload, "pending", formatTime(delivery.CreatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: enqueue webhook: %w", err)
	}
	return nil
}

// ListPendingWebhooks implements worker.WebhookStore.
func (s *Store) ListPendingWebhooks(ctx context.Context, limit int) ([]*worker.WebhookDelivery, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, url, event_type, payload, status, attempts,
		       last_error, created_at, delivered_at
		FROM workflow_webhooks
		WHERE status = 'pending'
		ORDER BY created_at ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list pending webhooks: %w", err)
	}
	defer rows.Close()

	var out []*worker.WebhookDelivery
	for rows.Next() {
		var (
			d           worker.WebhookDelivery
			lastError   sql.NullString
			createdAt   string
			deliveredAt sql.NullString
		)
		if err := rows.Scan(&d.ID, &d.RunID, &d.URL, &d.EventType, &d.Payload,
			&d.Status, &d.Attempts, &lastError, &createdAt, &deliveredAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan webhook: %w", err)
		}
		d.CreatedAt = parseTime(createdAt)
		if lastError.Valid {
			d.LastError = lastError.String
		}
		if deliveredAt.Valid {
			d.DeliveredAt = parseTime(deliveredAt.String)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// MarkWebhookDelivered implements worker.WebhookStore.
func (s *Store) MarkWebhookDelivered(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(timeFormat)
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_webhooks SET status = 'delivered', delivered_at = ? WHERE id = ?
	`, now, id)
	if err != nil {
		return fmt.Errorf("sqlite: mark webhook delivered: %w", err)
	}
	return nil
}

// IncrementWebhookAttempts implements worker.WebhookStore.
func (s *Store) IncrementWebhookAttempts(ctx context.Context, id string, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_webhooks
		SET attempts = attempts + 1, last_error = ?
		WHERE id = ?
	`, lastError, id)
	if err != nil {
		return fmt.Errorf("sqlite: increment webhook attempts: %w", err)
	}
	return nil
}

// MarkWebhookFailed implements worker.WebhookStore.
func (s *Store) MarkWebhookFailed(ctx context.Context, id string, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_webhooks
		SET status = 'failed', last_error = ?
		WHERE id = ?
	`, errMsg, id)
	if err != nil {
		return fmt.Errorf("sqlite: mark webhook failed: %w", err)
	}
	return nil
}
