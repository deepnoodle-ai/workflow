package workflowtest

import "github.com/deepnoodle-ai/workflow"

// MockActivity creates a stub activity that always returns the given result.
func MockActivity(name string, result any) workflow.Activity {
	return workflow.NewActivityFunction(name, func(ctx workflow.Context, params map[string]any) (any, error) {
		return result, nil
	})
}

// MockActivityError creates a stub activity that always returns the given error.
func MockActivityError(name string, err error) workflow.Activity {
	return workflow.NewActivityFunction(name, func(ctx workflow.Context, params map[string]any) (any, error) {
		return nil, err
	})
}
