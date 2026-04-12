package workflow

import "reflect"

// Confirm the interfaces are implemented correctly.
var (
	_ Activity                = (*activityFunc)(nil)
	_ TypedActivity[any, any] = (*typedActivityFunc[any, any])(nil)
)

// activityFunc wraps a function for use as an Activity.
type activityFunc struct {
	name string
	fn   ExecuteActivityFunc
}

// ActivityFunc returns an Activity backed by fn. The returned value
// implements the Activity interface, mirroring http.HandlerFunc.
func ActivityFunc(name string, fn ExecuteActivityFunc) Activity {
	return &activityFunc{name: name, fn: fn}
}

// Name of the Activity.
func (a *activityFunc) Name() string {
	return a.name
}

// Execute the Activity.
func (a *activityFunc) Execute(ctx Context, parameters map[string]any) (any, error) {
	return a.fn(ctx, parameters)
}

// TypedActivityFunc returns an Activity backed by a strongly-typed
// function. Parameters are JSON-marshalled into TParams by the adapter
// layer.
func TypedActivityFunc[TParams, TResult any](name string, fn func(ctx Context, params TParams) (TResult, error)) Activity {
	return NewTypedActivity(&typedActivityFunc[TParams, TResult]{
		name: name,
		fn:   fn,
	})
}

// typedActivityFunc is the internal struct backing TypedActivityFunc.
type typedActivityFunc[TParams, TResult any] struct {
	name string
	fn   func(ctx Context, params TParams) (TResult, error)
}

// Name of the Activity.
func (t *typedActivityFunc[TParams, TResult]) Name() string {
	return t.name
}

// Execute the Activity.
func (t *typedActivityFunc[TParams, TResult]) Execute(ctx Context, params TParams) (TResult, error) {
	return t.fn(ctx, params)
}

// ParametersType returns the type of the parameters for the Activity.
func (t *typedActivityFunc[TParams, TResult]) ParametersType() reflect.Type {
	return reflect.TypeOf((*TParams)(nil)).Elem()
}

// ResultType returns the type of the result for the Activity.
func (t *typedActivityFunc[TParams, TResult]) ResultType() reflect.Type {
	return reflect.TypeOf((*TResult)(nil)).Elem()
}
