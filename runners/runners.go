package runners

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow/domain"
)

// Runner defines how an activity is executed by workers.
type Runner = domain.Runner

// ContainerRunner executes activities as Docker containers.
type ContainerRunner struct {
	Image   string
	Command []string
	Timeout string // e.g., "5m"
}

func (r *ContainerRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
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

func (r *ContainerRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
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

// ProcessRunner executes activities as local processes.
type ProcessRunner struct {
	Program string
	Args    []string
	Dir     string
	Timeout string
}

func (r *ProcessRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
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

func (r *ProcessRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
	if !result.Success {
		return nil, fmt.Errorf("process failed (exit %d): %s", result.ExitCode, result.Error)
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

// HTTPRunner executes activities by calling HTTP endpoints.
type HTTPRunner struct {
	URL     string
	Method  string // defaults to POST
	Headers map[string]string
	Timeout string
}

func (r *HTTPRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
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

func (r *HTTPRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
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

// InlineRunner executes activities in-process using a function.
// Useful for testing and simple activities that don't need isolation.
type InlineRunner struct {
	Func func(ctx context.Context, params map[string]any) (map[string]any, error)
}

func (r *InlineRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskSpec, error) {
	return &domain.TaskSpec{
		Type:  "inline",
		Input: params,
	}, nil
}

func (r *InlineRunner) ParseResult(result *domain.TaskResult) (map[string]any, error) {
	if !result.Success {
		return nil, fmt.Errorf("inline execution failed: %s", result.Error)
	}
	return result.Data, nil
}

// Execute runs the inline function directly (used by in-process engine).
func (r *InlineRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskResult, error) {
	output, err := r.Func(ctx, params)
	if err != nil {
		return &domain.TaskResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}
	return &domain.TaskResult{
		Success: true,
		Data:    output,
	}, nil
}

// Verify interface compliance.
var _ domain.Runner = (*ContainerRunner)(nil)
var _ domain.Runner = (*ProcessRunner)(nil)
var _ domain.Runner = (*HTTPRunner)(nil)
var _ domain.Runner = (*InlineRunner)(nil)
var _ domain.InlineExecutor = (*InlineRunner)(nil)
