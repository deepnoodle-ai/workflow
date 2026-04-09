// Package workflowtest provides test utilities for the workflow library.
// It follows the standard Go convention of separate test helper packages
// (net/http/httptest, io/iotest).
package workflowtest

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/workflow"
)

// TestOptions allows overriding execution settings for test runs.
// Only fields that make sense to customize in tests are exposed.
type TestOptions struct {
	// ExecutionID sets a fixed execution ID. Auto-generated if empty.
	ExecutionID string

	// Checkpointer overrides the default in-memory checkpointer.
	Checkpointer workflow.Checkpointer

	// Callbacks receives execution lifecycle events.
	Callbacks workflow.ExecutionCallbacks

	// StepProgressStore receives step progress updates.
	StepProgressStore workflow.StepProgressStore
}

// Run executes a workflow with sensible defaults for testing.
// It uses an in-memory checkpointer, discards logs, and fails the test on
// infrastructure errors. Returns the execution result for assertions.
func Run(
	t testing.TB,
	wf *workflow.Workflow,
	activities []workflow.Activity,
	inputs map[string]any,
) *workflow.ExecutionResult {
	t.Helper()
	return RunWithOptions(t, wf, activities, inputs, TestOptions{})
}

// RunWithOptions is like Run but allows overriding execution options.
func RunWithOptions(
	t testing.TB,
	wf *workflow.Workflow,
	activities []workflow.Activity,
	inputs map[string]any,
	opts TestOptions,
) *workflow.ExecutionResult {
	t.Helper()

	checkpointer := opts.Checkpointer
	if checkpointer == nil {
		checkpointer = NewMemoryCheckpointer()
	}

	exec, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:           wf,
		Activities:         activities,
		Inputs:             inputs,
		ExecutionID:        opts.ExecutionID,
		Checkpointer:       checkpointer,
		ExecutionCallbacks: opts.Callbacks,
		StepProgressStore:  opts.StepProgressStore,
	})
	if err != nil {
		t.Fatalf("workflowtest.Run: creating execution: %v", err)
	}

	result, err := exec.Execute(context.Background())
	if err != nil {
		t.Fatalf("workflowtest.Run: executing: %v", err)
	}
	return result
}
