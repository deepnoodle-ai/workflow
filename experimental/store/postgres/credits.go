package postgres

import (
	"context"
	"fmt"
)

// Debit implements worker.CreditStore. Idempotent per (run_id, "debit").
func (s *Store) Debit(ctx context.Context, orgID, runID, workflowType string, amount int) error {
	id, err := generateID("crd_")
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (id, org_id, run_id, workflow_type, amount, reason)
		VALUES ($1, $2, $3, $4, $5, 'debit')
		ON CONFLICT (run_id, reason) DO NOTHING
	`, s.t("workflow_credit_ledger"))
	if _, err := s.pool.Exec(ctx, query, id, orgID, runID, workflowType, amount); err != nil {
		return fmt.Errorf("postgres: debit credits: %w", err)
	}
	return nil
}

// Refund implements worker.CreditStore. Idempotent per (run_id, "refund").
func (s *Store) Refund(ctx context.Context, orgID, runID, workflowType string, amount int) error {
	id, err := generateID("crd_")
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (id, org_id, run_id, workflow_type, amount, reason)
		VALUES ($1, $2, $3, $4, $5, 'refund')
		ON CONFLICT (run_id, reason) DO NOTHING
	`, s.t("workflow_credit_ledger"))
	if _, err := s.pool.Exec(ctx, query, id, orgID, runID, workflowType, -amount); err != nil {
		return fmt.Errorf("postgres: refund credits: %w", err)
	}
	return nil
}

// HasRefund implements worker.CreditStore.
func (s *Store) HasRefund(ctx context.Context, orgID, runID string) (bool, error) {
	var exists bool
	query := fmt.Sprintf(`
		SELECT EXISTS(
			SELECT 1 FROM %s
			WHERE org_id = $1 AND run_id = $2 AND reason = 'refund'
		)
	`, s.t("workflow_credit_ledger"))
	err := s.pool.QueryRow(ctx, query, orgID, runID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("postgres: has refund: %w", err)
	}
	return exists, nil
}

// Balance implements worker.CreditStore.
func (s *Store) Balance(ctx context.Context, orgID string) (int, error) {
	var balance int
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(amount), 0)
		FROM %s
		WHERE org_id = $1
	`, s.t("workflow_credit_ledger"))
	if err := s.pool.QueryRow(ctx, query, orgID).Scan(&balance); err != nil {
		return 0, fmt.Errorf("postgres: balance: %w", err)
	}
	return balance, nil
}
