package workflow

import (
	"context"
	"errors"
	"fmt"
)

// FenceFunc validates that the current worker still holds its lease or lock.
// Return nil if the fence is still valid. Return an error to abort the
// checkpoint save — the execution will receive the error and should terminate.
type FenceFunc func(ctx context.Context) error

// ErrFenceViolation is returned when a fence check fails, indicating the
// worker has lost its lease and should stop processing. ErrFenceViolation
// is always wrapped with the original fence check error for context.
//
// ErrFenceViolation bypasses retry and catch handlers. The engine treats it
// as non-retryable and non-catchable, similar to ErrorTypeFatal. A lost
// lease is not a recoverable activity error — retrying on the same worker
// is pointless and catching it would mask the real problem.
var ErrFenceViolation = errors.New("fence violation: lease lost")

// WithFencing wraps a Checkpointer with a pre-save fence validation. Before
// each SaveCheckpoint call, fenceCheck is called. If it returns an error, the
// save is aborted and the error is returned wrapped with ErrFenceViolation.
//
// LoadCheckpoint and DeleteCheckpoint pass through to the inner checkpointer
// without fence checks.
//
// Use this with distributed workers to prevent stale workers from overwriting
// checkpoint state after losing their lease.
func WithFencing(inner Checkpointer, fenceCheck FenceFunc) Checkpointer {
	return &fencedCheckpointer{inner: inner, fenceCheck: fenceCheck}
}

type fencedCheckpointer struct {
	inner      Checkpointer
	fenceCheck FenceFunc
}

func (f *fencedCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error {
	if err := f.fenceCheck(ctx); err != nil {
		if !errors.Is(err, ErrFenceViolation) {
			return fmt.Errorf("%w: %w", ErrFenceViolation, err)
		}
		return err
	}
	return f.inner.SaveCheckpoint(ctx, checkpoint)
}

func (f *fencedCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return f.inner.LoadCheckpoint(ctx, executionID)
}

func (f *fencedCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	return f.inner.DeleteCheckpoint(ctx, executionID)
}
