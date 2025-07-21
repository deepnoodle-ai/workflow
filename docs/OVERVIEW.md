# Workflow Automation Library

## Overview

This Go library provides a workflow automation framework that enables the definition and execution of complex, multi-step processes with support for parallel execution, conditional branching, and failure recovery. The design emphasizes simplicity over complexity while maintaining the power needed for sophisticated automation scenarios.

## Goals

- **Simplicity**: Easier to use than complex workflow engines like Temporal, while still providing essential enterprise features
- **Resumability**: Support for checkpoint-based recovery from failures without the complexity of full event sourcing
- **Parallel Execution**: Native support for concurrent workflow paths to maximize throughput
- **Extensibility**: Pluggable action system for integrating external operations
- **Determinism**: Predictable execution through operation tracking and state management
- **Observability**: Built-in logging and operation tracking for debugging and monitoring

## Architecture

### Core Components

#### 1. Workflows
Workflows define the structure and flow of automation processes. They consist of:
- **Steps**: Individual operations or decision points
- **Graph**: The topology connecting steps with conditional edges
- **Inputs/Outputs**: Typed parameters for workflow execution
- **Triggers**: Optional event-based activation mechanisms

#### 2. Executions
Runtime instances of workflows that track:
- **State**: Workflow variables and intermediate results
- **Paths**: Multiple concurrent execution branches
- **Operations**: Logged units of work for deterministic behavior
- **Checkpoints**: Serializable snapshots for recovery

#### 3. Steps
Individual workflow operations supporting:
- **Actions**: Pluggable external operations (API calls, file operations, etc.)
- **Scripts**: Risor-based scripting with safe evaluation contexts
- **Conditionals**: Dynamic branching based on workflow state
- **Loops**: "Each" blocks for iterating over collections

#### 4. Execution Paths
Parallel execution branches that enable:
- **Concurrent Processing**: Multiple workflow paths running simultaneously
- **Dynamic Branching**: Creation of new paths based on conditions
- **Path Coordination**: Synchronization and state sharing between paths

## Current Implementation Status

### ✅ Implemented Features

**Core Framework**
- Workflow definition and validation
- Step-based execution model
- Graph topology with conditional edges
- Execution state management
- Parallel path execution

**Operations & Logging**
- Deterministic operation IDs
- Operation logging with timing and parameters
- Null and file-based operation loggers

**Checkpointing & Recovery**
- JSON-serializable execution state
- File-based checkpoint persistence
- Automatic checkpoint creation
- Failure recovery with path reset

**Scripting & Templating**
- Risor script integration with safe evaluation contexts
- Template variable interpolation (`${expression}`)
- Support for deterministic and non-deterministic script contexts
- Built-in function access with safety controls

**Control Flow**
- Conditional branching with script-based conditions
- Loop constructs via "each" blocks
- Path creation and management
- Step result storage in workflow state

**Actions System**
- Pluggable action interface
- Built-in actions (Time.Now, Print)
- Parameter template evaluation
- Action registry for extension

### 🚧 Areas Needing Refinement

**Error Handling**
- Retry logic partially implemented but not fully integrated
- Error propagation and recovery strategies need formalization
- Timeout handling for long-running operations

**State Management**
- State serialization format standardization
- State migration strategies for workflow evolution
- Cross-path state synchronization patterns

**Testing Infrastructure**
- Comprehensive test coverage for edge cases
- Integration testing for complex workflows
- Performance testing for high-throughput scenarios

**Documentation & Tooling**
- API documentation and examples
- Workflow definition utilities
- Debugging and monitoring tools

## Example Usage

```go
// Define a workflow
workflow, err := workflow.New(workflow.Options{
    Name: "data-processing",
    Steps: []*workflow.Step{
        workflow.NewStep(workflow.StepOptions{
            Name:   "fetch-data",
            Type:   "action",
            Action: "http.get",
            Parameters: map[string]any{
                "url": "${inputs.source_url}",
            },
            Store: "raw_data",
            Next: []*workflow.Edge{{Step: "process-data"}},
        }),
        workflow.NewStep(workflow.StepOptions{
            Name:   "process-data",
            Type:   "script",
            Script: `processed = transform(state.raw_data)`,
            Store:  "processed_data",
            Next: []*workflow.Edge{{Step: "save-results"}},
        }),
        workflow.NewStep(workflow.StepOptions{
            Name:   "save-results",
            Type:   "action", 
            Action: "file.write",
            Parameters: map[string]any{
                "path": "${inputs.output_path}",
                "data": "${state.processed_data}",
            },
        }),
    },
})

// Execute the workflow
execution, err := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow: workflow,
    Inputs: map[string]interface{}{
        "source_url":  "https://api.example.com/data",
        "output_path": "/tmp/results.json",
    },
})

err = execution.Run(context.Background())
```

## Key Design Decisions

### Checkpoint-Based Recovery
Rather than full event sourcing, the library uses periodic checkpoints to capture execution state. This provides resumability without the complexity of event replay while maintaining acceptable recovery granularity.

### Risor Scripting Integration
The library integrates Risor for safe script evaluation with different security contexts for deterministic vs. non-deterministic operations, enabling powerful workflow logic while maintaining predictability.

### Path-Based Parallelism
Execution paths provide a clean abstraction for parallel processing, allowing workflows to fork and merge execution branches dynamically while maintaining state isolation and coordination.

### Operation Tracking
Every non-deterministic operation is logged with deterministic IDs, enabling debugging, monitoring, and potential replay scenarios without full event sourcing overhead.

## Next Steps

See [ROADMAP.md](ROADMAP.md) for detailed development priorities and milestones for evolving this into a production-ready workflow automation library. 