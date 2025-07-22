package workflow

import (
	"context"
	"encoding/json"
	"fmt"
)

// Activity represents an action that can be executed as part of a workflow.
type Activity interface {

	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters.
	Execute(ctx context.Context, params map[string]any) (any, error)
}

// WorkflowActivity is the enhanced activity interface that uses WorkflowContext
// for easier and more explicit access to path state within activity implementations.
type WorkflowActivity interface {
	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters using WorkflowContext for direct state access
	Execute(ctx WorkflowContext, params map[string]any) (any, error)
}

// TypedActivity is the new typed interface for activities
type TypedActivity[TParams, TResult any] interface {
	Name() string
	Execute(ctx context.Context, params TParams) (TResult, error)
}

// TypedWorkflowActivity is the enhanced typed interface that uses WorkflowContext
type TypedWorkflowActivity[TParams, TResult any] interface {
	Name() string
	Execute(ctx WorkflowContext, params TParams) (TResult, error)
}

// TypedActivityAdapter wraps a TypedActivity to implement the Activity interface
type TypedActivityAdapter[TParams, TResult any] struct {
	activity TypedActivity[TParams, TResult]
}

func (a *TypedActivityAdapter[TParams, TResult]) Name() string {
	return a.activity.Name()
}

func (a *TypedActivityAdapter[TParams, TResult]) Execute(ctx context.Context, params map[string]any) (any, error) {
	// Marshal params to JSON then unmarshal to typed struct
	var typedParams TParams

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal parameters for %s: %w", a.Name(), err)
	}

	// Unmarshal JSON bytes to typed struct
	if err := json.Unmarshal(jsonBytes, &typedParams); err != nil {
		return nil, fmt.Errorf("invalid parameters for %s: %w", a.Name(), err)
	}

	return a.activity.Execute(ctx, typedParams)
}

// NewTypedActivity creates a new typed activity that implements the Activity interface
func NewTypedActivity[TParams, TResult any](activity TypedActivity[TParams, TResult]) Activity {
	return &TypedActivityAdapter[TParams, TResult]{activity: activity}
}

// WorkflowActivityAdapter wraps a WorkflowActivity to implement the Activity interface
type WorkflowActivityAdapter struct {
	activity WorkflowActivity
}

func (a *WorkflowActivityAdapter) Name() string {
	return a.activity.Name()
}

func (a *WorkflowActivityAdapter) Execute(ctx context.Context, params map[string]any) (any, error) {
	// If the context is already a WorkflowContext, use it directly
	if wctx, ok := ctx.(WorkflowContext); ok {
		return a.activity.Execute(wctx, params)
	}
	
	// Otherwise, this is a fallback that shouldn't happen in normal execution
	// since the execution should always provide a WorkflowContext
	return nil, fmt.Errorf("WorkflowActivity requires WorkflowContext, got %T", ctx)
}

// NewWorkflowActivity creates a workflow activity that implements the Activity interface
func NewWorkflowActivity(activity WorkflowActivity) Activity {
	return &WorkflowActivityAdapter{activity: activity}
}

// TypedWorkflowActivityAdapter wraps a TypedWorkflowActivity to implement the Activity interface
type TypedWorkflowActivityAdapter[TParams, TResult any] struct {
	activity TypedWorkflowActivity[TParams, TResult]
}

func (a *TypedWorkflowActivityAdapter[TParams, TResult]) Name() string {
	return a.activity.Name()
}

func (a *TypedWorkflowActivityAdapter[TParams, TResult]) Execute(ctx context.Context, params map[string]any) (any, error) {
	// If the context is already a WorkflowContext, use it directly
	wctx, ok := ctx.(WorkflowContext)
	if !ok {
		return nil, fmt.Errorf("TypedWorkflowActivity requires WorkflowContext, got %T", ctx)
	}

	// Marshal params to JSON then unmarshal to typed struct
	var typedParams TParams

	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal parameters for %s: %w", a.Name(), err)
	}

	// Unmarshal JSON bytes to typed struct
	if err := json.Unmarshal(jsonBytes, &typedParams); err != nil {
		return nil, fmt.Errorf("invalid parameters for %s: %w", a.Name(), err)
	}

	return a.activity.Execute(wctx, typedParams)
}

// NewTypedWorkflowActivity creates a new typed workflow activity that implements the Activity interface
func NewTypedWorkflowActivity[TParams, TResult any](activity TypedWorkflowActivity[TParams, TResult]) Activity {
	return &TypedWorkflowActivityAdapter[TParams, TResult]{activity: activity}
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
