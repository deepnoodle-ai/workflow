package workflow

import "context"

type ExecutionAdapter struct {
	execution *Execution
}

func (e *ExecutionAdapter) ExecuteActivity(ctx context.Context, stepName string, branchID string, activity Activity, params map[string]any, state *BranchLocalState) (any, error) {
	return e.execution.executeActivity(ctx, stepName, branchID, activity, params, state)
}
