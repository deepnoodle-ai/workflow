# YAML Workflow Definition

This example demonstrates defining workflows in YAML format instead of Go code.

## Key Concepts

### Loading from YAML

Use `workflow.LoadFile` to load a workflow definition from a YAML file:

```go
w, err := workflow.LoadFile("example.yaml")
if err != nil {
    log.Fatal(err)
}
```

### YAML Structure

```yaml
name: demo
description: A demo workflow
state:
  counter: 1
outputs:
  - name: counter
steps:
  - name: Start
    activity: print
    parameters:
      message: 'The counter is $(state.counter)'
    next:
      - step: Wait

  - name: Wait
    activity: wait
    parameters:
      seconds: 0.5
    next:
      - step: PrintResult

  - name: PrintResult
    activity: print
    parameters:
      message: 'Workflow completed!'
```

### YAML vs Go

YAML definitions support the same features as Go definitions:
- Inputs with defaults and validation
- State initialization
- Output declarations
- Conditional edges
- Retry and catch configurations
- All parameter interpolation (`$(inputs.x)`, `$(state.y)`, `$(steps.z.result)`)

### When to Use YAML

YAML definitions are useful for:
- Non-developers who need to define workflows
- Configuration-driven systems
- Dynamic workflow loading
- Version-controlled workflow definitions separate from code

## Running the Example

```bash
go run main.go
```

Expected output:
```
The workflow is running. The initial counter value is 1.
Workflow completed! Counter: 1
```
