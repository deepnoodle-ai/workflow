# Join Paths Example

This example demonstrates the **join functionality** in the workflow library, which allows multiple execution paths to converge and wait for each other before proceeding.

## Overview

The join functionality enables you to:
- **Wait for multiple paths**: A join step waits for specified paths to complete before proceeding
- **Store path variables**: Each path's variables are stored under specific keys in the current path's state
- **Continue execution**: After all required paths arrive, execution continues with all path variables available

## Workflow Structure

```
    start
     ↙ ↓ ↘
   a   b  final  
     ↘ ↓ ↙
     join     ← Waits for paths "a" and "b"
      ↓
   finalize
```

## Key Features

### 1. Join Configuration

```go
{
    Name: "join",
    Join: &workflow.JoinConfig{
        Paths: []string{"a", "b"},                    // Wait for specific paths
        PathMappings: map[string]string{              // Where to store path data
            // Store entire path state (current behavior)
            "a": "results.pathA",                     // All path "a" variables → "results.pathA"
            "b": "results.pathB",                     // All path "b" variables → "results.pathB"
            
            // Extract specific variables (new functionality)
            "a.result": "extracted.valueA",          // Only path "a" result → "extracted.valueA"
            "b.result": "extracted.valueB",          // Only path "b" result → "extracted.valueB"
            "a.user.name": "userName",               // Nested extraction → "userName"
        },
    },
    Next: []*workflow.Edge{
        {Step: "finalize"},
    },
}
```

### 2. Path Variable Storage

PathMappings supports two powerful modes for handling joined path data:

#### Full Path State Storage
Store entire path state under a specified key:
- **Syntax**: `"pathID": "destination"`
- **Example**: `"pathA": "results.pathA"` stores all pathA variables under results.pathA
- **Default behavior**: If no mapping provided, uses path name as the key

#### Variable Extraction  
Extract specific variables from paths:
- **Syntax**: `"pathID.variable": "destination"`
- **Example**: `"pathA.result": "extracted.value"` stores only pathA.result under extracted.value
- **Nested extraction**: `"pathA.user.profile.name": "userName"` extracts deeply nested values
- **Multiple extractions**: Extract different variables from the same path to different destinations

#### Combined Usage
You can mix both approaches in the same join:
```go
PathMappings: map[string]string{
    "pathA": "full.pathA",           // Store complete pathA state
    "pathA.result": "quick.valueA",  // Also extract just the result for easy access
    "pathB.status": "status",        // Extract only status from pathB
}
```

### 3. Flexible Path Specification

- **Named paths**: Wait for specific named paths using `Paths`
- **Count-based**: Wait for a specific number of paths using `Count`
- **All active**: Wait for all currently active paths (if neither `Paths` nor `Count` specified)

## Join Configuration Options

```go
type JoinConfig struct {
    // Paths specifies which named paths to wait for. If empty, waits for all active paths.
    Paths []string `json:"paths,omitempty"`
    
    // Count specifies the number of paths to wait for. If 0, waits for all specified paths.
    Count int `json:"count,omitempty"`
    
    // PathMappings specifies where to store each path's variables.
    // Key is the path name, value is the variable name to store that path's variables under.
    // If empty, uses path names as variable names.
    PathMappings map[string]string `json:"path_mappings,omitempty"`
}
```

## Running the Example

```bash
cd examples/join_paths
go run main.go
```

## Expected Output

```
Starting workflow with join functionality...
🚀 Setting up initial data...
⚙️  Path A: Processing data...
⚙️  Path B: Processing data...
   Path A completed with result: 200
   Path B completed with result: 300
🔗 Combining results from all paths...
   Path A result (from full state): 200
   Path B result (from full state): 300
   Extracted value A: 200
   Extracted value B: 300
   Extracted base value: 100
   Final combined result: 500

✅ Workflow completed successfully in 151ms
Status: completed
Final result: 500
Path A results: map[initial_data:map[base_value:100 multiplier:2] result_a:200]
Path B results: map[initial_data:map[base_value:100 multiplier:2] result_b:300]
Path A specific result: 200
Extracted value A: 200
Extracted value B: 300
Base value: 100
```

## Accessing Joined Variables

After a join completes, you can access path data in multiple ways:

