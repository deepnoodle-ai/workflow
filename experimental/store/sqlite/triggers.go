package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// InsertTriggers implements worker.TriggerStore.
func (s *Store) InsertTriggers(ctx context.Context, triggers []worker.Trigger) error {
	if len(triggers) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("sqlite: begin trigger insert tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, t := range triggers {
		id := t.ID
		if id == "" {
			id = generateID("trg_")
		}
		childSpec, err := json.Marshal(t.ChildSpec)
		if err != nil {
			return fmt.Errorf("sqlite: marshal trigger child spec: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO workflow_triggers (
				id, parent_run_id, child_spec, status, created_at
			) VALUES (?, ?, ?, ?, ?)
		`, id, t.ParentRunID, childSpec, string(worker.TriggerPending), formatTime(t.CreatedAt))
		if err != nil {
			return fmt.Errorf("sqlite: insert trigger: %w", err)
		}
	}
	return tx.Commit()
}

// ListPendingTriggers implements worker.TriggerStore.
func (s *Store) ListPendingTriggers(ctx context.Context, limit int) ([]worker.Trigger, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, parent_run_id, child_spec, status, attempts, error_message,
		       child_run_id, created_at, processed_at
		FROM workflow_triggers
		WHERE status = ?
		ORDER BY created_at ASC
		LIMIT ?
	`, string(worker.TriggerPending), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list pending triggers: %w", err)
	}
	defer rows.Close()

	var out []worker.Trigger
	for rows.Next() {
		var (
			t           worker.Trigger
			childSpec   string
			createdAt   string
			processedAt sql.NullString
		)
		if err := rows.Scan(&t.ID, &t.ParentRunID, &childSpec, &t.Status,
			&t.Attempts, &t.ErrorMessage, &t.ChildRunID, &createdAt, &processedAt); err != nil {
			return nil, fmt.Errorf("sqlite: scan trigger: %w", err)
		}
		t.CreatedAt = parseTime(createdAt)
		if processedAt.Valid {
			t.ProcessedAt = parseTime(processedAt.String)
		}
		if childSpec != "" {
			_ = json.Unmarshal([]byte(childSpec), &t.ChildSpec)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkTriggerProcessing implements worker.TriggerStore.
func (s *Store) MarkTriggerProcessing(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_triggers SET status = ? WHERE id = ?
	`, string(worker.TriggerProcessing), id)
	if err != nil {
		return fmt.Errorf("sqlite: mark trigger processing: %w", err)
	}
	return nil
}

// MarkTriggerCompleted implements worker.TriggerStore.
func (s *Store) MarkTriggerCompleted(ctx context.Context, id string, childRunID string) error {
	now := time.Now().UTC().Format(timeFormat)
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_triggers
		SET status = ?, child_run_id = ?, processed_at = ?
		WHERE id = ?
	`, string(worker.TriggerCompleted), childRunID, now, id)
	if err != nil {
		return fmt.Errorf("sqlite: mark trigger completed: %w", err)
	}
	return nil
}

// IncrementTriggerAttempts implements worker.TriggerStore.
func (s *Store) IncrementTriggerAttempts(ctx context.Context, id string, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_triggers
		SET attempts = attempts + 1, error_message = ?, status = ?
		WHERE id = ?
	`, errMsg, string(worker.TriggerPending), id)
	if err != nil {
		return fmt.Errorf("sqlite: increment trigger attempts: %w", err)
	}
	return nil
}

// MarkTriggerFailed implements worker.TriggerStore.
func (s *Store) MarkTriggerFailed(ctx context.Context, id string, errMsg string) error {
	now := time.Now().UTC().Format(timeFormat)
	_, err := s.db.ExecContext(ctx, `
		UPDATE workflow_triggers
		SET status = ?, error_message = ?, processed_at = ?
		WHERE id = ?
	`, string(worker.TriggerFailed), errMsg, now, id)
	if err != nil {
		return fmt.Errorf("sqlite: mark trigger failed: %w", err)
	}
	return nil
}
