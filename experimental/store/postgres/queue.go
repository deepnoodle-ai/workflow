package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// Enqueue implements worker.QueueStore.
func (s *Store) Enqueue(ctx context.Context, run worker.NewRun) error {
	if run.ID == "" {
		return fmt.Errorf("postgres: NewRun.ID is required")
	}
	metadata, err := marshalMetadata(run.Metadata)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (
			id, spec, status,
			org_id, project_id, parent_run_id,
			workflow_type, initiated_by, credit_cost, callback_url, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, s.t("workflow_runs"))
	if _, err := s.pool.Exec(ctx, query,
		run.ID, run.Spec, string(worker.StatusQueued),
		nullableString(run.OrgID),
		nullableString(run.ProjectID),
		nullableString(run.ParentRunID),
		run.WorkflowType,
		nullableString(run.InitiatedBy),
		run.CreditCost,
		run.CallbackURL,
		metadata,
	); err != nil {
		return fmt.Errorf("postgres: enqueue run %s: %w", run.ID, err)
	}
	return nil
}

// ClaimQueued implements worker.QueueStore using SELECT ... FOR UPDATE
// SKIP LOCKED to atomically claim the oldest queued run.
func (s *Store) ClaimQueued(ctx context.Context, workerID string) (*worker.Claim, error) {
	if workerID == "" {
		return nil, fmt.Errorf("postgres: workerID is required")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("postgres: begin claim tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		id           string
		spec         []byte
		attempt      int
		orgID        *string
		projectID    *string
		parentRunID  *string
		workflowType string
		initiatedBy  *string
		creditCost   int
		callbackURL  string
		metadataRaw  []byte
	)
	selectQuery := fmt.Sprintf(`
		SELECT id, spec, attempt,
		       org_id, project_id, parent_run_id,
		       workflow_type, initiated_by, credit_cost, callback_url, metadata
		FROM %s
		WHERE status = $1
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, s.t("workflow_runs"))
	err = tx.QueryRow(ctx, selectQuery, string(worker.StatusQueued)).Scan(
		&id, &spec, &attempt,
		&orgID, &projectID, &parentRunID,
		&workflowType, &initiatedBy, &creditCost, &callbackURL, &metadataRaw,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: select queued: %w", err)
	}

	newAttempt := attempt + 1
	updateQuery := fmt.Sprintf(`
		UPDATE %s
		SET status       = $1,
		    claimed_by   = $2,
		    heartbeat_at = NOW(),
		    started_at   = COALESCE(started_at, NOW()),
		    attempt      = $3
		WHERE id = $4
	`, s.t("workflow_runs"))
	if _, err := tx.Exec(ctx, updateQuery,
		string(worker.StatusRunning), workerID, newAttempt, id,
	); err != nil {
		return nil, fmt.Errorf("postgres: claim update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: claim commit: %w", err)
	}

	metadata, err := unmarshalMetadata(metadataRaw)
	if err != nil {
		return nil, fmt.Errorf("postgres: unmarshal run metadata %s: %w", id, err)
	}

	return &worker.Claim{
		ID:           id,
		Spec:         spec,
		Attempt:      newAttempt,
		WorkerID:     workerID,
		OrgID:        deref(orgID),
		ProjectID:    deref(projectID),
		ParentRunID:  deref(parentRunID),
		WorkflowType: workflowType,
		InitiatedBy:  deref(initiatedBy),
		CreditCost:   creditCost,
		CallbackURL:  callbackURL,
		Metadata:     metadata,
	}, nil
}

// Heartbeat implements worker.QueueStore with (claimed_by, attempt)
// fencing. Rows with a status other than running, or a mismatched
// lease, produce ErrLeaseLost.
func (s *Store) Heartbeat(ctx context.Context, claim *worker.Claim) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET heartbeat_at = NOW()
		WHERE id         = $1
		  AND claimed_by = $2
		  AND attempt    = $3
		  AND status     = $4
	`, s.t("workflow_runs"))
	tag, err := s.pool.Exec(ctx, query,
		claim.ID, claim.WorkerID, claim.Attempt, string(worker.StatusRunning),
	)
	if err != nil {
		return fmt.Errorf("postgres: heartbeat %s: %w", claim.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// Complete implements worker.QueueStore with (claimed_by, attempt) fencing.
func (s *Store) Complete(ctx context.Context, claim *worker.Claim, outcome worker.Outcome) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status        = $1,
		    result        = $2,
		    error_message = $3,
		    completed_at  = CASE WHEN $1 IN ($6, $7) THEN NOW() ELSE completed_at END
		WHERE id         = $4
		  AND claimed_by = $5
		  AND attempt    = $8
	`, s.t("workflow_runs"))
	tag, err := s.pool.Exec(ctx, query,
		string(outcome.Status),
		outcome.Result,
		outcome.ErrorMessage,
		claim.ID,
		claim.WorkerID,
		string(worker.StatusCompleted),
		string(worker.StatusFailed),
		claim.Attempt,
	)
	if err != nil {
		return fmt.Errorf("postgres: complete %s: %w", claim.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// ReclaimStale implements worker.QueueStore.
func (s *Store) ReclaimStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) (int, error) {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status       = $1,
		    claimed_by   = '',
		    heartbeat_at = NULL,
		    started_at   = NULL
		WHERE status       = $2
		  AND heartbeat_at < $3
		  AND attempt      < $4
		  AND NOT (id = ANY($5::text[]))
	`, s.t("workflow_runs"))
	tag, err := s.pool.Exec(ctx, query,
		string(worker.StatusQueued),
		string(worker.StatusRunning),
		staleBefore,
		maxAttempts,
		coalesceIDs(excludeIDs),
	)
	if err != nil {
		return 0, fmt.Errorf("postgres: reclaim stale: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// DeadLetterStale implements worker.QueueStore. Returns the metadata
// for each dead-lettered run so the worker can refund credits inline.
func (s *Store) DeadLetterStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) ([]worker.DeadLetteredRun, error) {
	query := fmt.Sprintf(`
		UPDATE %s
		SET status        = $1,
		    error_message = $2,
		    claimed_by    = '',
		    heartbeat_at  = NULL,
		    completed_at  = NOW()
		WHERE status       = $3
		  AND heartbeat_at < $4
		  AND attempt      >= $5
		  AND NOT (id = ANY($6::text[]))
		RETURNING id, COALESCE(org_id, ''), workflow_type, credit_cost
	`, s.t("workflow_runs"))
	rows, err := s.pool.Query(ctx, query,
		string(worker.StatusFailed),
		fmt.Sprintf("exceeded max retry attempts (%d)", maxAttempts),
		string(worker.StatusRunning),
		staleBefore,
		maxAttempts,
		coalesceIDs(excludeIDs),
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: dead-letter stale: %w", err)
	}
	defer rows.Close()

	var out []worker.DeadLetteredRun
	for rows.Next() {
		var d worker.DeadLetteredRun
		if err := rows.Scan(&d.ID, &d.OrgID, &d.WorkflowType, &d.CreditCost); err != nil {
			return nil, fmt.Errorf("postgres: scan dead-letter: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListFailedWithCredits implements worker.QueueStore by joining
// workflow_runs against the credit ledger: runs in StatusFailed with
// a matching debit but no matching refund.
func (s *Store) ListFailedWithCredits(ctx context.Context, limit int) ([]worker.FailedRun, error) {
	if limit <= 0 {
		limit = 50
	}
	query := fmt.Sprintf(`
		SELECT r.id, COALESCE(r.org_id, ''), r.workflow_type, r.credit_cost
		FROM %s r
		JOIN %s l ON l.run_id = r.id AND l.reason = 'debit'
		WHERE r.status = $1
		  AND r.credit_cost > 0
		  AND NOT EXISTS (
			SELECT 1 FROM %s l2
			WHERE l2.run_id = r.id AND l2.reason = 'refund'
		  )
		ORDER BY r.completed_at ASC NULLS LAST
		LIMIT $2
	`,
		s.t("workflow_runs"),
		s.t("workflow_credit_ledger"),
		s.t("workflow_credit_ledger"),
	)
	rows, err := s.pool.Query(ctx, query, string(worker.StatusFailed), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list failed with credits: %w", err)
	}
	defer rows.Close()

	var out []worker.FailedRun
	for rows.Next() {
		var f worker.FailedRun
		if err := rows.Scan(&f.ID, &f.OrgID, &f.WorkflowType, &f.CreditCost); err != nil {
			return nil, fmt.Errorf("postgres: scan failed run: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// --- helpers ---

// coalesceIDs returns a non-nil slice; pgx serializes nil slices to
// NULL, which breaks NOT (id = ANY(...)).
func coalesceIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// nullableString returns nil for an empty string so the INSERT
// stores a real NULL instead of a sentinel empty value.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// deref returns the empty string for a nil *string and the
// pointed-to value otherwise. Used at the read boundary to keep
// the Go API free of *string.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func marshalMetadata(m map[string]string) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("postgres: marshal run metadata: %w", err)
	}
	return b, nil
}

func unmarshalMetadata(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
