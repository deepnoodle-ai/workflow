package domain

// ExecutionContextKey is the context key for execution context values.
// This is used to pass execution metadata through context.Context
// for inline activity execution.
type ExecutionContextKey struct{}

// ExecutionInfo contains execution context passed through context.Context.
// This allows inline executors to access execution metadata like
// execution ID, path ID, step name, and workflow state.
type ExecutionInfo struct {
	ExecutionID string
	PathID      string
	StepName    string
	Inputs      map[string]any
	Variables   map[string]any
}
