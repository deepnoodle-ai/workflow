package workflow

import (
	"context"
)

// Checkpointer defines simple checkpoint interface
type Checkpointer interface {
	// SaveCheckpoint saves the current execution state
	SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error

	// LoadCheckpoint loads the latest checkpoint for an execution
	LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error)

	// DeleteCheckpoint removes checkpoint data for an execution
	DeleteCheckpoint(ctx context.Context, executionID string) error
}
