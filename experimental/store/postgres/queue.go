package postgres

import (
	"context"
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
	_, err := s.pool.Exec(ctx, `
		INSERT INTO workflow_runs (id, spec, status, org_id, workflow_type, initiated_by, credit_cost, callback_url)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, run.ID, run.Spec, string(worker.StatusQueued),
		run.OrgID, run.WorkflowType, run.InitiatedBy, run.CreditCost, run.CallbackURL)
	if err != nil {
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
		orgID        string
		workflowType string
		creditCost   int
		callbackURL  string
	)
	err = tx.QueryRow(ctx, `
		SELECT id, spec, attempt, org_id, workflow_type, credit_cost, callback_url
		FROM workflow_runs
		WHERE status = $1
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, string(worker.StatusQueued)).Scan(&id, &spec, &attempt, &orgID, &workflowType, &creditCost, &callbackURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: select queued: %w", err)
	}

	newAttempt := attempt + 1
	_, err = tx.Exec(ctx, `
		UPDATE workflow_runs
		SET status       = $1,
		    claimed_by   = $2,
		    heartbeat_at = NOW(),
		    started_at   = COALESCE(started_at, NOW()),
		    attempt      = $3
		WHERE id = $4
	`, string(worker.StatusRunning), workerID, newAttempt, id)
	if err != nil {
		return nil, fmt.Errorf("postgres: claim update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("postgres: claim commit: %w", err)
	}

	return &worker.Claim{
		ID:           id,
		Spec:         spec,
		Attempt:      newAttempt,
		WorkerID:     workerID,
		OrgID:        orgID,
		WorkflowType: workflowType,
		CreditCost:   creditCost,
		CallbackURL:  callbackURL,
	}, nil
}

// Heartbeat implements worker.QueueStore with (claimed_by, attempt)
// fencing. Rows with a status other than running, or a mismatched
// lease, produce ErrLeaseLost.
func (s *Store) Heartbeat(ctx context.Context, claim *worker.Claim) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs
		SET heartbeat_at = NOW()
		WHERE id         = $1
		  AND claimed_by = $2
		  AND attempt    = $3
		  AND status     = $4
	`, claim.ID, claim.WorkerID, claim.Attempt, string(worker.StatusRunning))
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
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs
		SET status        = $1,
		    result        = $2,
		    error_message = $3,
		    completed_at  = CASE WHEN $1 IN ($6, $7) THEN NOW() ELSE completed_at END
		WHERE id         = $4
		  AND claimed_by = $5
		  AND attempt    = $8
	`,
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
	tag, err := s.pool.Exec(ctx, `
		UPDATE workflow_runs
		SET status       = $1,
		    claimed_by   = '',
		    heartbeat_at = NULL,
		    started_at   = NULL
		WHERE status       = $2
		  AND heartbeat_at < $3
		  AND attempt      < $4
		  AND NOT (id = ANY($5::text[]))
	`,
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

// DeadLetterStale implements worker.QueueStore.
func (s *Store) DeadLetterStale(ctx context.Context, staleBefore time.Time, maxAttempts int, excludeIDs []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE workflow_runs
		SET status        = $1,
		    error_message = $2,
		    claimed_by    = '',
		    heartbeat_at  = NULL,
		    completed_at  = NOW()
		WHERE status       = $3
		  AND heartbeat_at < $4
		  AND attempt      >= $5
		  AND NOT (id = ANY($6::text[]))
		RETURNING id
	`,
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

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("postgres: scan dead-letter id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// coalesceIDs returns a non-nil slice; pgx serializes nil slices to
// NULL, which breaks NOT (id = ANY(...)).
func coalesceIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}
