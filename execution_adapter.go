package workflow

import "context"

type ExecutionAdapter struct {
	execution *Execution
}

func (e *ExecutionAdapter) ExecuteActivity(ctx context.Context, stepName string, pathID string, activity Activity, params map[string]any, state *PathLocalState) (any, error) {
	return e.execution.executeActivity(ctx, stepName, pathID, activity, params, state)
}
