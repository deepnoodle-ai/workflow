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

Activities work directly with path-local state:

- **Immediate Updates**: State changes are applied directly to path variables
- **No Patches**: No complex diff/patch system required
- **Simple API**: Activities use `SetVariable()`, `DeleteVariable()`, `GetVariables()`
- **Checkpointing**: Path state is automatically captured in checkpoints

## Implementation Details

### PathLocalState Interface

```go
type PathLocalState struct {
    inputs    map[string]any  // Read-only workflow inputs
    variables map[string]any  // Mutable path-local variables
}

// Direct state access methods
func (s *PathLocalState) SetVariable(key string, value any)
func (s *PathLocalState) DeleteVariable(key string)  
func (s *PathLocalState) GetVariables() map[string]any
func (s *PathLocalState) GetInputs() map[string]any
```

### Execution Flow

```
1. Path created → receives initial state copy
2. Activity executes → direct state modifications  
3. Path branches → each child gets state copy
4. Path completes → state saved to checkpoint
```

## Example Usage

### Script Activity with State Changes

```javascript
// Activity modifies path state directly
state.counter = state.counter + 1
state.last_updated = "2024-01-01"  
state.status = "processing"
```

**Result**: Changes immediately visible to subsequent steps in the same path.

### Conditional Branching with Isolated State

```go
{
    Name: "Process Data",
    Activity: "script",
    Parameters: map[string]any{
        "code": "state.processed = true; state.count++",
    },
    Next: []*Edge{
        {Step: "Success Path", Condition: "state.count > 5"},
        {Step: "Continue Path", Condition: "state.count <= 5"},
    },
}
```

**Behavior**: 
- Both child paths get a copy of state with `processed=true` and incremented `count`
- Each path can modify its state copy independently
- No coordination needed between parallel paths

## Implementation Patterns

### Activity Development

```go
func myActivity(ctx context.Context, params map[string]any) (any, error) {
    // Get path-local state from context
    pathState, _ := workflow.GetStateFromContext(ctx)
    
    // Read current values
    currentValue := pathState.GetVariables()["my_var"]
    
    // Modify state directly
    pathState.SetVariable("result", processedValue)
    pathState.SetVariable("timestamp", time.Now())
    
    return result, nil
}
```
