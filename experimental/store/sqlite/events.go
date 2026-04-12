package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// AppendEvent implements worker.EventStore.
func (s *Store) AppendEvent(ctx context.Context, event *worker.Event) error {
	var payload []byte
	if event.Payload != nil {
		b, err := json.Marshal(event.Payload)
		if err != nil {
			return fmt.Errorf("sqlite: marshal event payload: %w", err)
		}
		payload = b
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO workflow_events (
			run_id, event_type, attempt, worker_id, step_name, payload, created_at
		) VALUES (?,?,?,?,?,?,?)
		RETURNING seq
	`,
		event.RunID,
		event.EventType,
		event.Attempt,
		event.WorkerID,
		event.StepName,
		payload,
		formatTime(event.CreatedAt),
	).Scan(&event.Seq)
	if err != nil {
		return fmt.Errorf("sqlite: append event: %w", err)
	}
	return nil
}

// ListEvents implements worker.EventStore.
func (s *Store) ListEvents(ctx context.Context, runID string, afterSeq int64) ([]*worker.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT seq, run_id, event_type, attempt, worker_id, step_name, payload, created_at
		FROM workflow_events
		WHERE run_id = ? AND seq > ?
		ORDER BY seq ASC
	`, runID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list events: %w", err)
	}
	defer rows.Close()

	var out []*worker.Event
	for rows.Next() {
		var (
			e         worker.Event
			payload   sql.NullString
			createdAt string
		)
		if err := rows.Scan(&e.Seq, &e.RunID, &e.EventType, &e.Attempt,
			&e.WorkerID, &e.StepName, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan event: %w", err)
		}
		e.CreatedAt = parseTime(createdAt)
		if payload.Valid && payload.String != "" {
			if err := json.Unmarshal([]byte(payload.String), &e.Payload); err != nil {
				return nil, fmt.Errorf("sqlite: unmarshal event payload (seq %d): %w", e.Seq, err)
			}
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// CleanupEvents implements worker.EventStore.
func (s *Store) CleanupEvents(ctx context.Context, olderThan time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM workflow_events WHERE created_at < ?
	`, olderThan.UTC().Format(timeFormat))
	if err != nil {
		return 0, fmt.Errorf("sqlite: cleanup events: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}
