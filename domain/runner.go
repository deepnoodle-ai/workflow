package domain

import "context"

// Runner defines how an activity is executed by workers.
// It converts activity parameters to a TaskInput and interprets outputs.
type Runner interface {
	// ToSpec converts activity parameters to a TaskInput for workers.
	ToSpec(ctx context.Context, params map[string]any) (*TaskInput, error)

	// ParseResult interprets the worker's output as activity output.
	ParseResult(output *TaskOutput) (map[string]any, error)
}

// InlineExecutor is an optional interface for runners that can execute
// tasks directly in-process. The engine checks for this interface when
// processing inline tasks rather than type-asserting to a concrete type.
type InlineExecutor interface {
	// Execute runs the task in-process and returns the output.
	Execute(ctx context.Context, params map[string]any) (*TaskOutput, error)
}
