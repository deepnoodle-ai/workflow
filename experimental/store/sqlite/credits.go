package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// Debit implements worker.CreditStore. Idempotent per (run_id, "debit").
func (s *Store) Debit(ctx context.Context, orgID, runID, workflowType string, amount int) error {
	id, err := generateID("crd_")
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_credit_ledger (id, org_id, run_id, workflow_type, amount, reason)
		VALUES (?, ?, ?, ?, ?, 'debit')
		ON CONFLICT (run_id, reason) DO NOTHING
	`, id, orgID, runID, workflowType, amount)
	if err != nil {
		return fmt.Errorf("sqlite: debit credits: %w", err)
	}
	return nil
}

// Refund implements worker.CreditStore. Idempotent per (run_id, "refund").
func (s *Store) Refund(ctx context.Context, orgID, runID, workflowType string, amount int) error {
	id, err := generateID("crd_")
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_credit_ledger (id, org_id, run_id, workflow_type, amount, reason)
		VALUES (?, ?, ?, ?, ?, 'refund')
		ON CONFLICT (run_id, reason) DO NOTHING
	`, id, orgID, runID, workflowType, -amount)
	if err != nil {
		return fmt.Errorf("sqlite: refund credits: %w", err)
	}
	return nil
}

// HasRefund implements worker.CreditStore.
func (s *Store) HasRefund(ctx context.Context, orgID, runID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workflow_credit_ledger
			WHERE org_id = ? AND run_id = ? AND reason = 'refund'
		)
	`, orgID, runID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("sqlite: has refund: %w", err)
	}
	return exists, nil
}

// Balance implements worker.CreditStore.
func (s *Store) Balance(ctx context.Context, orgID string) (int, error) {
	var balance sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT SUM(amount)
		FROM workflow_credit_ledger
		WHERE org_id = ?
	`, orgID).Scan(&balance)
	if err != nil {
		return 0, fmt.Errorf("sqlite: balance: %w", err)
	}
	if !balance.Valid {
		return 0, nil
	}
	return int(balance.Int64), nil
}

// ListUnrefunded implements worker.CreditStore.
func (s *Store) ListUnrefunded(ctx context.Context, limit int) ([]worker.UnrefundedRun, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.org_id, l.run_id, l.workflow_type, l.amount
		FROM workflow_credit_ledger l
		JOIN workflow_runs r ON r.id = l.run_id
		WHERE l.reason = 'debit'
		  AND r.status = ?
		  AND NOT EXISTS (
			SELECT 1 FROM workflow_credit_ledger r2
			WHERE r2.run_id = l.run_id AND r2.reason = 'refund'
		  )
		LIMIT ?
	`, string(worker.StatusFailed), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list unrefunded: %w", err)
	}
	defer rows.Close()

	var out []worker.UnrefundedRun
	for rows.Next() {
		var u worker.UnrefundedRun
		if err := rows.Scan(&u.OrgID, &u.RunID, &u.WorkflowType, &u.Amount); err != nil {
			return nil, fmt.Errorf("sqlite: scan unrefunded: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
