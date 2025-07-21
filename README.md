# Workflow Automation Library

A robust yet easy-to-use workflow automation library in Go that enables the definition and execution of complex, multi-step processes with support for parallel execution, conditional branching, failure recovery, and retry mechanisms.

## Features

✅ **Graph-based workflow topology** - Define complex workflows with branching and merging
✅ **Parallel execution paths** - Native support for concurrent workflow execution
✅ **Retry logic with exponential backoff** - Configurable retry policies for resilient execution
✅ **Error classification** - Smart detection of recoverable vs non-recoverable errors
✅ **Checkpoint-based recovery** - Resume workflows from failures without losing progress
✅ **Risor scripting integration** - Safe script evaluation with deterministic contexts
✅ **Pluggable action system** - Extensible operations for integrating external systems
✅ **Fluent API** - Easy-to-use builder patterns for workflow creation
✅ **Comprehensive logging** - Built-in operation tracking and debugging support

## Quick Start

### Simple Linear Workflow

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/deepnoodle-ai/workflow"
)

func main() {
    // Create a simple workflow with retry configuration
    wf, err := workflow.SimpleWorkflow("data-processing",
        workflow.NewActionStep("get-time", "Time.Now", nil).
            WithStore("start_time").
            WithRetry(3, 2*time.Second),
        
        workflow.NewActionStep("print-message", "Print", map[string]any{
            "Message": "Processing started at ${state.start_time}",
        }),
        
        workflow.NewWaitStep("delay", 1*time.Second),
        
        workflow.NewScriptStep("process-data", `
            result = {
                "processed_at": state.start_time,
                "status": "completed"
            }
        `).WithStore("result"),
    )
    if err != nil {
        panic(err)
    }

    // Execute the workflow
    execution, err := workflow.NewExecution(workflow.ExecutionOptions{
        Workflow: wf,
        Inputs:   map[string]interface{}{},
    })
    if err != nil {
        panic(err)
    }

    if err := execution.Run(context.Background()); err != nil {
        panic(err)
    }

    fmt.Printf("Workflow completed with status: %s\n", execution.Status())
}
```

### Builder Pattern Workflow

```go
wf, err := workflow.NewWorkflowBuilder("advanced-example").
    WithDescription("Advanced workflow showcasing builder pattern").
    WithInputs(
        workflow.NewStringInput("source_url", "Data source URL", true),
        workflow.NewStringInput("output_path", "Output file path", true),
    ).
    WithOutput(workflow.NewStringOutput("result", "Processing result")).
    AddStep(
        workflow.NewActionStep("fetch-data", "HTTP.Get", map[string]any{
            "url": "${inputs.source_url}",
        }).WithStore("raw_data").WithRetryAndTimeout(3, 1*time.Second, 30*time.Second),
    ).
    AddStep(
        workflow.NewScriptStep("process-data", `
            processed = transform(state.raw_data)
        `).WithStore("processed_data"),
    ).
    AddStep(
        workflow.NewActionStep("save-results", "File.Write", map[string]any{
            "path": "${inputs.output_path}",
            "data": "${state.processed_data}",
        }),
    ).
    Build()
```

### Parallel Execution Workflow

```go
start := workflow.NewActionStep("initialize", "Print", map[string]any{
    "Message": "Starting parallel processing",
})

task1 := workflow.NewActionStep("task1", "Sleep", map[string]any{"Seconds": 2})
task2 := workflow.NewActionStep("task2", "Sleep", map[string]any{"Seconds": 2})
task3 := workflow.NewActionStep("task3", "Sleep", map[string]any{"Seconds": 2})

wf, err := workflow.ParallelWorkflow("parallel-processing", start, task1, task2, task3)
// This will complete in ~2 seconds instead of ~6 seconds sequentially
```

### Conditional Branching

```go
condition := workflow.NewScriptStep("check-status", `state.error_count > 5`).WithStore("has_errors")

successPath := workflow.NewActionStep("handle-success", "Print", map[string]any{
    "Message": "Processing successful",
})

