package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
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
	runner *containerRunner
}

// containerRunner is a local copy to avoid import cycle with runners package.
type containerRunner struct {
	Image   string
	Command []string
	Timeout string
}

func (r *containerRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
	env := make(map[string]string)
	for k, v := range params {
		switch val := v.(type) {
		case string:
			env[k] = val
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("marshal param %s: %w", k, err)
			}
			env[k] = string(data)
		}
	}

	return &domain.TaskSpec{
		Type:    "container",
		Image:   r.Image,
		Command: r.Command,
		Env:     env,
		Input:   params,
	}, nil
}

func (r *containerRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
	if !result.Success {
		return nil, fmt.Errorf("container failed: %s", result.Error)
	}
	if result.Data != nil {
		return result.Data, nil
	}
	if result.Output != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(result.Output), &data); err == nil {
			return data, nil
		}
	}
	return map[string]any{"output": result.Output}, nil
}

// Execute implements domain.InlineExecutor for local Docker execution.
func (r *containerRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskResult, error) {
	// Build docker run command
	args := []string{"run", "--rm"}

	// Add environment variables from params
	for k, v := range params {
		var envVal string
		switch val := v.(type) {
		case string:
			envVal = val
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return &domain.TaskResult{
					Success: false,
					Error:   fmt.Sprintf("marshal param %s: %v", k, err),
				}, nil
			}
			envVal = string(data)
		}
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, envVal))
	}

	// Add image
	args = append(args, r.Image)

	// Add command if specified
	args = append(args, r.Command...)

	// Run docker
	cmd := exec.CommandContext(ctx, "docker", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return &domain.TaskResult{
			Success:  false,
			Error:    fmt.Sprintf("container failed (exit %d): %s", exitCode, stderr.String()),
			ExitCode: exitCode,
			Output:   stdout.String(),
		}, nil
	}

	output := stdout.String()

	// Try to parse as JSON
	var data map[string]any
	if err := json.Unmarshal([]byte(output), &data); err == nil {
		return &domain.TaskResult{
			Success: true,
			Data:    data,
		}, nil
	}

	// Return raw output if not JSON
	return &domain.TaskResult{
		Success: true,
		Data:    map[string]any{"output": output},
	}, nil
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
		runner: &containerRunner{
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
	runner *httpRunner
}

// httpRunner is a local copy to avoid import cycle with runners package.
type httpRunner struct {
	URL     string
	Method  string
	Headers map[string]string
	Timeout string
}

func (r *httpRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	method := r.Method
	if method == "" {
		method = "POST"
	}

	headers := make(map[string]string)
	for k, v := range r.Headers {
		headers[k] = v
	}
	if _, ok := headers["Content-Type"]; !ok {
		headers["Content-Type"] = "application/json"
	}

	return &domain.TaskSpec{
		Type:    "http",
		URL:     r.URL,
		Method:  method,
		Headers: headers,
		Body:    string(body),
		Input:   params,
	}, nil
}

func (r *httpRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
	if !result.Success {
		return nil, fmt.Errorf("http request failed: %s", result.Error)
	}
	if result.Data != nil {
		return result.Data, nil
	}
	if result.Output != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(result.Output), &data); err == nil {
			return data, nil
		}
	}
	return map[string]any{"output": result.Output}, nil
}

// Execute implements domain.InlineExecutor for local HTTP execution.
func (r *httpRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskResult, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return &domain.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("marshal params: %v", err),
		}, nil
	}

	method := r.Method
	if method == "" {
		method = "POST"
	}

	req, err := http.NewRequestWithContext(ctx, method, r.URL, bytes.NewReader(body))
	if err != nil {
		return &domain.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("create request: %v", err),
		}, nil
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}

	// Create client with timeout
	client := &http.Client{}
	if r.Timeout != "" {
		if d, err := time.ParseDuration(r.Timeout); err == nil {
			client.Timeout = d
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return &domain.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("http request failed: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &domain.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("read response: %v", err),
		}, nil
	}

	if resp.StatusCode >= 400 {
		return &domain.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("http status %d: %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	// Try to parse as JSON
	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err == nil {
		return &domain.TaskResult{
			Success: true,
			Data:    data,
		}, nil
	}

	// Return raw output if not JSON
	return &domain.TaskResult{
		Success: true,
		Data:    map[string]any{"output": string(respBody)},
	}, nil
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
		runner: &httpRunner{
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
	runner *processRunner
}

// processRunner is a local copy to avoid import cycle with runners package.
type processRunner struct {
	Program string
	Args    []string
	Dir     string
	Timeout string
}

func (r *processRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
	env := make(map[string]string)
	for k, v := range params {
		switch val := v.(type) {
		case string:
			env[k] = val
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("marshal param %s: %w", k, err)
			}
			env[k] = string(data)
		}
	}

	return &domain.TaskSpec{
		Type:    "process",
		Program: r.Program,
		Args:    r.Args,
		Dir:     r.Dir,
		Env:     env,
		Input:   params,
	}, nil
}

func (r *processRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
	if !result.Success {
		return nil, fmt.Errorf("process failed: %s", result.Error)
	}
	if result.Data != nil {
		return result.Data, nil
	}
	if result.Output != "" {
		var data map[string]any
		if err := json.Unmarshal([]byte(result.Output), &data); err == nil {
			return data, nil
		}
	}
	return map[string]any{"output": result.Output}, nil
}

// Execute implements domain.InlineExecutor for local process execution.
func (r *processRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskResult, error) {
	cmd := exec.CommandContext(ctx, r.Program, r.Args...)

	if r.Dir != "" {
		cmd.Dir = r.Dir
	}

	// Set environment variables from params
	cmd.Env = os.Environ()
	for k, v := range params {
		switch val := v.(type) {
		case string:
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, val))
		default:
			data, err := json.Marshal(v)
			if err != nil {
				return &domain.TaskResult{
					Success: false,
					Error:   fmt.Sprintf("marshal param %s: %v", k, err),
				}, nil
			}
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, string(data)))
		}
	}

	// Capture output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		return &domain.TaskResult{
			Success:  false,
			Error:    fmt.Sprintf("process failed (exit %d): %s", exitCode, stderr.String()),
			ExitCode: exitCode,
			Output:   stdout.String(),
		}, nil
	}

	output := stdout.String()

	// Try to parse as JSON
	var data map[string]any
	if err := json.Unmarshal([]byte(output), &data); err == nil {
		return &domain.TaskResult{
			Success: true,
			Data:    data,
		}, nil
	}

	// Return raw output if not JSON
	return &domain.TaskResult{
		Success: true,
		Data:    map[string]any{"output": output},
	}, nil
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
		runner: &processRunner{
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
var _ domain.InlineExecutor = (*containerRunner)(nil)
var _ Activity = (*httpActivity)(nil)
var _ RunnableActivity = (*httpActivity)(nil)
var _ domain.InlineExecutor = (*httpRunner)(nil)
var _ Activity = (*processActivity)(nil)
var _ RunnableActivity = (*processActivity)(nil)
var _ domain.InlineExecutor = (*processRunner)(nil)

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
