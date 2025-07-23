# State Management Architecture

## Overview

The workflow library implements a **path-local state architecture** similar to
AWS Step Functions. Each execution path maintains its own independent state
variables, providing natural isolation and avoiding complex synchronization
mechanisms.

## Architecture Principles

### 1. Path-Local Ownership

Each execution path owns its state variables directly:

- **Independent Variables**: Each path has its own `map[string]any` of state variables
- **No Shared State**: Parallel paths cannot interfere with each other
- **Simple Mental Model**: Each path is like a separate execution context

### 2. State Copying on Branch

When workflow paths branch, each new path receives a complete copy of the parent's current state:

- **Full State Copy**: Child paths get all parent variables at branch time
- **Independent Evolution**: After branching, paths evolve their state independently  
- **No Synchronization**: Paths never need to coordinate state changes
- **Predictable Behavior**: State at branch time determines child path's initial state

### 3. Direct State Modification

Activities modify path-local state directly through the context:

- **Direct Access**: Activities use context methods to read and write variables
- **Immediate Updates**: State changes are applied directly to path variables
- **Simple API**: Use `ctx.SetVariable()`, `ctx.DeleteVariable()`, `ctx.GetVariable()`
- **Automatic Isolation**: Each path maintains its own copy of variables
- **Checkpointing**: Path state is automatically captured in checkpoints

## Implementation Details

### Context Interface

Activities receive a `workflow.Context` that provides access to state and inputs:

```go
// Context interface embedded in workflow.Context
type Context interface {
    context.Context
    VariableContainer

    // State access
    ListInputs() []string
    GetInput(key string) (value any, exists bool)
    
    // Infrastructure access
    GetLogger() *slog.Logger
    GetCompiler() script.Compiler
    GetPathID() string
    GetStepName() string
}

// VariableContainer interface for state variables
type VariableContainer interface {
    SetVariable(key string, value any)
    DeleteVariable(key string)
    ListVariables() []string
    GetVariable(key string) (value any, exists bool)
}
```

### State Access Helper Functions

Use these helper functions when you need _complete_ state snapshots:

```go
// Get all current variables as a map (copy)
variables := workflow.VariablesFromContext(ctx)

// Get all current inputs as a map (copy)  
inputs := workflow.InputsFromContext(ctx)
```

### Execution Flow

```
1. Path created → receives initial state copy
2. Activity executes → modifies state via context methods
3. Path branches → each child gets current state copy
4. Path completes → state saved to checkpoint
```

## Example Usage

### Direct Context Access

```go
func (a *MyActivity) Execute(ctx workflow.Context, params MyParams) (any, error) {
    // Access individual variables
    if value, exists := ctx.GetVariable("counter"); exists {
        counter := value.(int)
        ctx.SetVariable("counter", counter+1)
    }
    
    // Access inputs
    if userID, exists := ctx.GetInput("user_id"); exists {
        ctx.SetVariable("current_user", userID)
    }
    
    return "success", nil
}
```

### Script Activity Usage

The built-in script activity allows direct state manipulation in JavaScript:

```javascript
// Script modifies state variables directly
state.counter = state.counter + 1
state.last_updated = "2024-01-01"  
state.status = "processing"
```

**Result**: Changes are immediately visible to subsequent steps in the same path.

### Working with Complete State

```go
func (a *MyActivity) Execute(ctx workflow.Context, params MyParams) (any, error) {
    // Get complete current state when needed
    variables := workflow.VariablesFromContext(ctx)
    inputs := workflow.InputsFromContext(ctx)
    
    // Process data with external libraries
    result := processData(variables, inputs, params)
    
    // Update specific variables
    ctx.SetVariable("processed_data", result)
    ctx.SetVariable("last_processed", time.Now())
    
    return result, nil
}
```
