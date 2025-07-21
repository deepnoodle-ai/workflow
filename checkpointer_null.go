package workflow

import "context"

// NullCheckpointer is a no-op implementation
type NullCheckpointer struct{}

func NewNullCheckpointer() *NullCheckpointer {
	return &NullCheckpointer{}
}

func (c *NullCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error {
	return nil
}

func (c *NullCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return nil, nil
}

func (c *NullCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	return nil
}
