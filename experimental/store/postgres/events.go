package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// AppendEvent implements worker.EventStore.
func (s *Store) AppendEvent(ctx context.Context, event *worker.Event) error {
	var payload []byte
	if event.Payload != nil {
		b, err := json.Marshal(event.Payload)
		if err != nil {
			return fmt.Errorf("postgres: marshal event payload: %w", err)
		}
		payload = b
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO workflow_events (
			run_id, event_type, attempt, worker_id, step_name, payload, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING seq
	`,
		event.RunID,
		event.EventType,
		event.Attempt,
		event.WorkerID,
		event.StepName,
		payload,
		event.CreatedAt,
	).Scan(&event.Seq)
	if err != nil {
		return fmt.Errorf("postgres: append event: %w", err)
	}
	return nil
}

// ListEvents implements worker.EventStore.
func (s *Store) ListEvents(ctx context.Context, runID string, afterSeq int64) ([]*worker.Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, run_id, event_type, attempt, worker_id, step_name, payload, created_at
		FROM workflow_events
		WHERE run_id = $1 AND seq > $2
		ORDER BY seq ASC
	`, runID, afterSeq)
	if err != nil {
		return nil, fmt.Errorf("postgres: list events: %w", err)
	}
	defer rows.Close()

	var out []*worker.Event
	for rows.Next() {
		var (
			e       worker.Event
			payload []byte
		)
		if err := rows.Scan(&e.Seq, &e.RunID, &e.EventType, &e.Attempt,
			&e.WorkerID, &e.StepName, &payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan event: %w", err)
		}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &e.Payload)
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return out, nil
}

// CleanupEvents implements worker.EventStore.
func (s *Store) CleanupEvents(ctx context.Context, olderThan time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM workflow_events WHERE created_at < $1
	`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("postgres: cleanup events: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
