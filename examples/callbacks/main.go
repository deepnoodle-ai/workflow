package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/activities"
)

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
	logger := workflow.NewLogger()

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
				Store:    "start_time",
				Next:     []*workflow.Edge{{Step: "Process Data"}},
			},
			{
				Name:     "Process Data",
				Activity: "script",
				Parameters: map[string]any{
					"code": `"Processing started at " + state.start_time.format(time.RFC3339)`,
				},
				Store: "message",
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
			activities.NewScriptActivity(),
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
