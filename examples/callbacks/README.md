# Execution Callbacks

This example demonstrates how to use execution callbacks for observability and metrics collection.

## Key Concepts

### ExecutionCallbacks Interface

Callbacks provide hooks into workflow execution lifecycle events:

- `BeforeWorkflowExecution` - Called before a workflow starts
- `AfterWorkflowExecution` - Called after a workflow completes
- `BeforeActivityExecution` - Called before each activity runs
- `AfterActivityExecution` - Called after each activity completes

### BaseExecutionCallbacks

The `BaseExecutionCallbacks` struct provides empty default implementations, so you only need to override the methods you care about:

```go
type LoggingCallbacks struct {
    workflow.BaseExecutionCallbacks
    logger *slog.Logger
}

func (c *LoggingCallbacks) AfterActivityExecution(ctx context.Context, event *workflow.ActivityExecutionEvent) {
    c.logger.Debug("Activity completed",
        "activity", event.ActivityName,
        "duration", event.Duration)
}
```

### Callback Chaining

Multiple callback implementations can be combined using `NewCallbackChain`:

```go
loggingCallbacks := NewLoggingCallbacks(logger)
metricsCallbacks := &MetricsCallbacks{}

callbacks := workflow.NewCallbackChain(loggingCallbacks, metricsCallbacks)

execution, err := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:           wf,
    ExecutionCallbacks: callbacks,
    // ...
})
```

All chained callbacks receive each event in order.

### Use Cases

- **Logging**: Record workflow and activity execution details
- **Metrics**: Track execution counts, durations, and error rates
- **Tracing**: Integrate with distributed tracing systems
- **Alerting**: Trigger notifications on failures or slow executions

## Running the Example

```bash
go run main.go
```

Expected output:
```
INFO Starting workflow execution execution_id=abc123 workflow=callback-demo
DEBUG Activity completed execution_id=abc123 activity=time duration=1ms
DEBUG Activity completed execution_id=abc123 activity=print duration=1ms
INFO Workflow execution completed execution_id=abc123 duration=5ms status=completed
INFO Metrics collected total_executions=1 successful_executions=1 total_activity_duration=2ms
```
