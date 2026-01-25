package runners

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

// Runner defines how an activity is executed by workers.
type Runner = domain.Runner

// ContainerRunner executes activities as Docker containers.
type ContainerRunner struct {
	Image   string
	Command []string
	Timeout string // e.g., "5m"
}

func (r *ContainerRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskInput, error) {
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

	return &domain.TaskInput{
		Type:    "container",
		Image:   r.Image,
		Command: r.Command,
		Env:     env,
		Input:   params,
	}, nil
}

func (r *ContainerRunner) ParseResult(result *domain.TaskOutput) (map[string]any, error) {
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
func (r *ContainerRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskOutput, error) {
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
				return &domain.TaskOutput{
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
		return &domain.TaskOutput{
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
		return &domain.TaskOutput{
			Success: true,
			Data:    data,
		}, nil
	}

	// Return raw output if not JSON
	return &domain.TaskOutput{
		Success: true,
		Data:    map[string]any{"output": output},
	}, nil
}

// ProcessRunner executes activities as local processes.
type ProcessRunner struct {
	Program string
	Args    []string
	Dir     string
	Timeout string
}

func (r *ProcessRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskInput, error) {
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

	return &domain.TaskInput{
		Type:    "process",
		Program: r.Program,
		Args:    r.Args,
		Dir:     r.Dir,
		Env:     env,
		Input:   params,
	}, nil
}

func (r *ProcessRunner) ParseResult(result *domain.TaskOutput) (map[string]any, error) {
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

// Execute implements domain.InlineExecutor for local process execution.
func (r *ProcessRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskOutput, error) {
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
				return &domain.TaskOutput{
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
		return &domain.TaskOutput{
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
		return &domain.TaskOutput{
			Success: true,
			Data:    data,
		}, nil
	}

	// Return raw output if not JSON
	return &domain.TaskOutput{
		Success: true,
		Data:    map[string]any{"output": output},
	}, nil
}

// HTTPRunner executes activities by calling HTTP endpoints.
type HTTPRunner struct {
	URL     string
	Method  string // defaults to POST
	Headers map[string]string
	Timeout string
}

func (r *HTTPRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskInput, error) {
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

	return &domain.TaskInput{
		Type:    "http",
		URL:     r.URL,
		Method:  method,
		Headers: headers,
		Body:    string(body),
		Input:   params,
	}, nil
}

func (r *HTTPRunner) ParseResult(result *domain.TaskOutput) (map[string]any, error) {
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
func (r *HTTPRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskOutput, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return &domain.TaskOutput{
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
		return &domain.TaskOutput{
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
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("http request failed: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("read response: %v", err),
		}, nil
	}

	if resp.StatusCode >= 400 {
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("http status %d: %s", resp.StatusCode, string(respBody)),
		}, nil
	}

	// Try to parse as JSON
	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err == nil {
		return &domain.TaskOutput{
			Success: true,
			Data:    data,
		}, nil
	}

	// Return raw output if not JSON
	return &domain.TaskOutput{
		Success: true,
		Data:    map[string]any{"output": string(respBody)},
	}, nil
}

// InlineRunner executes activities in-process using a function.
// Useful for testing and simple activities that don't need isolation.
type InlineRunner struct {
	Func func(ctx context.Context, params map[string]any) (map[string]any, error)
}

func (r *InlineRunner) ToSpec(ctx context.Context, params map[string]any) (*domain.TaskInput, error) {
	return &domain.TaskInput{
		Type:  "inline",
		Input: params,
	}, nil
}

func (r *InlineRunner) ParseResult(result *domain.TaskOutput) (map[string]any, error) {
	if !result.Success {
		return nil, fmt.Errorf("inline execution failed: %s", result.Error)
	}
	return result.Data, nil
}

// Execute runs the inline function directly (used by in-process engine).
func (r *InlineRunner) Execute(ctx context.Context, params map[string]any) (*domain.TaskOutput, error) {
	output, err := r.Func(ctx, params)
	if err != nil {
		return &domain.TaskOutput{
			Success: false,
			Error:   err.Error(),
		}, nil
	}
	return &domain.TaskOutput{
		Success: true,
		Data:    output,
	}, nil
}

// Verify interface compliance.
var _ domain.Runner = (*ContainerRunner)(nil)
var _ domain.Runner = (*ProcessRunner)(nil)
var _ domain.Runner = (*HTTPRunner)(nil)
var _ domain.Runner = (*InlineRunner)(nil)
var _ domain.InlineExecutor = (*ContainerRunner)(nil)
var _ domain.InlineExecutor = (*ProcessRunner)(nil)
var _ domain.InlineExecutor = (*HTTPRunner)(nil)
var _ domain.InlineExecutor = (*InlineRunner)(nil)
