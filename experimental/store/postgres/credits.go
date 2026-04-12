package postgres

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// Debit implements worker.CreditStore. Idempotent per (run_id, "debit").
func (s *Store) Debit(ctx context.Context, orgID, runID, workflowType string, amount int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_credit_ledger (id, org_id, run_id, workflow_type, amount, reason)
		VALUES ($1, $2, $3, $4, $5, 'debit')
		ON CONFLICT (run_id, reason) DO NOTHING
	`, generateID("crd_"), orgID, runID, workflowType, amount)
	if err != nil {
		return fmt.Errorf("postgres: debit credits: %w", err)
	}
	return nil
}

// Refund implements worker.CreditStore. Idempotent per (run_id, "refund").
func (s *Store) Refund(ctx context.Context, orgID, runID, workflowType string, amount int) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_credit_ledger (id, org_id, run_id, workflow_type, amount, reason)
		VALUES ($1, $2, $3, $4, $5, 'refund')
		ON CONFLICT (run_id, reason) DO NOTHING
	`, generateID("crd_"), orgID, runID, workflowType, -amount)
	if err != nil {
		return fmt.Errorf("postgres: refund credits: %w", err)
	}
	return nil
}

// HasRefund implements worker.CreditStore.
func (s *Store) HasRefund(ctx context.Context, orgID, runID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workflow_credit_ledger
			WHERE org_id = $1 AND run_id = $2 AND reason = 'refund'
		)
	`, orgID, runID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("postgres: has refund: %w", err)
	}
	return exists, nil
}

// Balance implements worker.CreditStore.
func (s *Store) Balance(ctx context.Context, orgID string) (int, error) {
	var balance int
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM workflow_credit_ledger
		WHERE org_id = $1
	`, orgID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("postgres: balance: %w", err)
	}
	return balance, nil
}

// ListUnrefunded implements worker.CreditStore.
func (s *Store) ListUnrefunded(ctx context.Context, limit int) ([]worker.UnrefundedRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.org_id, l.run_id, l.workflow_type, l.amount
		FROM workflow_credit_ledger l
		JOIN workflow_runs r ON r.id = l.run_id
		WHERE l.reason = 'debit'
		  AND r.status = $1
		  AND NOT EXISTS (
			SELECT 1 FROM workflow_credit_ledger r2
			WHERE r2.run_id = l.run_id AND r2.reason = 'refund'
		  )
		LIMIT $2
	`, string(worker.StatusFailed), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list unrefunded: %w", err)
	}
	defer rows.Close()

	var out []worker.UnrefundedRun
	for rows.Next() {
		var u worker.UnrefundedRun
		if err := rows.Scan(&u.OrgID, &u.RunID, &u.WorkflowType, &u.Amount); err != nil {
			return nil, fmt.Errorf("postgres: scan unrefunded: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
