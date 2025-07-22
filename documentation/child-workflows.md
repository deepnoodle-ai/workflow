# Child Workflows

Child workflows allow you to break complex automation into smaller, reusable
components by calling one workflow from another. This feature enables better
modularity, maintainability, and reusability in your workflow designs.

## Overview

A child workflow is simply another workflow that gets executed as part of a
parent workflow's execution. Child workflows run with their own isolated state
and can be executed either synchronously (parent waits for completion) or
asynchronously (parent continues immediately).

## Basic Usage

Child workflows are invoked using the `workflow.child` activity:

```go
// Define a step that calls a child workflow
step := &workflow.Step{
    Name:     "Process Data",
    Activity: "workflow.child",
    Parameters: map[string]any{
        "workflow_name": "data-processing",
        "sync": true,  // Wait for completion
        "inputs": map[string]any{
            "input_file": "${state.source_file}",
            "config": "${state.processing_config}",
        },
        "timeout": "10m",
    },
    Store: "processing_result",
    Next:  []*workflow.Edge{{Step: "Handle Results"}},
}
```

## Execution Modes

### Synchronous Execution (Default)

When `sync: true`, the parent workflow waits for the child to complete:

- **Blocking**: Parent path pauses until child completes
- **Output Available**: Child workflow outputs are stored in parent state
- **Error Propagation**: Child failures cause parent step to fail
- **Timeout Support**: Parent can specify maximum wait time

```go
Parameters: map[string]any{
    "workflow_name": "email-notification",
    "sync": true,
    "inputs": map[string]any{
        "recipient": "${state.user_email}",
        "template": "order_confirmation",
        "data": "${state.order_details}",
    },
}
```

### Asynchronous Execution

When `sync: false`, the parent continues immediately:

- **Non-blocking**: Parent continues while child runs independently
- **Handle Returned**: Parent receives execution ID for later reference
- **Fire-and-forget**: No automatic error propagation
- **Independent Lifecycle**: Child runs on its own timeline

```go
Parameters: map[string]any{
    "workflow_name": "audit-logging",
    "sync": false,
    "inputs": map[string]any{
        "action": "user_login",
        "user_id": "${state.current_user}",
        "timestamp": "${state.login_time}",
    },
}
```

## Input and Output Handling

**Inputs**: Child workflows receive inputs as their initial state variables.
You can pass any JSON-serializable data:

```go
"inputs": map[string]any{
    "user_data": "${state.user}",
    "config": map[string]any{
        "max_retries": 3,
        "timeout": "30s",
    },
    "tags": []string{"production", "critical"},
}
```

**Outputs**: For synchronous execution, child outputs become available in
parent state:

```go
// Child workflow ends with outputs
outputs := map[string]any{
    "processed_count": 150,
    "errors": []string{},
    "duration": "2m30s",
}

// Parent can access these via the Store parameter
Store: "child_result"
// Access as: ${state.child_result.processed_count}
```

## Use Cases

### 1. Modular Data Processing

Break complex data transformations into focused, testable components:

```
Main Pipeline:
├── Validate Input
├── → Child: Data Cleaning
├── → Child: Data Enrichment  
├── → Child: Quality Checks
└── Store Results
```

### 2. Parallel Processing

Execute independent operations simultaneously:

```go
// Launch multiple async children
{Activity: "workflow.child", Parameters: map[string]any{"workflow_name": "process-region-a", "sync": false}},
{Activity: "workflow.child", Parameters: map[string]any{"workflow_name": "process-region-b", "sync": false}},
{Activity: "workflow.child", Parameters: map[string]any{"workflow_name": "process-region-c", "sync": false}},
```

### 3. Reusable Components

Create workflow libraries for common operations:

```go
// Email notification workflow used by multiple parent workflows
"workflow_name": "send-notification",
"inputs": map[string]any{
    "type": "slack",
    "channel": "#alerts",
    "message": "Deployment completed successfully",
}
```

## Error Handling

**Synchronous**: Child workflow failures propagate to parent, allowing normal
retry and error handling patterns.

**Asynchronous**: Child failures don't affect parent execution. Monitor async
executions independently if needed.

## Design Philosophy

Child workflows follow the library's core principles:

- **State Isolation**: Each child runs with completely independent state
- **Path-Local Execution**: Child workflows participate in the same path-based execution model  
- **Activity Integration**: Child execution appears as a normal activity to the parent
- **Checkpoint Compatibility**: Parent-child relationships survive execution interruptions

This design enables workflow composition without adding architectural
complexity, making it easy to build maintainable automation systems from
smaller, focused components.

## Setup

To use child workflows, ensure your workflow registry contains all referenced workflows:

```go
registry := workflow.NewMemoryWorkflowRegistry()
registry.Register(parentWorkflow)
registry.Register(childWorkflow)

executor := workflow.NewDefaultChildWorkflowExecutor(workflow.ChildWorkflowExecutorOptions{
    WorkflowRegistry: registry,
    Activities: activities,
    // ... other options
})
```
