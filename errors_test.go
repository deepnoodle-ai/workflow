package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestWorkflowErrorWrapping(t *testing.T) {
	// Test basic error creation
	err := NewWorkflowError(ErrorTypeTimeout, "operation timed out")
	assert.Equal(t, err.Error(), "timeout: operation timed out")
	assert.Nil(t, err.Unwrap())

	// Test error wrapping
	originalErr := errors.New("network connection failed")
	wrappedErr := &WorkflowError{
		Type:    ErrorTypeTimeout,
		Cause:   originalErr.Error(),
		Wrapped: originalErr,
	}

	assert.Equal(t, wrappedErr.Error(), "timeout: network connection failed")
	assert.Equal(t, wrappedErr.Unwrap(), originalErr)

	// Test errors.Is
	assert.True(t, errors.Is(wrappedErr, originalErr))

	// Test errors.As
	var wErr *WorkflowError
	assert.True(t, errors.As(wrappedErr, &wErr))
	assert.Equal(t, wErr.Type, ErrorTypeTimeout)
}

func TestErrorClassification(t *testing.T) {
	// Test timeout classification
	timeoutErr := context.DeadlineExceeded
	classified := ClassifyError(timeoutErr)
	assert.Equal(t, classified.Type, ErrorTypeTimeout)
	assert.True(t, errors.Is(classified, timeoutErr))

	// Test default classification
	genericErr := errors.New("something went wrong")
	classified = ClassifyError(genericErr)
	assert.Equal(t, classified.Type, ErrorTypeActivityFailed)
	assert.True(t, errors.Is(classified, genericErr))

	// Test WorkflowError passthrough
	originalWorkflowErr := NewWorkflowError(ErrorTypeFatal, "runtime error")
	classified = ClassifyError(originalWorkflowErr)
	assert.Equal(t, classified, originalWorkflowErr)
}

func TestErrorMatching(t *testing.T) {
	timeoutErr := NewWorkflowError(ErrorTypeTimeout, "timeout")
	taskErr := NewWorkflowError(ErrorTypeActivityFailed, "task failed")
	fatalErr := NewWorkflowError(ErrorTypeFatal, "fatal error")

	// Test exact matching
	assert.True(t, MatchesErrorType(timeoutErr, ErrorTypeTimeout))
	assert.False(t, MatchesErrorType(timeoutErr, ErrorTypeActivityFailed))

	// Test ErrorTypeAll matching
	assert.True(t, MatchesErrorType(timeoutErr, ErrorTypeAll))
	assert.True(t, MatchesErrorType(taskErr, ErrorTypeAll))
	assert.False(t, MatchesErrorType(fatalErr, ErrorTypeAll), "Fatal error should not match ErrorTypeAll")

	// Test ErrorTypeActivityFailed matching
	assert.True(t, MatchesErrorType(taskErr, ErrorTypeActivityFailed))
	assert.False(t, MatchesErrorType(timeoutErr, ErrorTypeActivityFailed))
}
