package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/experimental/worker"
)

type leasedCheckpointer struct {
	store *Store
	lease worker.Lease
}

// SaveCheckpoint implements workflow.Checkpointer. The checkpoint's
// ExecutionID must match the lease's RunID.
func (c *leasedCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *workflow.Checkpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("sqlite: nil checkpoint")
	}
	if checkpoint.ExecutionID != c.lease.RunID {
		return fmt.Errorf("sqlite: checkpoint execution ID %q does not match lease run ID %q",
			checkpoint.ExecutionID, c.lease.RunID)
	}
	blob, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("sqlite: marshal checkpoint: %w", err)
	}
	result, err := c.store.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET checkpoint = ?
		WHERE id         = ?
		  AND claimed_by = ?
		  AND attempt    = ?
	`, blob, c.lease.RunID, c.lease.WorkerID, c.lease.Attempt)
	if err != nil {
		return fmt.Errorf("sqlite: save checkpoint %s: %w", c.lease.RunID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}

// LoadCheckpoint implements workflow.Checkpointer.
func (c *leasedCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*workflow.Checkpoint, error) {
	var blob []byte
	err := c.store.db.QueryRowContext(ctx, `
		SELECT checkpoint FROM workflow_runs WHERE id = ?
	`, executionID).Scan(&blob)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, workflow.ErrNoCheckpoint
		}
		return nil, fmt.Errorf("sqlite: load checkpoint %s: %w", executionID, err)
	}
	if len(blob) == 0 {
		return nil, workflow.ErrNoCheckpoint
	}
	var cp workflow.Checkpoint
	if err := json.Unmarshal(blob, &cp); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal checkpoint %s: %w", executionID, err)
	}
	if cp.SchemaVersion < 1 || cp.SchemaVersion > workflow.CheckpointSchemaVersion {
		return nil, fmt.Errorf("sqlite: checkpoint schema version %d is not supported (supported: 1..%d)",
			cp.SchemaVersion, workflow.CheckpointSchemaVersion)
	}
	return &cp, nil
}

// DeleteCheckpoint implements workflow.Checkpointer with (claimed_by,
// attempt) fencing.
func (c *leasedCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	result, err := c.store.db.ExecContext(ctx, `
		UPDATE workflow_runs
		SET checkpoint = NULL
		WHERE id         = ?
		  AND claimed_by = ?
		  AND attempt    = ?
	`, executionID, c.lease.WorkerID, c.lease.Attempt)
	if err != nil {
		return fmt.Errorf("sqlite: delete checkpoint %s: %w", executionID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return worker.ErrLeaseLost
	}
	return nil
}
