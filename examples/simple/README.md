# Simple Looping Workflow

This example demonstrates a workflow with:
- Looping via conditional edges
- State management with a counter variable
- File-based checkpointing for durability
- Custom activity functions

## Key Concepts

### Conditional Edges

The `Wait Then Loop` step uses conditional edges to create a loop:

```go
Next: []*workflow.Edge{
    {Step: "Get Current Time", Condition: "state.counter <= inputs.max_count"},
    {Step: "Finish", Condition: "state.counter > inputs.max_count"},
}
```

When `state.counter` is less than or equal to `inputs.max_count`, the workflow loops back to the beginning. Otherwise, it proceeds to the finish step.

### State Variables

The workflow initializes state with a counter:

```go
State: map[string]any{"counter": 1}
```

The `increment` activity reads the current counter value from the context and stores the incremented value:

```go
counter, ok := ctx.GetVariable("counter")
```

### File Checkpointing

The example uses `FileCheckpointer` to persist execution state:

```go
checkpointer, err := stores.NewFileCheckpointer("executions")
```

This enables workflow recovery if the process restarts.

## Running the Example

```bash
go run main.go
```

Expected output:
```
It is now 2024-01-15T10:30:00Z. The counter is 1. The max count is 5.
Incrementing counter: 1 -> 2
It is now 2024-01-15T10:30:01Z. The counter is 2. The max count is 5.
...
Finished!
Workflow completed successfully!
```