### Accessing Full Path State
```go
workflow.NewActivityFunction("combine_results", func(ctx workflow.Context, params map[string]any) (any, error) {
    // Access the complete path state
    results, _ := ctx.GetVariable("results")
    resultsMap := results.(map[string]any)
    
    // Access path A and B results from full state
    pathAResults := resultsMap["pathA"].(map[string]any)
    pathBResults := resultsMap["pathB"].(map[string]any)
    
    // Get specific values from full state
    resultA := pathAResults["result_a"].(int)
    resultB := pathBResults["result_b"].(int)
    
    return resultA + resultB, nil
})
```

### Accessing Extracted Variables
```go
workflow.NewActivityFunction("combine_results", func(ctx workflow.Context, params map[string]any) (any, error) {
    // Access directly extracted variables (much simpler!)
    extracted, _ := ctx.GetVariable("extracted")
    extractedMap := extracted.(map[string]any)
    
    // Direct access to extracted values
    resultA := extractedMap["valueA"].(int)
    resultB := extractedMap["valueB"].(int)
    
    // Access individual extracted variables
    userName, _ := ctx.GetVariable("userName")  // From "pathA.user.name": "userName"
    
    return resultA + resultB, nil
})
```

## Nested Field Support

Both PathMappings and workflow outputs support dot notation for nested field access:

### Nested PathMappings

```go
Join: &workflow.JoinConfig{
    Paths: []string{"pathA", "pathB", "pathC"},
    PathMappings: map[string]string{
        "pathA": "results.processing.fast",    // Deeply nested storage
        "pathB": "results.processing.medium",  // Organizes results by type
        "pathC": "results.processing.slow",    // Clear hierarchical structure
    },
}
```

### Nested Workflow Outputs

```go
Outputs: []*workflow.Output{
    {Name: "fast_result", Variable: "results.processing.fast.final_value", Path: "main"},
    {Name: "all_medium", Variable: "results.processing.medium", Path: "main"},
    {Name: "summary", Variable: "results.summary", Path: "main"},
}
```

## Benefits of Variable Extraction

Variable extraction provides several advantages over storing full path state:

### 1. **Simplified Access**
```go
// Instead of: results.pathA.user.profile.name
userName, _ := ctx.GetVariable("userName")
```

### 2. **Performance & Memory**
- Extract only the data you need
- Reduce memory usage for large path states
- Faster access to specific values

### 3. **Cleaner Interfaces** 
- Present exactly the data consumers need
- Hide implementation details of path structure
- Create intuitive variable names

### 4. **Flexible Organization**
```go
PathMappings: map[string]string{
    "pathA.result": "processing.fast_result",
    "pathB.result": "processing.slow_result", 
    "pathA.error":  "errors.validation",
    "pathB.error":  "errors.processing",
}
```

This allows you to:
- **Organize data logically**: Group related values regardless of source path
- **Extract precisely**: Get exactly what you need with minimal overhead
- **Maintain clean interfaces**: Present well-structured outputs from complex joins
- **Mix approaches**: Use both full state and extraction as needed

## Use Cases

The join functionality is useful for:

1. **Parallel Processing**: Split work across multiple paths and combine results
2. **Fan-out/Fan-in**: Distribute work and collect all results before proceeding
3. **Synchronization**: Ensure multiple async operations complete before continuing
4. **Data Aggregation**: Collect and merge data from multiple sources
5. **Workflow Coordination**: Coordinate between different workflow branches

## Advanced Usage

### Count-based Joins

Wait for any 2 out of 3 paths:

```go
Join: &workflow.JoinConfig{
    Count: 2,  // Wait for any 2 paths to complete
    PathMappings: map[string]string{
        "fast_path":   "timing.fast",     // Nested under timing category
        "medium_path": "timing.medium",   // Organize by performance tier
        "slow_path":   "timing.slow",     // Clear hierarchical grouping
    },
}
```

### Conditional Joins

Combine with conditions for complex logic:

```go
{
    Name: "conditional_join",
    Join: &workflow.JoinConfig{
        Paths: []string{"critical_path", "optional_path"},
        PathMappings: map[string]string{
            "critical_path": "results.critical",  // Nested under results
            "optional_path": "results.optional",  // Organized structure
        },
    },
    Next: []*workflow.Edge{
        {Step: "success_step", Condition: "$(results.critical.status == 'success')"},
        {Step: "failure_step", Condition: "$(results.critical.status != 'success')"},
    },
}
```

This join functionality makes the workflow library powerful for complex parallel processing scenarios while keeping the variable handling predictable and explicit! 