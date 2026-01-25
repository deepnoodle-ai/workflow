package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/workflow/runners"
)

// ExecuteActivityFunc is the signature for an Activity execution function.
type ExecuteActivityFunc func(ctx Context, parameters map[string]any) (any, error)

// Activity represents an action that can be executed as part of a workflow.
type Activity interface {

	// Name returns the name of the Activity
	Name() string

	// Execute the Activity with the given parameters.
	Execute(ctx Context, parameters map[string]any) (any, error)
}

// RunnableActivity is an optional interface for activities that have a custom
// execution strategy (container, HTTP, process). Activities implementing this
// interface will use their Runner for task specification and execution.
type RunnableActivity interface {
	Activity
	Runner() domain.Runner
}

// ContainerActivityOptions configures a container-based activity.
type ContainerActivityOptions struct {
	Image   string   // Docker image to run
	Command []string // Command to execute (optional)
	Timeout string   // Execution timeout (e.g., "5m")
}

// containerActivity wraps a ContainerRunner as an Activity.
type containerActivity struct {
	name   string
	runner *runners.ContainerRunner
}

func (a *containerActivity) Name() string { return a.name }

func (a *containerActivity) Execute(ctx Context, params map[string]any) (any, error) {
	// Execute the container locally using Docker
	result, err := a.runner.Execute(ctx, params)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, fmt.Errorf("%s", result.Error)
	}
	return result.Data, nil
}

func (a *containerActivity) Runner() domain.Runner { return a.runner }

// NewContainerActivity creates an activity that runs in a Docker container.
// This activity can execute locally (using Docker CLI) or via workers.
func NewContainerActivity(name string, opts ContainerActivityOptions) Activity {
	return &containerActivity{
		name: name,
		runner: &runners.ContainerRunner{
			Image:   opts.Image,
			Command: opts.Command,
			Timeout: opts.Timeout,
		},
	}
}

// HTTPActivityOptions configures an HTTP-based activity.
type HTTPActivityOptions struct {
	URL     string            // HTTP endpoint URL
	Method  string            // HTTP method (defaults to POST)
	Headers map[string]string // Additional headers
	Timeout string            // Request timeout (e.g., "30s")
}

// httpActivity wraps an HTTPRunner as an Activity.
type httpActivity struct {
	name   string
	runner *runners.HTTPRunner
}

func (a *httpActivity) Name() string { return a.name }

func (a *httpActivity) Execute(ctx Context, params map[string]any) (any, error) {
	// Execute the HTTP request locally
	result, err := a.runner.Execute(ctx, params)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, fmt.Errorf("%s", result.Error)
	}
	return result.Data, nil
}

func (a *httpActivity) Runner() domain.Runner { return a.runner }

// NewHTTPActivity creates an activity that calls an HTTP endpoint.
// This activity can execute locally (making real HTTP calls) or via workers.
func NewHTTPActivity(name string, opts HTTPActivityOptions) Activity {
	return &httpActivity{
		name: name,
		runner: &runners.HTTPRunner{
			URL:     opts.URL,
			Method:  opts.Method,
			Headers: opts.Headers,
			Timeout: opts.Timeout,
		},
	}
}

// ProcessActivityOptions configures a process-based activity.
type ProcessActivityOptions struct {
	Program string   // Program to execute
	Args    []string // Arguments
	Dir     string   // Working directory
	Timeout string   // Execution timeout
}

// processActivity wraps a ProcessRunner as an Activity.
type processActivity struct {
	name   string
	runner *runners.ProcessRunner
}

func (a *processActivity) Name() string { return a.name }

func (a *processActivity) Execute(ctx Context, params map[string]any) (any, error) {
	// Execute the process locally
	result, err := a.runner.Execute(ctx, params)
	if err != nil {
		return nil, err
	}
	if !result.Success {
		return nil, fmt.Errorf("%s", result.Error)
	}
	return result.Data, nil
}

func (a *processActivity) Runner() domain.Runner { return a.runner }

// NewProcessActivity creates an activity that runs as a local process.
// This activity can execute locally or via workers.
func NewProcessActivity(name string, opts ProcessActivityOptions) Activity {
	return &processActivity{
		name: name,
		runner: &runners.ProcessRunner{
			Program: opts.Program,
			Args:    opts.Args,
			Dir:     opts.Dir,
			Timeout: opts.Timeout,
		},
	}
}

// Verify interface compliance
var _ Activity = (*containerActivity)(nil)
var _ RunnableActivity = (*containerActivity)(nil)
var _ Activity = (*httpActivity)(nil)
var _ RunnableActivity = (*httpActivity)(nil)
var _ Activity = (*processActivity)(nil)
var _ RunnableActivity = (*processActivity)(nil)

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
	// Convert parameters to typed struct via JSON marshalling
	var typedParams TParams
	jsonBytes, err := json.Marshal(parameters)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters for activity %q: %w", a.Name(), err)
	}
	if err := json.Unmarshal(jsonBytes, &typedParams); err != nil {
		return nil, fmt.Errorf("invalid parameters for activity %q: %w", a.Name(), err)
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
