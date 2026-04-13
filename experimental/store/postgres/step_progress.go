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

	query := fmt.Sprintf(`
		INSERT INTO %s (
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
	`, s.t("workflow_step_progress"))

	_, err := s.pool.Exec(ctx, query,
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

// GetStepProgress returns every step progress row recorded for an
// execution, ordered by started_at (NULLS LAST) then step_name. One
// row per (step_name, branch_id). Returns an empty slice if no rows
// exist. Use this on the read side to render per-step status for a
// run whose identity came back from runquery.Store.GetRun, which
// intentionally does not carry step progress.
func (s *Store) GetStepProgress(ctx context.Context, executionID string) ([]workflow.StepProgress, error) {
	query := fmt.Sprintf(`
		SELECT step_name, branch_id, status, activity, attempt,
		       detail, started_at, finished_at, error
		FROM %s
		WHERE execution_id = $1
		ORDER BY started_at ASC NULLS LAST, step_name ASC, branch_id ASC
	`, s.t("workflow_step_progress"))

	rows, err := s.pool.Query(ctx, query, executionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: query step progress %s: %w", executionID, err)
	}
	defer rows.Close()

	var out []workflow.StepProgress
	for rows.Next() {
		var (
			p          workflow.StepProgress
			status     string
			detail     []byte
			startedAt  *time.Time
			finishedAt *time.Time
		)
		if err := rows.Scan(
			&p.StepName, &p.BranchID, &status, &p.ActivityName, &p.Attempt,
			&detail, &startedAt, &finishedAt, &p.Error,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan step progress: %w", err)
		}
		p.Status = workflow.StepStatus(status)
		if len(detail) > 0 {
			pd := &workflow.ProgressDetail{}
			if err := json.Unmarshal(detail, pd); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal progress detail for %s/%s: %w", executionID, p.StepName, err)
			}
			p.Detail = pd
		}
		if startedAt != nil {
			p.StartedAt = *startedAt
		}
		if finishedAt != nil {
			p.FinishedAt = *finishedAt
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate step progress %s: %w", executionID, err)
	}
	return out, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
