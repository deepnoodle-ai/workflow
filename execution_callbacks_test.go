package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/internal/require"
)

// TestCallbacksImplementation is a test implementation of ExecutionCallbacks
type TestCallbacksImplementation struct {
	events []string
}

func (t *TestCallbacksImplementation) BeforeWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("BeforeWorkflowExecution: %s (%s)", event.ExecutionID, event.WorkflowName))
}

func (t *TestCallbacksImplementation) AfterWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("AfterWorkflowExecution: %s (%s) - Duration: %s",
		event.ExecutionID, event.WorkflowName, event.Duration))
}

func (t *TestCallbacksImplementation) OnWorkflowExecutionFailure(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("OnWorkflowExecutionFailure: %s - Error: %s",
		event.ExecutionID, event.Error))
}

func (t *TestCallbacksImplementation) BeforeBranchExecution(ctx context.Context, event *workflow.BranchExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("BeforeBranchExecution: %s - Path: %s",
		event.ExecutionID, event.BranchID))
}

func (t *TestCallbacksImplementation) AfterBranchExecution(ctx context.Context, event *workflow.BranchExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("AfterBranchExecution: %s - Path: %s - Duration: %s",
		event.ExecutionID, event.BranchID, event.Duration))
}

func (t *TestCallbacksImplementation) OnBranchFailure(ctx context.Context, event *workflow.BranchExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("OnBranchFailure: %s - Path: %s - Error: %s",
		event.ExecutionID, event.BranchID, event.Error))
}

func (t *TestCallbacksImplementation) BeforeActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("BeforeActivityExecution: %s - Activity: %s",
		event.ExecutionID, event.ActivityName))
}

func (t *TestCallbacksImplementation) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("AfterActivityExecution: %s - Activity: %s - Duration: %s",
		event.ExecutionID, event.ActivityName, event.Duration))
}

func (t *TestCallbacksImplementation) OnActivityFailure(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	t.events = append(t.events, fmt.Sprintf("OnActivityFailure: %s - Activity: %s - Error: %s",
		event.ExecutionID, event.ActivityName, event.Error))
}

func (t *TestCallbacksImplementation) GetEvents() []string {
	return t.events
}

func TestExecutionCallbacks(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	// Create test workflow
	wf, err := workflow.New(workflow.Options{
		Name: "callback-test",
		Steps: []*workflow.Step{
			{
				Name:     "Get Time",
				Activity: "time.now",
				Store:    "current_time",
				Next:     []*workflow.Edge{{Step: "Print Message"}},
			},
			{
				Name:     "Print Message",
				Activity: "print",
				Parameters: map[string]any{
					"message": "Current time: ${state.current_time}",
				},
			},
		},
	})
	require.NoError(t, err)

	// Create test callbacks
	callbacks := &TestCallbacksImplementation{events: []string{}}

	// Create execution with callbacks
	reg := workflow.NewActivityRegistry()
	reg.MustRegister(workflow.ActivityFunc("time.now", func(ctx workflow.Context, params map[string]any) (any, error) {
				return "2025-01-01T12:00:00Z", nil
			}))
	reg.MustRegister(workflow.ActivityFunc("print", func(ctx workflow.Context, params map[string]any) (any, error) {
				message := params["message"].(string)
				fmt.Printf("Printed: %s\n", message)
				return nil, nil
			}))
	execution, err := workflow.NewExecution(wf, reg,
		workflow.WithScriptCompiler(workflow.NewTestCompiler()),
		workflow.WithLogger(logger),
		workflow.WithExecutionCallbacks(callbacks),
	)
	require.NoError(t, err)

	// Run execution
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = execution.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, workflow.ExecutionStatusCompleted, execution.Status())

	// Verify callbacks were called
	events := callbacks.GetEvents()
	require.NotEmpty(t, events)

	// Print all events for debugging
	fmt.Println("Callback events:")
	for i, event := range events {
		fmt.Printf("%d: %s\n", i, event)
	}

	// Check that we have the expected callback types
	eventTypes := make(map[string]bool)
	for _, event := range events {
		eventType := strings.Split(event, ":")[0]
		eventTypes[eventType] = true
	}

	// Verify we got the main callback types
	require.True(t, eventTypes["BeforeWorkflowExecution"], "Should have BeforeWorkflowExecution")
	require.True(t, eventTypes["AfterWorkflowExecution"], "Should have AfterWorkflowExecution")
	require.True(t, eventTypes["BeforeBranchExecution"], "Should have BeforeBranchExecution")
	require.True(t, eventTypes["AfterBranchExecution"], "Should have AfterBranchExecution")
	require.True(t, eventTypes["BeforeActivityExecution"], "Should have BeforeActivityExecution")
	require.True(t, eventTypes["AfterActivityExecution"], "Should have AfterActivityExecution")
}

func TestExecutionCallbacksWithFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	// Create test workflow with failing step
	wf, err := workflow.New(workflow.Options{
		Name: "callback-failure-test",
		Steps: []*workflow.Step{
			{
				Name:     "Failing Step",
				Activity: "fail",
			},
		},
	})
	require.NoError(t, err)

	// Create test callbacks
	callbacks := &TestCallbacksImplementation{events: []string{}}

	// Create execution with callbacks
	reg := workflow.NewActivityRegistry()
	reg.MustRegister(workflow.ActivityFunc("fail", func(ctx workflow.Context, params map[string]any) (any, error) {
				return nil, errors.New("intentional failure")
			}))
	execution, err := workflow.NewExecution(wf, reg,
		workflow.WithScriptCompiler(workflow.NewTestCompiler()),
		workflow.WithLogger(logger),
		workflow.WithExecutionCallbacks(callbacks),
	)
	require.NoError(t, err)

	// Run execution (should fail)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := execution.Execute(ctx)
	require.NoError(t, err)
	require.True(t, result.Failed())
	require.Equal(t, workflow.ExecutionStatusFailed, execution.Status())

	// Verify failure callbacks were called
	events := callbacks.GetEvents()
	require.NotEmpty(t, events)

	// Print all events for debugging
	fmt.Println("Failure callback events:")
	for i, event := range events {
		fmt.Printf("%d: %s\n", i, event)
	}

	// Check that we have the expected failure callback types
	eventTypes := make(map[string]bool)
	for _, event := range events {
		eventType := strings.Split(event, ":")[0]
		eventTypes[eventType] = true
	}

	// Verify we got failure callbacks
	require.Equal(t, 6, len(eventTypes), "Should have 6 callbacks")
	require.Equal(t, map[string]bool{
		"BeforeWorkflowExecution": true,
		"AfterWorkflowExecution":  true,
		"BeforeBranchExecution":     true,
		"AfterBranchExecution":      true,
		"BeforeActivityExecution": true,
		"AfterActivityExecution":  true,
	}, eventTypes)
}

func TestCallbackChain(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	// Create test workflow
	wf, err := workflow.New(workflow.Options{
		Name: "callback-chain-test",
		Steps: []*workflow.Step{
			{
				Name:     "Simple Step",
				Activity: "simple",
			},
		},
	})
	require.NoError(t, err)

	// Create multiple callback implementations
	callbacks1 := &TestCallbacksImplementation{events: []string{}}
	callbacks2 := &TestCallbacksImplementation{events: []string{}}

	// Chain them together
	callbackChain := workflow.NewCallbackChain(callbacks1, callbacks2)

	// Create execution with callback chain
	reg := workflow.NewActivityRegistry()
	reg.MustRegister(workflow.ActivityFunc("simple", func(ctx workflow.Context, params map[string]any) (any, error) {
				return "done", nil
			}))
	execution, err := workflow.NewExecution(wf, reg,
		workflow.WithScriptCompiler(workflow.NewTestCompiler()),
		workflow.WithLogger(logger),
		workflow.WithExecutionCallbacks(callbackChain),
	)
	require.NoError(t, err)

	// Run execution
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = execution.Execute(ctx)
	require.NoError(t, err)

	// Verify both callback implementations were called
	events1 := callbacks1.GetEvents()
	events2 := callbacks2.GetEvents()

	require.NotEmpty(t, events1)
	require.NotEmpty(t, events2)
	require.Equal(t, len(events1), len(events2), "Both callback chains should receive the same events")

	fmt.Printf("Callback chain 1 received %d events\n", len(events1))
	fmt.Printf("Callback chain 2 received %d events\n", len(events2))
}
