package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// Executor handles task execution for different task types.
type Executor struct {
	// HTTPClient is used for HTTP tasks. If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// MaxOutputSize limits the output captured from processes/containers.
	// Default is 1MB.
	MaxOutputSize int64
}

// DefaultExecutor returns an executor with sensible defaults.
func DefaultExecutor() *Executor {
	return &Executor{
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		MaxOutputSize: 1024 * 1024, // 1MB
	}
}

// Execute runs a task and returns the result.
func (e *Executor) Execute(ctx context.Context, task *domain.TaskClaimed) *domain.TaskOutput {
	// Apply task timeout if specified
	if task.Input.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, task.Input.Timeout)
		defer cancel()
	}

	switch task.Input.Type {
	case "http":
		return e.executeHTTP(ctx, task.Input)
	case "process":
		return e.executeProcess(ctx, task.Input)
	case "container":
		return e.executeContainer(ctx, task.Input)
	case "inline":
		return &domain.TaskOutput{
			Success: false,
			Error:   "inline tasks cannot be executed by remote workers",
		}
	default:
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("unknown task type: %s", task.Input.Type),
		}
	}
}

// executeHTTP performs an HTTP request.
func (e *Executor) executeHTTP(ctx context.Context, input *domain.TaskInput) *domain.TaskOutput {
	method := input.Method
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if input.Body != "" {
		bodyReader = strings.NewReader(input.Body)
	} else if len(input.Input) > 0 {
		// Serialize input as JSON body
		data, err := json.Marshal(input.Input)
		if err != nil {
			return &domain.TaskOutput{
				Success: false,
				Error:   fmt.Sprintf("failed to serialize input: %v", err),
			}
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, input.URL, bodyReader)
	if err != nil {
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("failed to create request: %v", err),
		}
	}

	// Set headers
	for k, v := range input.Headers {
		req.Header.Set(k, v)
	}

	// Default content-type for POST/PUT with body
	if bodyReader != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := e.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("request failed: %v", err),
		}
	}
	defer resp.Body.Close()

	// Read response body (with limit)
	maxSize := e.MaxOutputSize
	if maxSize == 0 {
		maxSize = 1024 * 1024
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return &domain.TaskOutput{
			Success: false,
			Error:   fmt.Sprintf("failed to read response: %v", err),
		}
	}

	// Try to parse as JSON
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		// Not JSON, store as output string
		data = nil
	}

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	result := &domain.TaskOutput{
		Success:  success,
		Output:   string(body),
		ExitCode: resp.StatusCode,
		Data:     data,
	}

	if !success {
		result.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	return result
}

// executeProcess runs a local process.
func (e *Executor) executeProcess(ctx context.Context, input *domain.TaskInput) *domain.TaskOutput {
	if input.Program == "" {
		return &domain.TaskOutput{
			Success: false,
			Error:   "no program specified",
		}
	}

	cmd := exec.CommandContext(ctx, input.Program, input.Args...)

	// Set working directory if specified
	if input.Dir != "" {
		cmd.Dir = input.Dir
	}

	// Set environment variables
	if len(input.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range input.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	// Pass input as JSON via stdin if provided
	if len(input.Input) > 0 {
		data, err := json.Marshal(input.Input)
		if err != nil {
			return &domain.TaskOutput{
				Success: false,
				Error:   fmt.Sprintf("failed to serialize input: %v", err),
			}
		}
		cmd.Stdin = bytes.NewReader(data)
	}

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	// Limit output size
	maxSize := int(e.MaxOutputSize)
	if maxSize == 0 {
		maxSize = 1024 * 1024
	}
	if len(output) > maxSize {
		output = output[:maxSize] + "\n... (truncated)"
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &domain.TaskOutput{
				Success:  false,
				Output:   output,
				Error:    err.Error(),
				ExitCode: -1,
			}
		}
	}

	// Try to parse stdout as JSON for structured data
	var data map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
		data = nil
	}

	result := &domain.TaskOutput{
		Success:  exitCode == 0,
		Output:   output,
		ExitCode: exitCode,
		Data:     data,
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("process exited with code %d", exitCode)
	}

	return result
}

// executeContainer runs a Docker container.
func (e *Executor) executeContainer(ctx context.Context, input *domain.TaskInput) *domain.TaskOutput {
	if input.Image == "" {
		return &domain.TaskOutput{
			Success: false,
			Error:   "no container image specified",
		}
	}

	// Build docker run command
	args := []string{"run", "--rm"}

	// Add environment variables
	for k, v := range input.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Add image
	args = append(args, input.Image)

	// Add command if specified
	args = append(args, input.Command...)

	cmd := exec.CommandContext(ctx, "docker", args...)

	// Pass input as JSON via stdin if provided
	if len(input.Input) > 0 {
		data, err := json.Marshal(input.Input)
		if err != nil {
			return &domain.TaskOutput{
				Success: false,
				Error:   fmt.Sprintf("failed to serialize input: %v", err),
			}
		}
		cmd.Stdin = bytes.NewReader(data)
		// Enable stdin for the container
		args = []string{"run", "--rm", "-i"}
		for k, v := range input.Env {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, input.Image)
		args = append(args, input.Command...)
		cmd = exec.CommandContext(ctx, "docker", args...)
		cmd.Stdin = bytes.NewReader(data)
	}

	// Capture stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	// Limit output size
	maxSize := int(e.MaxOutputSize)
	if maxSize == 0 {
		maxSize = 1024 * 1024
	}
	if len(output) > maxSize {
		output = output[:maxSize] + "\n... (truncated)"
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return &domain.TaskOutput{
				Success:  false,
				Output:   output,
				Error:    fmt.Sprintf("docker execution failed: %v", err),
				ExitCode: -1,
			}
		}
	}

	// Try to parse stdout as JSON for structured data
	var data map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
		data = nil
	}

	result := &domain.TaskOutput{
		Success:  exitCode == 0,
		Output:   output,
		ExitCode: exitCode,
		Data:     data,
	}

	if exitCode != 0 {
		result.Error = fmt.Sprintf("container exited with code %d", exitCode)
	}

	return result
}
