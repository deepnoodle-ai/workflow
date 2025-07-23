package workflow

import (
	"encoding/json"
	"fmt"
)

// ActivityRegistry contains activities indexed by name.
type ActivityRegistry map[string]Activity

// ExecuteActivityFunc is the signature for an Activity execution function.
type ExecuteActivityFunc func(ctx Context, parameters map[string]any) (any, error)

// Activity represents an action that can be executed as part of a workflow.
type Activity interface {

	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters.
	Execute(ctx Context, parameters map[string]any) (any, error)
}

// TypedActivity is a parameterized interface for activities that assists with
// marshalling parameters and results.
type TypedActivity[TParams, TResult any] interface {

	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters.
	Execute(ctx Context, parameters TParams) (TResult, error)
}

// TypedActivityAdapter wraps a TypedActivity to implement the Activity interface.
type TypedActivityAdapter[TParams, TResult any] struct {
	activity TypedActivity[TParams, TResult]
}

// Name of the Activity.
func (a *TypedActivityAdapter[TParams, TResult]) Name() string {
	return a.activity.Name()
}

// Execute the Activity.
func (a *TypedActivityAdapter[TParams, TResult]) Execute(ctx Context, parameters map[string]any) (any, error) {
	// Marshal params to JSON then unmarshal to typed struct
	var typedParams TParams

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(parameters)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal parameters for %s: %w", a.Name(), err)
	}

	// Unmarshal JSON bytes to typed struct
	if err := json.Unmarshal(jsonBytes, &typedParams); err != nil {
		return nil, fmt.Errorf("invalid parameters for %s: %w", a.Name(), err)
	}

	return a.activity.Execute(ctx, typedParams)
}

// Activity returns the underlying TypedActivity
func (a *TypedActivityAdapter[TParams, TResult]) Activity() TypedActivity[TParams, TResult] {
	return a.activity
}

// NewTypedActivity creates a new typed activity that implements the Activity interface
func NewTypedActivity[TParams, TResult any](activity TypedActivity[TParams, TResult]) Activity {
	return &TypedActivityAdapter[TParams, TResult]{activity: activity}
}
