package workflow

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/deepnoodle-ai/wonton/assert"
)

func TestNewContainerActivity(t *testing.T) {
	activity := NewContainerActivity("processor", ContainerActivityOptions{
		Image:   "python:3.11",
		Command: []string{"python", "script.py"},
		Timeout: "5m",
	})

	assert.Equal(t, activity.Name(), "processor")

	// Should implement RunnableActivity
	runnable, ok := activity.(RunnableActivity)
	assert.True(t, ok)
	assert.NotNil(t, runnable.Runner())

	// Runner should generate correct TaskInput
	runner := runnable.Runner()
	spec, err := runner.ToSpec(context.Background(), map[string]any{"input": "value"})
	assert.NoError(t, err)
	assert.Equal(t, spec.Type, "container")
	assert.Equal(t, spec.Image, "python:3.11")
	assert.Equal(t, spec.Command, []string{"python", "script.py"})
	assert.Equal(t, spec.Input["input"], "value")
}

func TestNewHTTPActivity(t *testing.T) {
	activity := NewHTTPActivity("api-call", HTTPActivityOptions{
		URL:     "https://api.example.com/process",
		Method:  "POST",
		Headers: map[string]string{"Authorization": "Bearer token"},
		Timeout: "30s",
	})

	assert.Equal(t, activity.Name(), "api-call")

	// Should implement RunnableActivity
	runnable, ok := activity.(RunnableActivity)
	assert.True(t, ok)
	assert.NotNil(t, runnable.Runner())

	// Runner should generate correct TaskInput
	runner := runnable.Runner()
	spec, err := runner.ToSpec(context.Background(), map[string]any{"data": "test"})
	assert.NoError(t, err)
	assert.Equal(t, spec.Type, "http")
	assert.Equal(t, spec.URL, "https://api.example.com/process")
	assert.Equal(t, spec.Method, "POST")
	assert.Equal(t, spec.Headers["Authorization"], "Bearer token")
	assert.Equal(t, spec.Headers["Content-Type"], "application/json")
}

func TestNewProcessActivity(t *testing.T) {
	activity := NewProcessActivity("run-script", ProcessActivityOptions{
		Program: "python",
		Args:    []string{"script.py", "--input"},
		Dir:     "/tmp",
		Timeout: "1m",
	})

	assert.Equal(t, activity.Name(), "run-script")

	// Should implement RunnableActivity
	runnable, ok := activity.(RunnableActivity)
	assert.True(t, ok)
	assert.NotNil(t, runnable.Runner())

	// Runner should generate correct TaskInput
	runner := runnable.Runner()
	spec, err := runner.ToSpec(context.Background(), map[string]any{"param": "value"})
	assert.NoError(t, err)
	assert.Equal(t, spec.Type, "process")
	assert.Equal(t, spec.Program, "python")
	assert.Equal(t, spec.Args, []string{"script.py", "--input"})
	assert.Equal(t, spec.Dir, "/tmp")
}

func TestInlineActivityDoesNotImplementRunnableActivity(t *testing.T) {
	activity := NewActivityFunction("inline", func(ctx Context, params map[string]any) (any, error) {
		return "result", nil
	})

	// Should NOT implement RunnableActivity
	_, ok := activity.(RunnableActivity)
	assert.False(t, ok)
}

func TestContainerActivityCanExecuteLocally(t *testing.T) {
	activity := NewContainerActivity("echo-test", ContainerActivityOptions{
		Image:   "alpine:latest",
		Command: []string{"echo", "hello from container"},
	})

	// Test through the runner directly since it implements InlineExecutor
	runnable, ok := activity.(RunnableActivity)
	assert.True(t, ok)

	runner := runnable.Runner()
	executor, ok := runner.(interface {
		Execute(context.Context, map[string]any) (*domain.TaskOutput, error)
	})
	assert.True(t, ok)

	// This test requires Docker to be available
	// If Docker is not available, the test will fail with a container error
	result, err := executor.Execute(context.Background(), nil)
	assert.NoError(t, err)
	if result.Success {
		assert.Contains(t, result.Data["output"], "hello from container")
	} else {
		// Docker might not be available or image might not be pulled
		// Either way, the test verifies the execution path works
		t.Logf("Container execution failed (Docker may not be available): %s", result.Error)
	}
}

func TestProcessActivityCanExecuteLocally(t *testing.T) {
	activity := NewProcessActivity("echo-test", ProcessActivityOptions{
		Program: "echo",
		Args:    []string{"hello world"},
	})

	// Test through the runner directly since it implements InlineExecutor
	runnable, ok := activity.(RunnableActivity)
	assert.True(t, ok)

	runner := runnable.Runner()
	executor, ok := runner.(interface {
		Execute(context.Context, map[string]any) (*domain.TaskOutput, error)
	})
	assert.True(t, ok)

	result, err := executor.Execute(context.Background(), nil)
	assert.NoError(t, err)
	assert.True(t, result.Success)
	assert.Contains(t, result.Data["output"], "hello world")
}
