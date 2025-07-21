package workflow

import (
	"context"
	"sync"

	"github.com/deepnoodle-ai/workflow/state"
)

type ExecutionAdapter struct {
	execution *Execution
	patches   []state.Patch
	mutex     sync.RWMutex
}

func (e *ExecutionAdapter) ExecuteActivity(ctx context.Context, stepName, pathID string, activity Activity, params map[string]any) (any, error) {
	return e.execution.executeActivity(ctx, stepName, pathID, activity, params)
}

func (e *ExecutionAdapter) GetVariables() map[string]any {
	return e.execution.state.GetVariables()
}

func (e *ExecutionAdapter) GetInputs() map[string]any {
	return e.execution.state.GetInputs()
}

func (e *ExecutionAdapter) ApplyPatches(patches []state.Patch) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	e.patches = append(e.patches, patches...)
}
