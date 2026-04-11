package workflow

import "context"

type executionAdapter struct {
	execution *Execution
}

func (e *executionAdapter) ExecuteActivity(ctx context.Context, stepName string, branchID string, activity Activity, params map[string]any, state *BranchLocalState) (any, error) {
	return e.execution.executeActivity(ctx, stepName, branchID, activity, params, state)
}
