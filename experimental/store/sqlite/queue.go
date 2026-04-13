package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
	metadata, err := marshalMetadata(run.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workflow_runs (
			id, spec, status,
			org_id, project_id, parent_run_id,
			workflow_type, initiated_by, credit_cost, callback_url, metadata
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		run.ID, run.Spec, string(worker.StatusQueued),
		nullableString(run.OrgID),
		nullableString(run.ProjectID),
		nullableString(run.ParentRunID),
		run.WorkflowType,
		nullableString(run.InitiatedBy),
		run.CreditCost,
		run.CallbackURL,
		metadata,
	)
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
		orgID        sql.NullString
		projectID    sql.NullString
		parentRunID  sql.NullString
		workflowType string
		initiatedBy  sql.NullString
		creditCost   int
		callbackURL  string
		metadataRaw  sql.NullString
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
		RETURNING id, spec, attempt,
		          org_id, project_id, parent_run_id,
		          workflow_type, initiated_by, credit_cost, callback_url, metadata
	`, string(worker.StatusRunning), workerID, now, now, string(worker.StatusQueued)).Scan(
		&id, &spec, &attempt,
		&orgID, &projectID, &parentRunID,
		&workflowType, &initiatedBy, &creditCost, &callbackURL, &metadataRaw,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("sqlite: claim queued: %w", err)
	}

	metadata, err := unmarshalMetadata(metadataRaw)
	if err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal run metadata %s: %w", id, err)
	}

	return &worker.Claim{
		ID:           id,
		Spec:         spec,
		Attempt:      attempt,
		WorkerID:     workerID,
		OrgID:        orgID.String,
		ProjectID:    projectID.String,
		ParentRunID:  parentRunID.String,
		WorkflowType: workflowType,
		InitiatedBy:  initiatedBy.String,
		CreditCost:   creditCost,
		CallbackURL:  callbackURL,
		Metadata:     metadata,
	}, nil
}

// Heartbeat implements worker.QueueStore.
func (s *Store) Heartbeat(ctx context.Context, claim *worker.Claim) error {
	now := time.Now().UTC().Format(timeFormat)
	result, err := s.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET heartbeat_at = ?
		WHERE id         = ?
		  AND claimed_by = ?
		  AND attempt    = ?
		  AND status     = ?
	`, now, claim.ID, claim.WorkerID, claim.Attempt, string(worker.StatusRunning))
	if err != nil {
		return fmt.Errorf("sqlite: heartbeat %s: %w", claim.ID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// Complete implements worker.QueueStore.
func (s *Store) Complete(ctx context.Context, claim *worker.Claim, outcome worker.Outcome) error {
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
		claim.ID,
		claim.WorkerID,
		claim.Attempt,
	)
	if err != nil {
		return fmt.Errorf("sqlite: complete %s: %w", claim.ID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// ReclaimStale implements worker.QueueStore. Clears started_at so a
// reclaimed run has no stale start timestamp — it will be re-stamped
// on the next claim.
func (s *Store) ReclaimStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) (int, error) {
	query := `UPDATE workflow_runs
		SET status = ?, claimed_by = '', heartbeat_at = NULL, started_at = NULL
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
func (s *Store) DeadLetterStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) ([]worker.DeadLetteredRun, error) {
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
	query += " RETURNING id, COALESCE(org_id, ''), workflow_type, credit_cost"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: dead-letter stale: %w", err)
	}
	defer rows.Close()

	var out []worker.DeadLetteredRun
	for rows.Next() {
		var d worker.DeadLetteredRun
		if err := rows.Scan(&d.ID, &d.OrgID, &d.WorkflowType, &d.CreditCost); err != nil {
			return nil, fmt.Errorf("sqlite: scan dead-letter: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListRefundPending implements worker.QueueStore.
func (s *Store) ListRefundPending(ctx context.Context, limit int) ([]worker.FailedRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, COALESCE(r.org_id, ''), r.workflow_type, r.credit_cost
		FROM workflow_runs r
		JOIN workflow_credit_ledger l ON l.run_id = r.id AND l.reason = 'debit'
		WHERE r.status = ?
		  AND r.credit_cost > 0
		  AND NOT EXISTS (
			SELECT 1 FROM workflow_credit_ledger l2
			WHERE l2.run_id = r.id AND l2.reason = 'refund'
		  )
		ORDER BY r.completed_at ASC
		LIMIT ?
	`, string(worker.StatusFailed), limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list failed with credits: %w", err)
	}
	defer rows.Close()

	var out []worker.FailedRun
	for rows.Next() {
		var f worker.FailedRun
		if err := rows.Scan(&f.ID, &f.OrgID, &f.WorkflowType, &f.CreditCost); err != nil {
			return nil, fmt.Errorf("sqlite: scan failed run: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// --- helpers ---

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func marshalMetadata(m map[string]string) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("sqlite: marshal run metadata: %w", err)
	}
	return string(b), nil
}

func unmarshalMetadata(raw sql.NullString) (map[string]string, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
