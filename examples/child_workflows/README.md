# Child Workflows Example

This example demonstrates the new child workflow functionality,
inspired by AWS Step Functions' nested workflow capabilities.

## What This Example Shows

### Core Features
1. **Parent-Child Workflow Orchestration**: A main workflow that orchestrates multiple child workflows
2. **Synchronous Execution**: Parent workflow waits for child workflows to complete
3. **Data Flow**: Seamless data passing between parent and child workflows
4. **State Integration**: Child workflow results are integrated into parent workflow state
5. **Workflow Registry**: Managing multiple workflow definitions in a centralized registry

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

- **Purpose**: Manages multiple workflow definitions
- **Implementation**: In-memory registry for this example
- **Usage**: Register child workflows that can be called by name

### 2. ChildWorkflowExecutor 

- **Purpose**: Manages child workflow execution lifecycle
- **Features**: Synchronous and asynchronous execution patterns
- **Integration**: Works with existing activity system

### 3. ChildWorkflowActivity

- **Purpose**: Activity that executes child workflows
- **Activity Name**: `"workflow.child"`
- **Parameters**: 
  - `workflow_name`: Name of child workflow to execute
  - `sync`: Boolean for synchronous vs asynchronous execution
  - `timeout`: Optional timeout duration
  - `inputs`: Data to pass to child workflow
