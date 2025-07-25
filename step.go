package workflow

import (
	"time"
)

// EdgeMatchingStrategy defines how edges should be evaluated
type EdgeMatchingStrategy string

const (
	// EdgeMatchingAll evaluates all edges and follows all matches (default behavior)
	EdgeMatchingAll EdgeMatchingStrategy = "all"

	// EdgeMatchingFirst evaluates edges in order and follows only the first matching one
	EdgeMatchingFirst EdgeMatchingStrategy = "first"
)

// Edge is used to configure a next step in a workflow.
type Edge struct {
	Step      string `json:"step"`
	Condition string `json:"condition,omitempty"`
	Path      string `json:"path,omitempty"`
}

// Each is used to configure a step to loop over a list of items.
type Each struct {
	Items any    `json:"items"`
	As    string `json:"as,omitempty"`
}

// JoinConfig configures a step to wait for multiple paths to converge
type JoinConfig struct {
	// Paths specifies which named paths to wait for. If empty, waits for all active paths.
	Paths []string `json:"paths,omitempty"`

	// Count specifies the number of paths to wait for. If 0, waits for all specified paths.
	Count int `json:"count,omitempty"`

	// PathMappings specifies where to store path data. Supports two syntaxes:
	// 1. Store entire path state: "pathID": "destination"
	//    Example: "pathA": "results.pathA" stores all pathA variables under results.pathA
	// 2. Extract specific variables: "pathID.variable": "destination"
	//    Example: "pathA.result": "extracted.value" stores only pathA.result under extracted.value
	// Supports nested field extraction using dot notation for both variable names and destinations.
	PathMappings map[string]string `json:"path_mappings,omitempty"`
}

// Step represents a single step in a workflow.
type Step struct {
	Name                 string               `json:"name"`
	Description          string               `json:"description,omitempty"`
	Store                string               `json:"store,omitempty"`
	Activity             string               `json:"activity,omitempty"`
	Parameters           map[string]any       `json:"parameters,omitempty"`
	Each                 *Each                `json:"each,omitempty"`
	Join                 *JoinConfig          `json:"join,omitempty"`
	Next                 []*Edge              `json:"next,omitempty"`
	EdgeMatchingStrategy EdgeMatchingStrategy `json:"edge_matching_strategy,omitempty"`
	Retry                []*RetryConfig       `json:"retry,omitempty"`
	Catch                []*CatchConfig       `json:"catch,omitempty"`
}

// GetEdgeMatchingStrategy returns the edge matching strategy for this step,
// defaulting to "all" if not specified
func (s *Step) GetEdgeMatchingStrategy() EdgeMatchingStrategy {
	if s.EdgeMatchingStrategy == "" {
		return EdgeMatchingAll
	}
	return s.EdgeMatchingStrategy
}

// JitterStrategy defines the jitter strategy for retry delays
type JitterStrategy string

const (
	JitterNone JitterStrategy = "NONE"
	JitterFull JitterStrategy = "FULL"
)

// RetryConfig configures retry behavior for a step.
type RetryConfig struct {
	ErrorEquals    []string       `json:"error_equals,omitempty"`
	MaxRetries     int            `json:"max_retries,omitempty"`
	BaseDelay      time.Duration  `json:"base_delay,omitempty"`
	MaxDelay       time.Duration  `json:"max_delay,omitempty"`
	BackoffRate    float64        `json:"backoff_rate,omitempty"`
	JitterStrategy JitterStrategy `json:"jitter_strategy,omitempty"`
	Timeout        time.Duration  `json:"timeout,omitempty"`
}

// CatchConfig configures fallback behavior when errors occur
type CatchConfig struct {
	ErrorEquals []string `json:"error_equals"`
	Next        string   `json:"next"`
	Store       string   `json:"store,omitempty"`
}
