package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Error type constants for classification and matching
const (
	// ErrorTypeAll acts as a wildcard that matches any error except fatal errors
	ErrorTypeAll = "all"

	// ErrorTypeActivityFailed matches any error except timeouts and fatal errors
	ErrorTypeActivityFailed = "activity_failed"

	// ErrorTypeTimeout matches a timeout context canceled error
	ErrorTypeTimeout = "timeout"

	// ErrorTypeFatal indicates an execution failed due to a fatal error.
	// The approach we're taking is that by default, unknown errors are
	// classified as activity failed errors. This is because we want to
	// allow retries on unknown errors by default. If we know a specific
	// error should NOT be retried, it should have type=ErrorTypeFatal set.
	ErrorTypeFatal = "fatal_error"
)

// WorkflowError represents a structured error with classification
// It supports Go's error wrapping patterns with Unwrap() method
type WorkflowError struct {
	Type    string      `json:"type"`
	Cause   string      `json:"cause"`
	Details interface{} `json:"details,omitempty"`
	Wrapped error       `json:"-"` // Original error being wrapped
}

// Error implements the error interface
func (e *WorkflowError) Error() string {
	return fmt.Sprintf("%s: %s", e.Type, e.Cause)
}

// Unwrap implements the error unwrapping interface for Go's errors.Is and errors.As
func (e *WorkflowError) Unwrap() error {
	return e.Wrapped
}

// ErrorOutput represents the structured error information passed to catch handlers
type ErrorOutput struct {
	Error   string      `json:"Error"`
	Cause   string      `json:"Cause"`
	Details interface{} `json:"Details,omitempty"`
}

// NewWorkflowError creates a new WorkflowError with the specified type and cause.
// The type can be any user-defined string e.g. "network-error". The important
// thing is that it may be used to match against the type used in a retry config.
func NewWorkflowError(errorType, cause string) *WorkflowError {
	return &WorkflowError{
		Type:  errorType,
		Cause: cause,
	}
}

// ClassifyError attempts to classify a regular error into a WorkflowError
func ClassifyError(err error) *WorkflowError {
	// If the error is already a WorkflowError, return it
	var workflowError *WorkflowError
	if errors.As(err, &workflowError) {
		return workflowError
	}
	// Check for timeout patterns
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return &WorkflowError{
			Type:    ErrorTypeTimeout,
			Cause:   err.Error(),
			Wrapped: err,
		}
	}
	// Default to an activity failed error
	return &WorkflowError{
		Type:    ErrorTypeActivityFailed,
		Cause:   err.Error(),
		Wrapped: err,
	}
}

// MatchesErrorType checks if an error matches a specified error type pattern
func MatchesErrorType(err error, errorType string) bool {
	wErr := ClassifyError(err)
	// Fatal errors are only matched by the ErrorTypeFatal pattern
	if wErr.Type == ErrorTypeFatal {
		return errorType == ErrorTypeFatal
	}
	// Otherwise...
	switch errorType {
	case ErrorTypeAll:
		return true
	case ErrorTypeActivityFailed:
		return wErr.Type != ErrorTypeTimeout
	default:
		// Note the intent here is to handle arbitrary error type strings, not
		// just a fixed set of types.
		return wErr.Type == errorType
	}
}

// ToErrorOutput converts a WorkflowError to ErrorOutput for catch handlers
func (e *WorkflowError) ToErrorOutput() ErrorOutput {
	return ErrorOutput{
		Error:   e.Type,
		Cause:   e.Cause,
		Details: e.Details,
	}
}
