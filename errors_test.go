package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowErrorWrapping(t *testing.T) {
	// Test basic error creation
	err := NewWorkflowError(ErrorTypeTimeout, "operation timed out")
	require.Equal(t, "timeout: operation timed out", err.Error())
	require.Nil(t, err.Unwrap())

	// Test error wrapping
	originalErr := errors.New("network connection failed")
	wrappedErr := &WorkflowError{
		Type:    ErrorTypeTimeout,
		Cause:   originalErr.Error(),
		Wrapped: originalErr,
	}

	require.Equal(t, "timeout: network connection failed", wrappedErr.Error())
	require.Equal(t, originalErr, wrappedErr.Unwrap())

	// Test errors.Is
	require.True(t, errors.Is(wrappedErr, originalErr))

	// Test errors.As
	var wErr *WorkflowError
	require.True(t, errors.As(wrappedErr, &wErr))
	require.Equal(t, ErrorTypeTimeout, wErr.Type)
}

func TestErrorClassification(t *testing.T) {
	// Test timeout classification
	timeoutErr := context.DeadlineExceeded
	classified := ClassifyError(timeoutErr)
	require.Equal(t, ErrorTypeTimeout, classified.Type)
	require.True(t, errors.Is(classified, timeoutErr))

	// Test default classification
	genericErr := errors.New("something went wrong")
	classified = ClassifyError(genericErr)
	require.Equal(t, ErrorTypeActivityFailed, classified.Type)
	require.True(t, errors.Is(classified, genericErr))

	// Test WorkflowError passthrough
	originalWorkflowErr := NewWorkflowError(ErrorTypeFatal, "runtime error")
	classified = ClassifyError(originalWorkflowErr)
	require.Equal(t, originalWorkflowErr, classified)
}

func TestErrorMatching(t *testing.T) {
	timeoutErr := NewWorkflowError(ErrorTypeTimeout, "timeout")
	taskErr := NewWorkflowError(ErrorTypeActivityFailed, "task failed")
	fatalErr := NewWorkflowError(ErrorTypeFatal, "fatal error")

	// Test exact matching
	require.True(t, MatchesErrorType(timeoutErr, ErrorTypeTimeout))
	require.False(t, MatchesErrorType(timeoutErr, ErrorTypeActivityFailed))

	// Test ErrorTypeAll matching
	require.True(t, MatchesErrorType(timeoutErr, ErrorTypeAll))
	require.True(t, MatchesErrorType(taskErr, ErrorTypeAll))
	require.False(t, MatchesErrorType(fatalErr, ErrorTypeAll), "Fatal error should not match ErrorTypeAll")

	// Test ErrorTypeActivityFailed matching
	require.True(t, MatchesErrorType(taskErr, ErrorTypeActivityFailed))
	require.False(t, MatchesErrorType(timeoutErr, ErrorTypeActivityFailed))
}
