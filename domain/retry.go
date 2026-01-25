package domain

import "time"

// JitterStrategy defines how jitter is applied to retry delays.
type JitterStrategy string

const (
	// JitterNone applies no jitter.
	JitterNone JitterStrategy = "none"
	// JitterFull applies full jitter (0 to calculated delay).
	JitterFull JitterStrategy = "full"
	// JitterEqual applies equal jitter (half fixed, half random).
	JitterEqual JitterStrategy = "equal"
)

// RetryConfig configures retry behavior for a step.
type RetryConfig struct {
	// ErrorEquals specifies which error types this config applies to.
	// If empty, matches all errors.
	ErrorEquals []string `json:"error_equals,omitempty"`

	// MaxRetries is the maximum number of retry attempts (not counting the initial attempt).
	MaxRetries int `json:"max_retries,omitempty"`

	// BaseDelay is the initial delay before the first retry.
	BaseDelay time.Duration `json:"base_delay,omitempty"`

	// MaxDelay is the maximum delay between retries.
	MaxDelay time.Duration `json:"max_delay,omitempty"`

	// BackoffRate is the multiplier for exponential backoff (default 2.0).
	BackoffRate float64 `json:"backoff_rate,omitempty"`

	// JitterStrategy defines how jitter is applied.
	JitterStrategy JitterStrategy `json:"jitter_strategy,omitempty"`

	// Timeout is the maximum duration for a single attempt.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// CalculateBackoffDelay calculates the delay before the next retry attempt.
func CalculateBackoffDelay(attempt int, config *RetryConfig) time.Duration {
	baseDelay := config.BaseDelay
	if baseDelay <= 0 {
		baseDelay = 1 * time.Second // Default base delay
	}

	backoffRate := config.BackoffRate
	if backoffRate <= 0 {
		backoffRate = 2.0 // Default backoff rate
	}

	// Calculate exponential backoff: baseDelay * (backoffRate ^ attempt)
	delay := float64(baseDelay)
	for i := 0; i < attempt-1; i++ {
		delay *= backoffRate
	}

	// Apply maximum delay cap
	if config.MaxDelay > 0 && time.Duration(delay) > config.MaxDelay {
		delay = float64(config.MaxDelay)
	}

	// Note: Jitter is not applied here for task-based retries since
	// the VisibleAt time is set when releasing the task.
	// Jitter can be added by callers if needed.

	return time.Duration(delay)
}
