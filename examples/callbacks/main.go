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
	logger *slog.Logger
}

func NewLoggingCallbacks(logger *slog.Logger) *LoggingCallbacks {
	return &LoggingCallbacks{logger: logger}
}

func (c *LoggingCallbacks) BeforeWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) error {
	c.logger.Info("🚀 Starting workflow execution",
		"execution_id", event.ExecutionID,
		"workflow", event.WorkflowName,
		"inputs", event.Inputs)
	return nil
}

func (c *LoggingCallbacks) AfterWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) error {
	c.logger.Info("✅ Workflow execution completed",
		"execution_id", event.ExecutionID,
		"workflow", event.WorkflowName,
		"duration", event.Duration,
		"status", event.Status)
	return nil
}

func (c *LoggingCallbacks) OnWorkflowExecutionFailure(ctx context.Context, event *workflow.WorkflowExecutionEvent) error {
	c.logger.Error("❌ Workflow execution failed",
		"execution_id", event.ExecutionID,
		"workflow", event.WorkflowName,
		"error", event.Error)
	return nil
}

func (c *LoggingCallbacks) BeforePathExecution(ctx context.Context, event *workflow.PathExecutionEvent) error {
	c.logger.Debug("🛤️ Starting path execution",
		"execution_id", event.ExecutionID,
		"path_id", event.PathID,
		"current_step", event.CurrentStep)
	return nil
}

func (c *LoggingCallbacks) AfterPathExecution(ctx context.Context, event *workflow.PathExecutionEvent) error {
	c.logger.Debug("🏁 Path execution completed",
		"execution_id", event.ExecutionID,
		"path_id", event.PathID,
		"duration", event.Duration)
	return nil
}

func (c *LoggingCallbacks) OnPathFailure(ctx context.Context, event *workflow.PathExecutionEvent) error {
	c.logger.Error("💥 Path execution failed",
		"execution_id", event.ExecutionID,
		"path_id", event.PathID,
		"error", event.Error)
	return nil
}

func (c *LoggingCallbacks) BeforeActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) error {
	c.logger.Debug("🔧 Starting activity",
		"execution_id", event.ExecutionID,
		"activity", event.ActivityName,
		"parameters", event.Parameters)
	return nil
}

func (c *LoggingCallbacks) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) error {
	c.logger.Debug("✨ Activity completed",
		"execution_id", event.ExecutionID,
		"activity", event.ActivityName,
		"duration", event.Duration)
	return nil
}

func (c *LoggingCallbacks) OnActivityFailure(ctx context.Context, event *workflow.ActivityExecutionEvent) error {
	c.logger.Error("⚠️ Activity failed",
		"execution_id", event.ExecutionID,
		"activity", event.ActivityName,
		"error", event.Error)
	return nil
}

// MetricsCallbacks implements ExecutionCallbacks to collect execution metrics
type MetricsCallbacks struct {
	WorkflowExecutions    int
	SuccessfulExecutions  int
	FailedExecutions      int
	TotalActivityDuration time.Duration
}

func (m *MetricsCallbacks) BeforeWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) error {
	m.WorkflowExecutions++
	return nil
}

func (m *MetricsCallbacks) AfterWorkflowExecution(ctx context.Context, event *workflow.WorkflowExecutionEvent) error {
	m.SuccessfulExecutions++
	return nil
}

func (m *MetricsCallbacks) OnWorkflowExecutionFailure(ctx context.Context, event *workflow.WorkflowExecutionEvent) error {
	m.FailedExecutions++
	return nil
}

func (m *MetricsCallbacks) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) error {
	m.TotalActivityDuration += event.Duration
	return nil
}

// All other methods are no-ops for metrics collection
func (m *MetricsCallbacks) BeforePathExecution(ctx context.Context, event *workflow.PathExecutionEvent) error {
	return nil
}

func (m *MetricsCallbacks) AfterPathExecution(ctx context.Context, event *workflow.PathExecutionEvent) error {
	return nil
}

func (m *MetricsCallbacks) OnPathFailure(ctx context.Context, event *workflow.PathExecutionEvent) error {
	return nil
}

func (m *MetricsCallbacks) BeforeActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) error {
	return nil
}

func (m *MetricsCallbacks) OnActivityFailure(ctx context.Context, event *workflow.ActivityExecutionEvent) error {
	return nil
}

func main() {
	// Set up logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

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
				Activity: "time.now",
				Store:    "start_time",
				Next:     []*workflow.Edge{{Step: "Process Data"}},
			},
			{
				Name:     "Process Data",
				Activity: "script",
				Parameters: map[string]any{
					"code": `"Processing started at " + state.start_time`,
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
		Logger:             logger,
		ExecutionCallbacks: callbacks, // Use the callback chain
		Activities: []workflow.Activity{
			workflow.NewActivityFunction("time.now", func(ctx context.Context, params map[string]any) (any, error) {
				return time.Now().Format(time.RFC3339), nil
			}),
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

	logger.Info("Starting workflow execution with callbacks...")

	if err := execution.Run(ctx); err != nil {
		logger.Error("Workflow execution failed", "error", err)
		os.Exit(1)
	}

	// Print final metrics
	logger.Info("Execution completed successfully!")
	logger.Info("Metrics collected",
		"total_executions", metricsCallbacks.WorkflowExecutions,
		"successful_executions", metricsCallbacks.SuccessfulExecutions,
		"failed_executions", metricsCallbacks.FailedExecutions,
		"total_activity_duration", metricsCallbacks.TotalActivityDuration)
}
