package workflow

// Confirm the interfaces are implemented correctly.
var (
	_ Activity                = (*ActivityFunction)(nil)
	_ TypedActivity[any, any] = (*typedActivityFunction[any, any])(nil)
)

// ActivityFunction wraps a function for use as an Activity. It implements the
// workflow.Activity interface.
type ActivityFunction struct {
	name string
	fn   ExecuteActivityFunc
}

// NewActivityFunction returns an Activity for the given function.
func NewActivityFunction(name string, fn ExecuteActivityFunc) Activity {
	return &ActivityFunction{name: name, fn: fn}
}

// Name of the Activity.
func (a *ActivityFunction) Name() string {
	return a.name
}

// Execute the Activity.
func (a *ActivityFunction) Execute(ctx Context, parameters map[string]any) (any, error) {
	return a.fn(ctx, parameters)
}

// TypedActivityFunction wraps a function for use as a TypedActivity. It
// implements the workflow.TypedActivity interface.
func TypedActivityFunction[TParams, TResult any](name string, fn func(ctx Context, params TParams) (TResult, error)) Activity {
	return NewTypedActivity(&typedActivityFunction[TParams, TResult]{
		name: name,
		fn:   fn,
	})
}

// typedActivityFunction is a helper struct for creating typed activities from functions
type typedActivityFunction[TParams, TResult any] struct {
	name string
	fn   func(ctx Context, params TParams) (TResult, error)
}

// Name of the Activity.
func (t *typedActivityFunction[TParams, TResult]) Name() string {
	return t.name
}

// Execute the Activity.
func (t *typedActivityFunction[TParams, TResult]) Execute(ctx Context, params TParams) (TResult, error) {
	return t.fn(ctx, params)
}