errorPath := workflow.NewActionStep("handle-errors", "Print", map[string]any{
    "Message": "Processing failed, handling errors",
})

wf, err := workflow.ConditionalWorkflow("conditional-example", condition, successPath, errorPath)
```

## Retry Configuration

The library provides sophisticated retry mechanisms with exponential backoff:

```go
step := workflow.NewActionStep("flaky-operation", "HTTP.Get", params).
    WithRetry(3, 1*time.Second).  // Max 3 retries, 1s base delay
    WithRetryAndTimeout(5, 2*time.Second, 30*time.Second)  // With timeout
```

### Error Classification

The library automatically classifies errors as recoverable or non-recoverable:

- **Recoverable errors** (will be retried):
  - Network timeouts
  - Connection refused/reset
  - Rate limiting
  - Temporary server errors (500, 502, 503)

- **Non-recoverable errors** (won't be retried):
  - Authentication failures
  - Bad requests (400)
  - Not found (404)
  - Explicitly marked non-recoverable errors

You can also explicitly control error recovery:

```go
// This error will be retried
return nil, retry.NewRecoverableError(err)

// This error will NOT be retried
return nil, retry.NewNonRecoverableError(err)
```

## Checkpoint Recovery

Enable persistent execution state for failure recovery:

```go
checkpointer, err := workflow.NewFileCheckpointer("/tmp/workflow-checkpoints")

execution, err := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:     wf,
    Checkpointer: checkpointer,
    ExecutionID:  "my-workflow-run",
})

// First run - may fail partway through
if err := execution.Run(ctx); err != nil {
    log.Printf("Workflow failed: %v", err)
}

// Resume from checkpoint
if err := execution.ResumeFromFailure(ctx); err != nil {
    log.Printf("Resume failed: %v", err)
}
```

## Built-in Actions

- **`Time.Now`** - Get current timestamp
- **`Print`** - Output messages 
- **`Sleep`** - Configurable delays
- **`Fail`** - Simulate failures for testing

### Custom Actions

Register your own actions:

```go
type HTTPGetAction struct{}

func (a *HTTPGetAction) Name() string {
    return "HTTP.Get"
}

func (a *HTTPGetAction) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
    url := params["url"].(string)
    // ... implement HTTP GET
    return response, nil
}

workflow.RegisterAction(&HTTPGetAction{})
```

## Step Types

- **Action Steps** - Execute pluggable actions
- **Script Steps** - Run Risor scripts with state access
- **Wait Steps** - Introduce delays
- **Each Steps** - Iterate over collections

```go
// Each step example
step := workflow.NewScriptStep("process-items", `
    result = item * 2  // Process each item
`).WithEach([]int{1, 2, 3, 4, 5}, "item").WithStore("results")
```

## State Management

Workflows maintain state across steps:

```go
// Store values
step.WithStore("variable_name")

// Access in templates
"Message": "Value is ${state.variable_name}"

// Access in scripts
result = state.variable_name + 10

// Access inputs
"URL": "${inputs.source_url}"
```

## Monitoring and Debugging

Built-in structured logging and operation tracking:

```go
execution, err := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow: wf,
    Logger:   slog.New(slog.NewJSONHandler(os.Stdout, nil)),
    OperationLogger: workflow.NewFileOperationLogger("/tmp/operations.log"),
})
```

## Architecture

The library follows a clean architecture with:

- **Workflows** - Define process structure and flow
- **Steps** - Individual operations with retry and timeout configuration
- **Execution Engine** - Manages parallel paths and state
- **Actions** - Pluggable external operations
- **Checkpointing** - Persistent state for recovery
- **Operation Logging** - Complete audit trail

## Thread Safety

All components are designed to be thread-safe:
- Concurrent execution path management
- Thread-safe state operations
- Parallel step execution
- Safe checkpoint serialization

## Testing

The library includes comprehensive examples and test coverage:

```bash
go test -v ./...
```

## Performance

- Minimal overhead for simple workflows  
- Efficient parallel execution
- Optimized checkpoint serialization
- Resource-conscious operation logging

## Contributing

1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure all tests pass
5. Submit a pull request
