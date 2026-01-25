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
