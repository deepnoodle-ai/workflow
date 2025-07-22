package workflow

import (
	"context"
	"fmt"

	"github.com/mitchellh/mapstructure"
)

// Activity represents an action that can be executed as part of a workflow.
type Activity interface {

	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters.
	Execute(ctx context.Context, params map[string]any) (any, error)
}

// TypedActivity is the new typed interface for activities
type TypedActivity[TParams, TResult any] interface {
	Name() string
	Execute(ctx context.Context, params TParams) (TResult, error)
}

// TypedActivityAdapter wraps a TypedActivity to implement the Activity interface
type TypedActivityAdapter[TParams, TResult any] struct {
	activity TypedActivity[TParams, TResult]
}

func (a *TypedActivityAdapter[TParams, TResult]) Name() string {
	return a.activity.Name()
}

func (a *TypedActivityAdapter[TParams, TResult]) Execute(ctx context.Context, params map[string]any) (any, error) {
	// Marshal params to typed struct
	var typedParams TParams
	if err := mapstructure.Decode(params, &typedParams); err != nil {
		return nil, fmt.Errorf("invalid parameters for %s: %w", a.Name(), err)
	}

	return a.activity.Execute(ctx, typedParams)
}

// NewTypedActivity creates a new typed activity that implements the Activity interface
func NewTypedActivity[TParams, TResult any](activity TypedActivity[TParams, TResult]) Activity {
	return &TypedActivityAdapter[TParams, TResult]{activity: activity}
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

// TypedActivityFunction creates a typed activity from a function
func TypedActivityFunction[TParams, TResult any](name string, fn func(ctx context.Context, params TParams) (TResult, error)) Activity {
	return NewTypedActivity(&typedActivityFunction[TParams, TResult]{
		name: name,
		fn:   fn,
	})
}

// typedActivityFunction is a helper struct for creating typed activities from functions
type typedActivityFunction[TParams, TResult any] struct {
	name string
	fn   func(ctx context.Context, params TParams) (TResult, error)
}

func (t *typedActivityFunction[TParams, TResult]) Name() string {
	return t.name
}

func (t *typedActivityFunction[TParams, TResult]) Execute(ctx context.Context, params TParams) (TResult, error) {
	return t.fn(ctx, params)
}
