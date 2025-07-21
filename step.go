package workflow

import (
	"time"
)

// Edge is used to configure a next step in a workflow.
type Edge struct {
	Step      string `json:"step"`
	Condition string `json:"condition,omitempty"`
}

// Each is used to configure a step to loop over a list of items.
type Each struct {
	Items any    `json:"items"`
	As    string `json:"as,omitempty"`
}

// Step represents a single step in a workflow.
type Step struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Store       string         `json:"store,omitempty"`
	Activity    string         `json:"activity,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Each        *Each          `json:"each,omitempty"`
	Next        []*Edge        `json:"next,omitempty"`
	End         bool           `json:"end,omitempty"`
	Retry       *RetryConfig   `json:"retry,omitempty"`
}

// RetryConfig configures retry behavior for a step.
type RetryConfig struct {
	MaxRetries int           `json:"max_retries,omitempty"`
	BaseDelay  time.Duration `json:"base_delay,omitempty"`
	MaxDelay   time.Duration `json:"max_delay,omitempty"`
	Timeout    time.Duration `json:"timeout,omitempty"`
}
