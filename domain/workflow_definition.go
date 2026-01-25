package domain

// WorkflowDefinition is the interface that workflow definitions must implement.
type WorkflowDefinition interface {
	Name() string
	// StepList returns workflow steps. Each element must implement StepDefinition.
	StepList() []any
}

// StepDefinition is the interface for workflow steps.
type StepDefinition interface {
	StepName() string
	ActivityName() string
	StepParameters() map[string]any
}

// WorkflowGraph extends WorkflowDefinition with graph traversal methods.
// Engine type-asserts to this interface when it needs to traverse the workflow graph.
type WorkflowGraph interface {
	WorkflowDefinition
	// GetStepDef returns a step by name as a StepDefinition.
	GetStepDef(name string) (StepDefinition, bool)
	// StartStep returns the first step in the workflow.
	StartStep() StepDefinition
	// InitialState returns initial workflow state variables.
	InitialState() map[string]any
}

// StepEdge represents an edge to a next step in the workflow graph.
type StepEdge struct {
	Step      string // target step name
	Condition string // optional condition expression
	Path      string // optional path name for branching
}

// EdgeMatchingStrategy defines how edges should be evaluated.
type EdgeMatchingStrategy string

const (
	// EdgeMatchingAll evaluates all edges and follows all matches (default behavior).
	EdgeMatchingAll EdgeMatchingStrategy = "all"

	// EdgeMatchingFirst evaluates edges in order and follows only the first matching one.
	EdgeMatchingFirst EdgeMatchingStrategy = "first"
)

// StepWithEdges extends StepDefinition with graph edge information.
// Engine type-asserts to this interface to traverse the workflow graph.
type StepWithEdges interface {
	StepDefinition
	// NextEdges returns the outgoing edges from this step.
	NextEdges() []*StepEdge
	// JoinConfig returns the join configuration if this is a join step.
	JoinConfig() *JoinConfig
	// StoreVariable returns the variable name to store step output.
	StoreVariable() string
	// GetEdgeMatchingStrategy returns how edges should be matched.
	GetEdgeMatchingStrategy() EdgeMatchingStrategy
}
