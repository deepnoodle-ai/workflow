# Workflow Automation Library

## Overview

This Go library provides a workflow automation framework that enables the
definition and execution of complex, multi-step processes with support for
parallel execution, conditional branching, and failure recovery. The design
emphasizes simplicity over complexity while maintaining the power needed for
sophisticated automation scenarios.

## Goals

- **Simplicity**: Easier to use than complex workflow engines
- **Resumability**: Support for checkpoint-based recovery from failures
- **Parallel Execution**: Native support for concurrent workflow paths
- **Extensibility**: Pluggable activity system for to support new operations
- **Observability**: Built-in logging and operation tracking for monitoring

## Architecture

### Core Components

#### 1. Workflows

Workflows define the structure and flow of automation processes. They consist of:

- **Steps**: Individual operations or decision points
- **Inputs/Outputs**: Typed parameters for workflow execution

#### 2. Executions

Runtime instances of workflows that track:

- **Global State**: Workflow inputs and outputs
- **Paths**: Multiple concurrent execution branches with isolated state
- **Activities**: Logged units of work
- **Checkpoints**: Serializable snapshots for recovery

#### 3. Execution Paths

Parallel execution branches that enable:

- **Concurrent Processing**: Multiple workflow paths running simultaneously
- **Dynamic Branching**: Creation of new paths with copied parent state
- **Path Isolation**: Each path maintains its own independent state variables

## Key Design Decisions

### Checkpoint-Based Recovery

Rather than full event sourcing, the library uses periodic checkpoints to
capture execution state. This provides resumability without the complexity of
event replay while maintaining acceptable recovery granularity.

### Risor Scripting Integration

The library integrates Risor for safe script evaluation with different security
contexts for deterministic vs. non-deterministic operations, enabling powerful
workflow logic while maintaining predictability.

### Path-Local State Management

Execution paths use a path-local state model similar to AWS Step Functions:
- Each path owns its state variables independently 
- When paths branch, child paths receive a copy of parent's current state
- No shared state between parallel paths avoids race conditions
