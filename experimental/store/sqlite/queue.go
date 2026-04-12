package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// Enqueue implements worker.QueueStore.
func (s *Store) Enqueue(ctx context.Context, run worker.NewRun) error {
	if run.ID == "" {
		return fmt.Errorf("sqlite: NewRun.ID is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workflow_runs (id, spec, status, org_id, workflow_type, initiated_by, credit_cost, callback_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, run.ID, run.Spec, string(worker.StatusQueued),
		run.OrgID, run.WorkflowType, run.InitiatedBy, run.CreditCost, run.CallbackURL)
	if err != nil {
		return fmt.Errorf("sqlite: enqueue run %s: %w", run.ID, err)
	}
	return nil
}

// ClaimQueued implements worker.QueueStore. Uses an atomic
// UPDATE...RETURNING with a subquery to avoid TOCTOU races — the
// row selection and claim happen in a single statement.
func (s *Store) ClaimQueued(ctx context.Context, workerID string) (*worker.Claim, error) {
	if workerID == "" {
		return nil, fmt.Errorf("sqlite: workerID is required")
	}
	now := time.Now().UTC().Format(timeFormat)
	var (
		id           string
		spec         []byte
		attempt      int
		orgID        string
		workflowType string
		creditCost   int
		callbackURL  string
	)
	err := s.db.QueryRowContext(ctx, `
		UPDATE workflow_runs
		SET status       = ?,
		    claimed_by   = ?,
		    heartbeat_at = ?,
		    started_at   = COALESCE(started_at, ?),
		    attempt      = attempt + 1
		WHERE id = (
			SELECT id FROM workflow_runs
			WHERE status = ?
			ORDER BY created_at ASC, id ASC
			LIMIT 1
		)
		RETURNING id, spec, attempt, org_id, workflow_type, credit_cost, callback_url
	`, string(worker.StatusRunning), workerID, now, now, string(worker.StatusQueued)).Scan(
		&id, &spec, &attempt, &orgID, &workflowType, &creditCost, &callbackURL)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlite: claim queued: %w", err)
	}

	return &worker.Claim{
		ID:           id,
		Spec:         spec,
		Attempt:      attempt,
		OrgID:        orgID,
		WorkflowType: workflowType,
		CreditCost:   creditCost,
		CallbackURL:  callbackURL,
	}, nil
}

// Heartbeat implements worker.QueueStore.
func (s *Store) Heartbeat(ctx context.Context, lease worker.Lease) error {
	now := time.Now().UTC().Format(timeFormat)
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET heartbeat_at = ?
		WHERE id         = ?
		  AND claimed_by = ?
		  AND attempt    = ?
		  AND status     = ?
	`, now, lease.RunID, lease.WorkerID, lease.Attempt, string(worker.StatusRunning))
	if err != nil {
		return fmt.Errorf("sqlite: heartbeat %s: %w", lease.RunID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// Complete implements worker.QueueStore.
func (s *Store) Complete(ctx context.Context, lease worker.Lease, outcome worker.Outcome) error {
	completedAt := nullableTime(time.Time{})
	if outcome.Status == worker.StatusCompleted || outcome.Status == worker.StatusFailed {
		completedAt = nullableTime(time.Now())
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET status        = ?,
		    result        = ?,
		    error_message = ?,
		    completed_at  = COALESCE(?, completed_at)
		WHERE id         = ?
		  AND claimed_by = ?
		  AND attempt    = ?
	`,
		string(outcome.Status),
		outcome.Result,
		outcome.ErrorMessage,
		completedAt,
		lease.RunID,
		lease.WorkerID,
		lease.Attempt,
	)
	if err != nil {
		return fmt.Errorf("sqlite: complete %s: %w", lease.RunID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// ReclaimStale implements worker.QueueStore.
func (s *Store) ReclaimStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) (int, error) {
	query := `UPDATE workflow_runs
		SET status = ?, claimed_by = '', heartbeat_at = NULL
		WHERE status = ? AND heartbeat_at < ? AND attempt < ?`
	args := []any{
		string(worker.StatusQueued),
		string(worker.StatusRunning),
		staleBefore.UTC().Format(timeFormat),
		maxAttempts,
	}
	if len(excludeIDs) > 0 {
		placeholders := make([]string, len(excludeIDs))
		for i, id := range excludeIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += " AND id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("sqlite: reclaim stale: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// DeadLetterStale implements worker.QueueStore.
func (s *Store) DeadLetterStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) ([]string, error) {
	now := time.Now().UTC().Format(timeFormat)
	query := `UPDATE workflow_runs
		SET status = ?, error_message = ?, claimed_by = '', heartbeat_at = NULL, completed_at = ?
		WHERE status = ? AND heartbeat_at < ? AND attempt >= ?`
	args := []any{
		string(worker.StatusFailed),
		fmt.Sprintf("exceeded max retry attempts (%d)", maxAttempts),
		now,
		string(worker.StatusRunning),
		staleBefore.UTC().Format(timeFormat),
		maxAttempts,
	}
	if len(excludeIDs) > 0 {
		placeholders := make([]string, len(excludeIDs))
		for i, id := range excludeIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query += " AND id NOT IN (" + strings.Join(placeholders, ",") + ")"
	}
	query += " RETURNING id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: dead-letter stale: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite: scan dead-letter id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
