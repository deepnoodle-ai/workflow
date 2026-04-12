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
func (s *Store) NewCheckpointer(lease worker.Lease) workflow.Checkpointer {
	return &leasedCheckpointer{store: s, lease: lease}
}

type leasedCheckpointer struct {
	store *Store
	lease worker.Lease
}

// SaveCheckpoint marshals the checkpoint to JSON and writes it to the
// workflow_runs.checkpoint column under (claimed_by, attempt) fencing.
// The checkpoint's ExecutionID must match the lease's RunID.
func (c *leasedCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *workflow.Checkpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("postgres: nil checkpoint")
	}
	if checkpoint.ExecutionID != c.lease.RunID {
		return fmt.Errorf("postgres: checkpoint execution ID %q does not match lease run ID %q",
			checkpoint.ExecutionID, c.lease.RunID)
	}
	blob, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("postgres: marshal checkpoint: %w", err)
	}
	tag, err := c.store.pool.Exec(ctx, `
		UPDATE workflow_runs
		SET checkpoint = $1
		WHERE id         = $2
		  AND claimed_by = $3
		  AND attempt    = $4
	`, blob, c.lease.RunID, c.lease.WorkerID, c.lease.Attempt)
	if err != nil {
		return fmt.Errorf("postgres: save checkpoint %s: %w", c.lease.RunID, err)
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
	err := c.store.pool.QueryRow(ctx, `
		SELECT checkpoint FROM workflow_runs WHERE id = $1
	`, executionID).Scan(&blob)
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
	tag, err := c.store.pool.Exec(ctx, `
		UPDATE workflow_runs
		SET checkpoint = NULL
		WHERE id         = $1
		  AND claimed_by = $2
		  AND attempt    = $3
	`, c.lease.RunID, c.lease.WorkerID, c.lease.Attempt)
	if err != nil {
		return fmt.Errorf("postgres: delete checkpoint %s: %w", c.lease.RunID, err)
	}
	if tag.RowsAffected() == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}
