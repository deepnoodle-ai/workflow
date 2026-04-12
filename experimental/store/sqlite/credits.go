package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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
