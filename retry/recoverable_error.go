package retry

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

type RecoverableError interface {
	error
	IsRecoverable() bool
}

// IsRecoverable checks if an error can be retried
func IsRecoverable(err error) bool {
	if err == nil {
		return false
	}

	// Check if error explicitly implements RecoverableError interface
	var recoverable RecoverableError
	if errors.As(err, &recoverable) {
		return recoverable.IsRecoverable()
	}

	// Default heuristics for common error types
	return isRecoverableByType(err)
}

// isRecoverableByType applies heuristics to determine if an error is recoverable
func isRecoverableByType(err error) bool {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return true // Timeout errors are usually recoverable
	case errors.Is(err, context.Canceled):
		return false // Cancellation is intentional, don't retry
	}

	// Check for network errors
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if netErr.Temporary() {
			return true
		}
		if netErr.Timeout() {
			return true
		}
	}

	// Check for URL errors (often network-related)
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return isRecoverableByType(urlErr.Err)
	}

	// Check error message for common recoverable patterns
	errStr := strings.ToLower(err.Error())
	recoverablePatterns := []string{
		"connection refused",
		"connection reset",
		"timeout",
		"temporary failure",
		"rate limit",
		"service unavailable",
		"internal server error",
		"bad gateway",
		"gateway timeout",
	}

	for _, pattern := range recoverablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

type recoverableError struct {
	err error
}

func (e *recoverableError) Error() string {
	return e.err.Error()
}

func (e *recoverableError) IsRecoverable() bool {
	return true
}

func (e *recoverableError) Unwrap() error {
	return e.err
}

func NewRecoverableError(err error) *recoverableError {
	return &recoverableError{err: err}
}

// NonRecoverableError represents an error that should not be retried
type NonRecoverableError struct {
	err error
}

func (e *NonRecoverableError) Error() string {
	return e.err.Error()
}

func (e *NonRecoverableError) IsRecoverable() bool {
	return false
}

func (e *NonRecoverableError) Unwrap() error {
	return e.err
}

func NewNonRecoverableError(err error) *NonRecoverableError {
	return &NonRecoverableError{err: err}
}
