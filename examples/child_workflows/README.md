# Child Workflows Example

This example demonstrates the child workflow functionality, inspired
by AWS Step Functions' nested workflow capabilities. The parent
workflow synchronously orchestrates two child workflows and waits for
each to complete before continuing.

## What This Example Shows

### Core Features

1. **Parent-Child Orchestration**: A parent workflow runs two child
   workflows in sequence.
2. **Wait for Completion**: The `workflow.child` activity blocks the
   parent step until the child finishes (or its `timeout` elapses),
   then writes the result into parent state via `Step.Store`.
3. **Data Flow**: Inputs are templated from parent state with `${...}`
   and the child's outputs are read back through the stored result.
4. **Workflow Registry**: A central registry holds the child
   workflow definitions so the activity can resolve them by name.

### Architecture Overview

```
Main Orchestrator Workflow (Parent)
├── Set Initial Data
├── → Child: Data Processor Workflow
│   ├── Process Input Data
│   └── Return Result
├── Extract Result from Child
├── → Child: Data Validator Workflow
│   ├── Validate Input
│   └── Report Validation
├── Check Validation Result
└── Success/Failure Based on Validation
```

## Key Components

### 1. WorkflowRegistry

In-memory registry of child workflow definitions, looked up by name.

### 2. ChildWorkflowExecutor

Owns the lifecycle of child executions. The interface exposes both
`ExecuteSync` (block until done) and `ExecuteAsync` (return a handle,
poll via `GetResult`). The bundled `workflow.child` activity always
calls `ExecuteSync`; build your own activity if you need fire-and-
forget semantics.

### 3. `workflow.child` activity

- **Activity Name**: `"workflow.child"`
- **Parameters**:
  - `workflow_name`: Name of child workflow to execute
  - `timeout`: `time.Duration` cap on the child execution (0 = no cap)
  - `inputs`: Data to pass to the child as workflow inputs
  - `parent_id`: Optional tracing ID

## Wait-for-Completion Pattern

The "wait for child" pattern in this library is just `workflow.child`
with `Step.Store`:

```go
{
    Name:     "Call Data Processor",
    Activity: "workflow.child",
    Parameters: map[string]any{
        "workflow_name": "data-processor",
        "timeout":       30 * time.Second,
        "inputs":        map[string]any{"raw_data": "${state.raw_data}"},
    },
    Store: "processing_workflow_result",
    Next:  []*workflow.Edge{{Step: "Extract Result"}},
}
```

The parent step does not advance until the child has reached a
terminal state. The stored result has shape:

```go
map[string]any{
    "outputs":      map[string]any{ /* child outputs */ },
    "status":       "completed",
    "execution_id": "exec_…",
    "duration":     1.234, // seconds
    "success":      true,
}
```

## Async children and durability

`ExecuteAsync` is single-process and non-durable: the in-flight handle
lives in memory only. If the parent process restarts while an async
child is running, the child goroutine dies with the process and the
resumed parent can no longer resolve its handle. For workflows that
must survive restarts, use `ExecuteSync` (this example), or model the
child as an independent top-level execution coordinated via signals.
