package workflow

import "context"

type ExecutionAdapter struct {
	execution *Execution
}

func (e *ExecutionAdapter) ExecuteActivity(ctx context.Context, stepName, pathID string, activity Activity, params map[string]any) (any, error) {
	return e.execution.executeActivity(ctx, stepName, pathID, activity, params)
}
