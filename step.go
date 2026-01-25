package workflow

import (
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// EdgeMatchingStrategy is an alias for backward compatibility.
type EdgeMatchingStrategy = domain.EdgeMatchingStrategy

// Edge matching constants for backward compatibility.
const (
	EdgeMatchingAll   = domain.EdgeMatchingAll
	EdgeMatchingFirst = domain.EdgeMatchingFirst
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

// JoinConfig configures a step to wait for multiple paths to converge.
type JoinConfig = domain.JoinConfig

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
// defaulting to "all" if not specified (implements domain.StepWithEdges)
func (s *Step) GetEdgeMatchingStrategy() domain.EdgeMatchingStrategy {
	if s.EdgeMatchingStrategy == "" {
		return domain.EdgeMatchingAll
	}
	return domain.EdgeMatchingStrategy(s.EdgeMatchingStrategy)
}

// StepName returns the step name (implements domain.StepDefinition)
func (s *Step) StepName() string {
	return s.Name
}

// ActivityName returns the activity name (implements domain.StepDefinition)
func (s *Step) ActivityName() string {
	return s.Activity
}

// StepParameters returns the step parameters (implements domain.StepDefinition)
func (s *Step) StepParameters() map[string]any {
	return s.Parameters
}

// NextEdges returns the outgoing edges from this step (implements domain.StepWithEdges)
func (s *Step) NextEdges() []*domain.StepEdge {
	edges := make([]*domain.StepEdge, len(s.Next))
	for i, e := range s.Next {
		edges[i] = &domain.StepEdge{
			Step:      e.Step,
			Condition: e.Condition,
			Path:      e.Path,
		}
	}
	return edges
}

// JoinConfig returns the join configuration (implements domain.StepWithEdges)
func (s *Step) JoinConfig() *domain.JoinConfig {
	return s.Join
}

// StoreVariable returns the variable name to store step output (implements domain.StepWithEdges)
func (s *Step) StoreVariable() string {
	return s.Store
}

// GetRetryConfigs returns the retry configurations for this step (implements domain.StepWithEdges)
func (s *Step) GetRetryConfigs() []*domain.RetryConfig {
	if len(s.Retry) == 0 {
		return nil
	}
	configs := make([]*domain.RetryConfig, len(s.Retry))
	for i, r := range s.Retry {
		configs[i] = &domain.RetryConfig{
			ErrorEquals:    r.ErrorEquals,
			MaxRetries:     r.MaxRetries,
			BaseDelay:      r.BaseDelay,
			MaxDelay:       r.MaxDelay,
			BackoffRate:    r.BackoffRate,
			JitterStrategy: domain.JitterStrategy(r.JitterStrategy),
			Timeout:        r.Timeout,
		}
	}
	return configs
}

// GetCatchConfigs returns the catch configurations for error handling (implements domain.StepWithEdges)
func (s *Step) GetCatchConfigs() []*domain.CatchConfig {
	if len(s.Catch) == 0 {
		return nil
	}
	configs := make([]*domain.CatchConfig, len(s.Catch))
	for i, c := range s.Catch {
		configs[i] = &domain.CatchConfig{
			ErrorEquals: c.ErrorEquals,
			Next:        c.Next,
			Store:       c.Store,
		}
	}
	return configs
}

// Verify Step implements StepWithEdges
var _ domain.StepWithEdges = (*Step)(nil)

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
