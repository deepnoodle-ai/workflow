package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

func generateID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// InsertTriggers implements worker.TriggerStore.
func (s *Store) InsertTriggers(ctx context.Context, triggers []worker.Trigger) error {
	if len(triggers) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: begin trigger insert tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, t := range triggers {
		id := t.ID
		if id == "" {
			id = generateID("trg_")
		}
		childSpec, err := json.Marshal(t.ChildSpec)
		if err != nil {
			return fmt.Errorf("postgres: marshal trigger child spec: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO workflow_triggers (
				id, parent_run_id, child_spec, status, created_at
			) VALUES ($1, $2, $3, $4, $5)
		`, id, t.ParentRunID, childSpec, string(worker.TriggerPending), t.CreatedAt)
		if err != nil {
			return fmt.Errorf("postgres: insert trigger: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListPendingTriggers implements worker.TriggerStore.
func (s *Store) ListPendingTriggers(ctx context.Context, limit int) ([]worker.Trigger, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, parent_run_id, child_spec, status, attempts, error_message,
		       child_run_id, created_at, processed_at
		FROM workflow_triggers
		WHERE status = $1
		ORDER BY created_at ASC
		LIMIT $2
	`, string(worker.TriggerPending), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pending triggers: %w", err)
	}
	defer rows.Close()

	var out []worker.Trigger
	for rows.Next() {
		var (
			t           worker.Trigger
			childSpec   []byte
			childRunID  *string
			processedAt *time.Time
		)
		if err := rows.Scan(&t.ID, &t.ParentRunID, &childSpec, &t.Status,
			&t.Attempts, &t.ErrorMessage, &childRunID, &t.CreatedAt, &processedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan trigger: %w", err)
		}
		if len(childSpec) > 0 {
			_ = json.Unmarshal(childSpec, &t.ChildSpec)
		}
		if childRunID != nil {
			t.ChildRunID = *childRunID
		}
		if processedAt != nil {
			t.ProcessedAt = *processedAt
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return out, nil
}

// MarkTriggerProcessing implements worker.TriggerStore.
func (s *Store) MarkTriggerProcessing(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_triggers SET status = $1 WHERE id = $2
	`, string(worker.TriggerProcessing), id)
	if err != nil {
		return fmt.Errorf("postgres: mark trigger processing: %w", err)
	}
	return nil
}

// MarkTriggerCompleted implements worker.TriggerStore.
func (s *Store) MarkTriggerCompleted(ctx context.Context, id string, childRunID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_triggers
		SET status = $1, child_run_id = $2, processed_at = NOW()
		WHERE id = $3
	`, string(worker.TriggerCompleted), childRunID, id)
	if err != nil {
		return fmt.Errorf("postgres: mark trigger completed: %w", err)
	}
	return nil
}

// IncrementTriggerAttempts implements worker.TriggerStore.
func (s *Store) IncrementTriggerAttempts(ctx context.Context, id string, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_triggers
		SET attempts = attempts + 1, error_message = $1, status = $2
		WHERE id = $3
	`, errMsg, string(worker.TriggerPending), id)
	if err != nil {
		return fmt.Errorf("postgres: increment trigger attempts: %w", err)
	}
	return nil
}

// MarkTriggerFailed implements worker.TriggerStore.
func (s *Store) MarkTriggerFailed(ctx context.Context, id string, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE workflow_triggers
		SET status = $1, error_message = $2, processed_at = NOW()
		WHERE id = $3
	`, string(worker.TriggerFailed), errMsg, id)
	if err != nil {
		return fmt.Errorf("postgres: mark trigger failed: %w", err)
	}
	return nil
}
