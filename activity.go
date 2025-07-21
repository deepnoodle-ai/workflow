package workflow

import (
	"context"
)

// Activity represents an action that can be executed as part of a workflow.
type Activity interface {

	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters.
	Execute(ctx context.Context, params map[string]any) (any, error)
}

// ActivityRegistry is a map of activity names to activities
type ActivityRegistry map[string]Activity

// ActivityFunction is a function that can be used as an activity
type ActivityFunction struct {
	name string
	fn   func(ctx context.Context, params map[string]any) (any, error)
}

// NewActivityFunction creates a new ActivityFunction
func NewActivityFunction(name string, fn func(ctx context.Context, params map[string]any) (any, error)) *ActivityFunction {
	return &ActivityFunction{name: name, fn: fn}
}

func (a *ActivityFunction) Name() string {
	return a.name
}

func (a *ActivityFunction) Execute(ctx context.Context, params map[string]any) (any, error) {
	return a.fn(ctx, params)
}
