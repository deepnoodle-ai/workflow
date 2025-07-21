package workflow

import (
	"context"

	"github.com/deepnoodle-ai/workflow/state"
)

type ExecutionAdapter struct {
	execution *Execution
}

func (e *ExecutionAdapter) ExecuteActivity(ctx context.Context, stepName, pathID string, activity Activity, params map[string]any, pathState *PathLocalState) (any, error) {
	return e.execution.executeActivity(ctx, stepName, pathID, activity, params, pathState)
}

// Note: These methods are no longer needed in the path-local state system,
// but kept for potential compatibility during transition

func (e *ExecutionAdapter) GetVariables() map[string]any {
	// No longer meaningful since variables are per-path, return empty map
	return map[string]any{}
}

func (e *ExecutionAdapter) GetInputs() map[string]any {
	return e.execution.state.GetInputs()
}

func (e *ExecutionAdapter) ApplyPatches(patches []state.Patch) {
	// No-op: patches are no longer used in path-local state system
}
