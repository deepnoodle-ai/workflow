package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

// formatStartMessage builds the status string that the old example used
// to assemble via a Risor script. Simple Go activities replace
// state-mutating scripts now that the engine is expression-only.
type formatStartInput struct {
	StartTime any `json:"start_time"`
}

func formatStartMessage(ctx workflow.Context, in formatStartInput) (string, error) {
	return fmt.Sprintf("Processing started at %v", in.StartTime), nil
}

// LoggingCallbacks implements ExecutionCallbacks to provide observability
type LoggingCallbacks struct {
	workflow.BaseExecutionCallbacks
	logger *slog.Logger
}

func NewLoggingCallbacks(logger *slog.Logger) *LoggingCallbacks {
	return &LoggingCallbacks{logger: logger}
}

func (c *LoggingCallbacks) BeforeWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	c.logger.Info("🚀 Starting workflow execution",
		"execution_id", event.ExecutionID,
		"workflow", event.WorkflowName,
		"inputs", event.Inputs)
}

func (c *LoggingCallbacks) AfterWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	c.logger.Info("✅ Workflow execution completed",
		"execution_id", event.ExecutionID,
		"workflow", event.WorkflowName,
		"duration", event.Duration,
		"status", event.Status)
}

func (c *LoggingCallbacks) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	c.logger.Debug("✨ Activity completed",
		"execution_id", event.ExecutionID,
		"activity", event.ActivityName,
		"duration", event.Duration)
}

// MetricsCallbacks implements ExecutionCallbacks to collect execution metrics
type MetricsCallbacks struct {
	workflow.BaseExecutionCallbacks
	WorkflowExecutions    int
	SuccessfulExecutions  int
	FailedExecutions      int
	TotalActivityDuration time.Duration
}

func (m *MetricsCallbacks) BeforeWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	m.WorkflowExecutions++
}

func (m *MetricsCallbacks) AfterWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) {
	m.SuccessfulExecutions++
}

func (m *MetricsCallbacks) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
	m.TotalActivityDuration += event.Duration
}

func main() {
	// Set up logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Create callbacks
	loggingCallbacks := NewLoggingCallbacks(logger)
	metricsCallbacks := &MetricsCallbacks{}

	// Chain multiple callback implementations
	callbacks := workflow.NewCallbackChain(loggingCallbacks, metricsCallbacks)

	// Define workflow
	wf, err := workflow.New(workflow.Options{
		Name: "callback-demo",
		Steps: []*workflow.Step{
			{
				Name:     "Get Current Time",
				Activity: "time",
				Store:    "state.start_time",
				Next:     []*workflow.Edge{{Step: "Process Data"}},
			},
			{
				Name:     "Process Data",
				Activity: "format_start_message",
				Parameters: map[string]any{
					"start_time": "${state.start_time}",
				},
				Store: "state.message",
				Next:  []*workflow.Edge{{Step: "Print Result"}},
			},
			{
				Name:     "Print Result",
				Activity: "print",
				Parameters: map[string]any{
					"message": "${state.message}",
				},
			},
		},
	})
	if err != nil {
		logger.Error("Failed to create workflow", "error", err)
		os.Exit(1)
	}

	// Create execution with callbacks
	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:           wf,
		ExecutionCallbacks: callbacks,
		Activities: []workflow.Activity{
			activities.NewTimeActivity(),
			workflow.NewTypedActivityFunction("format_start_message", formatStartMessage),
			activities.NewPrintActivity(),
		},
	})
	if err != nil {
		logger.Error("Failed to create execution", "error", err)
		os.Exit(1)
	}

	// Execute workflow
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := execution.Run(ctx); err != nil {
		logger.Error("Workflow execution failed", "error", err)
		os.Exit(1)
	}

	// Print final metrics
	logger.Info("Metrics collected",
		"total_executions", metricsCallbacks.WorkflowExecutions,
		"successful_executions", metricsCallbacks.SuccessfulExecutions,
		"failed_executions", metricsCallbacks.FailedExecutions,
		"total_activity_duration", metricsCallbacks.TotalActivityDuration)
}
