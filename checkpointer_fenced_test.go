package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFencedCheckpointerPassesOnValidFence(t *testing.T) {
	inner := NewNullCheckpointer()
	fenced := WithFencing(inner, func(ctx context.Context) error {
		return nil // fence is valid
	})

	err := fenced.SaveCheckpoint(context.Background(), &Checkpoint{
		ExecutionID: "test-1",
	})
	require.NoError(t, err)
}

func TestFencedCheckpointerRejectsOnLostLease(t *testing.T) {
	inner := NewNullCheckpointer()
	fenced := WithFencing(inner, func(ctx context.Context) error {
		return fmt.Errorf("lease expired for worker-7")
	})

	err := fenced.SaveCheckpoint(context.Background(), &Checkpoint{
		ExecutionID: "test-1",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFenceViolation))
	require.Contains(t, err.Error(), "lease expired for worker-7")
}

func TestFencedCheckpointerDoesNotFenceOnLoad(t *testing.T) {
	fenceCalled := false
	inner := NewNullCheckpointer()
	fenced := WithFencing(inner, func(ctx context.Context) error {
		fenceCalled = true
		return fmt.Errorf("should not be called")
	})

	_, err := fenced.LoadCheckpoint(context.Background(), "any-id")
	require.NoError(t, err)
	require.False(t, fenceCalled)
}

func TestFenceViolationBypassesRetryAndCatch(t *testing.T) {
	// Wrap an error with ErrFenceViolation
	fenceErr := fmt.Errorf("%w: lease lost for worker-3", ErrFenceViolation)

	// MatchesErrorType should return false for ALL error types — fence errors
	// are never retryable or catchable
	require.False(t, MatchesErrorType(fenceErr, ErrorTypeAll))
	require.False(t, MatchesErrorType(fenceErr, ErrorTypeActivityFailed))
	require.False(t, MatchesErrorType(fenceErr, ErrorTypeTimeout))
	require.False(t, MatchesErrorType(fenceErr, ErrorTypeFatal))
	require.False(t, MatchesErrorType(fenceErr, "custom-error"))
}

func TestFencedCheckpointerDoesNotDoubleWrap(t *testing.T) {
	inner := NewNullCheckpointer()
	// Consumer returns an error that already wraps ErrFenceViolation
	fenced := WithFencing(inner, func(ctx context.Context) error {
		return fmt.Errorf("%w: detected externally", ErrFenceViolation)
	})

	err := fenced.SaveCheckpoint(context.Background(), &Checkpoint{
		ExecutionID: "test-1",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrFenceViolation))
}
