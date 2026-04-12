package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// UpdateStepProgress implements workflow.StepProgressStore by
// upserting into workflow_step_progress. Keyed on
// (execution_id, step_name, branch_id) — a step running on two
// branches produces two rows.
func (s *Store) UpdateStepProgress(ctx context.Context, executionID string, p workflow.StepProgress) error {
	var detail []byte
	if p.Detail != nil {
		b, err := json.Marshal(p.Detail)
		if err != nil {
			return fmt.Errorf("postgres: marshal progress detail: %w", err)
		}
		detail = b
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_step_progress (
			execution_id, step_name, branch_id, status, activity,
			attempt, detail, started_at, finished_at, error, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
		ON CONFLICT (execution_id, step_name, branch_id) DO UPDATE SET
			status      = EXCLUDED.status,
			activity    = EXCLUDED.activity,
			attempt     = EXCLUDED.attempt,
			detail      = EXCLUDED.detail,
			started_at  = EXCLUDED.started_at,
			finished_at = EXCLUDED.finished_at,
			error       = EXCLUDED.error,
			updated_at  = NOW()
	`,
		executionID,
		p.StepName,
		p.BranchID,
		string(p.Status),
		p.ActivityName,
		p.Attempt,
		detail,
		nullTime(p.StartedAt),
		nullTime(p.FinishedAt),
		p.Error,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert step progress %s/%s: %w", executionID, p.StepName, err)
	}
	return nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
