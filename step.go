package workflow

import "github.com/deepnoodle-ai/workflow/domain"

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
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Store specifies the variable name where this step's output will be stored.
	// The stored value can be accessed in subsequent steps using expressions:
	//   - $(state.X) or $(vars.X) or $(variables.X)
	// Example: Store: "user_data" allows access via $(state.user_data)
	Store string `json:"store,omitempty"`
	Activity             string                     `json:"activity,omitempty"`
	Parameters           map[string]any             `json:"parameters,omitempty"`
	Each                 *Each                      `json:"each,omitempty"`
	Join                 *JoinConfig                `json:"join,omitempty"`
	Next                 []*Edge                    `json:"next,omitempty"`
	EdgeMatchingStrategy domain.EdgeMatchingStrategy `json:"edge_matching_strategy,omitempty"`
	Retry                []*RetryConfig             `json:"retry,omitempty"`
	Catch                []*CatchConfig             `json:"catch,omitempty"`
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
	return s.Retry
}

// GetCatchConfigs returns the catch configurations for error handling (implements domain.StepWithEdges)
func (s *Step) GetCatchConfigs() []*domain.CatchConfig {
	return s.Catch
}

// Verify Step implements StepWithEdges
var _ domain.StepWithEdges = (*Step)(nil)

// JitterStrategy is an alias for domain.JitterStrategy.
// Defines how jitter is applied to retry delays.
type JitterStrategy = domain.JitterStrategy

// Jitter strategy constants - use lowercase values to match domain package.
const (
	JitterNone  JitterStrategy = domain.JitterNone  // "none" - no jitter
	JitterFull  JitterStrategy = domain.JitterFull  // "full" - random 0 to calculated delay
	JitterEqual JitterStrategy = domain.JitterEqual // "equal" - half fixed, half random
)

// RetryConfig is an alias for domain.RetryConfig.
// Configures retry behavior for a step.
type RetryConfig = domain.RetryConfig

// CatchConfig is an alias for domain.CatchConfig.
// Configures fallback behavior when errors occur.
type CatchConfig = domain.CatchConfig
