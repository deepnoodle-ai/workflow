package domain

import "context"

// Runner defines how an activity is executed by workers.
// It converts activity parameters to a TaskSpec and interprets results.
type Runner interface {
	// ToSpec converts activity parameters to a TaskSpec for workers.
	ToSpec(ctx context.Context, params map[string]any) (*TaskSpec, error)

	// ParseResult interprets the worker's result as activity output.
	ParseResult(result *TaskResult) (map[string]any, error)
}

// InlineExecutor is an optional interface for runners that can execute
// tasks directly in-process. The engine checks for this interface when
// processing inline tasks rather than type-asserting to a concrete type.
type InlineExecutor interface {
	// Execute runs the task in-process and returns the result.
	Execute(ctx context.Context, params map[string]any) (*TaskResult, error)
}
