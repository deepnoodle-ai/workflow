package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

// NewCheckpointer returns a lease-fenced workflow.Checkpointer for
// the given claim. Writes fence on (claimed_by, attempt); a fencing
// failure returns worker.ErrLeaseLost from the SaveCheckpoint call.
//
// Reads (LoadCheckpoint) are unfenced: a fresh attempt must be able
// to resume regardless of which worker originally wrote the snapshot.
func (s *Store) NewCheckpointer(claim *worker.Claim) workflow.Checkpointer {
	return &leasedCheckpointer{store: s, claim: claim}
}

// NewStepProgressStore returns a workflow.StepProgressStore backed
// by this Store for the given claim. The current implementation
// ignores the claim (progress rows are not lease-fenced) but the
// signature matches HandlerStores so consumers can wire it directly
// into a worker.
func (s *Store) NewStepProgressStore(_ *worker.Claim) workflow.StepProgressStore {
	return s
}

// NewActivityLogger returns a workflow.ActivityLogger backed by
// this Store for the given claim. Activity log rows are append-only
// and not lease-fenced.
func (s *Store) NewActivityLogger(_ *worker.Claim) workflow.ActivityLogger {
	return s
}

type leasedCheckpointer struct {
	store *Store
	claim *worker.Claim
}

// SaveCheckpoint marshals the checkpoint to JSON and writes it to the
// workflow_runs.checkpoint column under (claimed_by, attempt) fencing.
// The checkpoint's ExecutionID must match the claim's run ID.
func (c *leasedCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *workflow.Checkpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("postgres: nil checkpoint")
	}
	if checkpoint.ExecutionID != c.claim.ID {
		return fmt.Errorf("postgres: checkpoint execution ID %q does not match claim run ID %q",
			checkpoint.ExecutionID, c.claim.ID)
	}
	blob, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("postgres: marshal checkpoint: %w", err)
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET checkpoint = $1
		WHERE id         = $2
		  AND claimed_by = $3
		  AND attempt    = $4
	`, c.store.t("workflow_runs"))
	tag, err := c.store.pool.Exec(ctx, query,
		blob, c.claim.ID, c.claim.WorkerID, c.claim.Attempt,
	)
	if err != nil {
		return fmt.Errorf("postgres: save checkpoint %s: %w", c.claim.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// LoadCheckpoint returns the most recent checkpoint for executionID.
// Returns workflow.ErrNoCheckpoint when no row exists or the row has
// a NULL checkpoint blob.
func (c *leasedCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*workflow.Checkpoint, error) {
	var blob []byte
	query := fmt.Sprintf(`SELECT checkpoint FROM %s WHERE id = $1`, c.store.t("workflow_runs"))
	err := c.store.pool.QueryRow(ctx, query, executionID).Scan(&blob)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workflow.ErrNoCheckpoint
		}
		return nil, fmt.Errorf("postgres: load checkpoint %s: %w", executionID, err)
	}
	if len(blob) == 0 {
		return nil, workflow.ErrNoCheckpoint
	}
	var cp workflow.Checkpoint
	if err := json.Unmarshal(blob, &cp); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal checkpoint %s: %w", executionID, err)
	}
	if cp.SchemaVersion < 1 || cp.SchemaVersion > workflow.CheckpointSchemaVersion {
		return nil, fmt.Errorf("postgres: checkpoint schema version %d is not supported (supported: 1..%d)",
			cp.SchemaVersion, workflow.CheckpointSchemaVersion)
	}
	return &cp, nil
}

// DeleteCheckpoint clears the checkpoint column under (claimed_by,
// attempt) fencing. The run row itself is left in place.
func (c *leasedCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	query := fmt.Sprintf(`
		UPDATE %s
		SET checkpoint = NULL
		WHERE id         = $1
		  AND claimed_by = $2
		  AND attempt    = $3
	`, c.store.t("workflow_runs"))
	tag, err := c.store.pool.Exec(ctx, query,
		c.claim.ID, c.claim.WorkerID, c.claim.Attempt,
	)
	if err != nil {
		return fmt.Errorf("postgres: delete checkpoint %s: %w", c.claim.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}
