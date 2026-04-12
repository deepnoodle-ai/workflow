package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// UpdateStepProgress implements workflow.StepProgressStore.
func (s *Store) UpdateStepProgress(ctx context.Context, executionID string, p workflow.StepProgress) error {
	var detail []byte
	if p.Detail != nil {
		b, err := json.Marshal(p.Detail)
		if err != nil {
			return fmt.Errorf("sqlite: marshal progress detail: %w", err)
		}
		detail = b
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_step_progress (
			execution_id, step_name, branch_id, status, activity,
			attempt, detail, started_at, finished_at, error, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%f+00:00','now'))
		ON CONFLICT (execution_id, step_name, branch_id) DO UPDATE SET
			status      = excluded.status,
			activity    = excluded.activity,
			attempt     = excluded.attempt,
			detail      = excluded.detail,
			started_at  = excluded.started_at,
			finished_at = excluded.finished_at,
			error       = excluded.error,
			updated_at  = strftime('%Y-%m-%dT%H:%M:%f+00:00','now')
	`,
		executionID,
		p.StepName,
		p.BranchID,
		string(p.Status),
		p.ActivityName,
		p.Attempt,
		detail,
		nullableTime(p.StartedAt),
		nullableTime(p.FinishedAt),
		p.Error,
	)
	if err != nil {
		return fmt.Errorf("sqlite: upsert step progress %s/%s: %w", executionID, p.StepName, err)
	}
	return nil
}
