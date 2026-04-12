package worker

import "context"

// UnrefundedRun represents a failed run that was debited but not yet
// refunded. Used by the credit reconciler.
type UnrefundedRun struct {
	OrgID        string
	RunID        string
	WorkflowType string
	Amount       int
}

// CreditStore tracks credit debits and refunds per workflow run.
// Implementations must make Debit and Refund idempotent per run ID
// so that retries and the reconciler cannot double-charge or
// double-refund.
type CreditStore interface {
	// Debit records a credit charge for a run. Idempotent: calling
	// Debit twice for the same runID is a no-op.
	Debit(ctx context.Context, orgID, runID, workflowType string, amount int) error

	// Refund records a credit refund for a failed run. Idempotent:
	// calling Refund twice for the same runID is a no-op.
	Refund(ctx context.Context, orgID, runID, workflowType string, amount int) error

	// HasRefund reports whether a refund exists for the given run.
	HasRefund(ctx context.Context, orgID, runID string) (bool, error)

	// Balance returns the net credit balance for an org. Positive
	// means credits consumed; negative means net refunds.
	Balance(ctx context.Context, orgID string) (int, error)

	// ListUnrefunded returns failed runs that have a debit but no
	// matching refund. Used by the credit reconciler.
	ListUnrefunded(ctx context.Context, limit int) ([]UnrefundedRun, error)
}
