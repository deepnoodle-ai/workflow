# Workflow Library API Review

This document provides a comprehensive review of the current public-facing API of the Workflow library. The goal is to identify "rough spots," areas of confusion, and opportunities to move the library toward production-ready status while remaining idiomatic to Go.

## 1. Executive Summary

The library has a solid foundation with clear core concepts (Workflow, Step, Activity, Execution). It successfully implements advanced features like checkpointing, signals, and replay-safe activity history. The `Runner` abstraction is a major strength for production usage.

However, the API surface is currently "Options-heavy" and can feel verbose for simple use cases. There are also several overlapping configuration structs that could be consolidated.

## 2. Core Recommendations

### 2.1 Refine and Consolidate Options Structs

Currently, a developer must interact with `workflow.Options`, `workflow.ExecutionOptions`, `workflow.RunOptions`, and `workflow.RunnerConfig`. This creates friction.

**Recommendation:**
- **Consolidate Execution/Run Options:** Many fields in `ExecutionOptions` (like `Logger`) are also present in `RunnerConfig` or `RunOptions`. We should define a clearer boundary between "Infrastructure" (Registry, Checkpointer, SignalStore) and "Instance Data" (Workflow, Inputs, ExecutionID).
- **Functional Options:** Adopt the functional options pattern for `NewExecution` and `NewRunner` to provide sensible defaults and reduce the "struct wall."

```go
// Proposed NewExecution signature
exec, err := workflow.NewExecution(wf, inputs, 
    workflow.WithExecutionID("my-id"),
    workflow.WithCheckpointer(myCp),
)
```

### 2.2 Introduce an Activity Registry Type

Passing a slice of `Activity` objects to every `NewExecution` call is tedious and inefficient for workers that handle many workflows.

**Recommendation:**
- Create a dedicated `ActivityRegistry` type that can be initialized once and shared across executions.
- Allow global or scoped registries.

```go
registry := workflow.NewRegistry()
registry.Register(activities.NewPrintActivity())
registry.RegisterFunc("my_op", myOpFunc)

exec, _ := workflow.NewExecution(wf, inputs, workflow.WithRegistry(registry))
```

### 2.3 Fluent Workflow Builder API

Defining workflows using large nested structs is great for JSON/YAML serialization but cumbersome in Go code.

**Recommendation:**
- Add a builder API to allow for more readable, type-safe workflow definitions in code.

```go
wf := workflow.Define("my-workflow").
    Step("Fetch").
        Activity("fetch-data").
        Store("records").
        Next("Process").
    Step("Process").
        Activity("process-data").
        Parameters(map[string]any{"input": "${state.records}"}).
    Build()
```

### 2.4 Activity API Ergonomics

The current naming `NewActivityFunction` and `NewTypedActivityFunction` is quite verbose.

**Recommendation:**
- Simplify to `workflow.ActivityFunc` and `workflow.TypedActivityFunc`.
- Consider if we can use generics even more effectively to reduce the need for the "Typed" prefix everywhere.

### 2.5 Execution Result Helpers

`ExecutionResult` is a great abstraction, but it could be more helpful.

**Recommendation:**
- Add helper methods like `result.OutputInt("count")` or `result.OutputString("status")` to handle type assertion and error checking for common types.
- Add `result.WaitReason()` to quickly see why an execution suspended without digging into `SuspensionInfo`.

### 2.6 Consistent State Access Naming

The "state." prefix handling is currently inconsistent between docs and implementation (sometimes stripped, sometimes required).

**Recommendation:**
- Standardize on a single convention. Suggestion: `Store` fields should implicitly target state variables, so the "state." prefix should be optional or omitted entirely in the definition, but consistently handled by the engine.

### 2.7 Step "God Object" Refactoring

The `Step` struct contains fields for every possible type of step (Activity, Join, Wait, Sleep, Each, Pause). This makes the API "wide."

**Recommendation:**
- While maintaining JSON compatibility is important, we could introduce specialized constructors or a "type" field to clarify the intent of a step.
- Internally, consider a `StepAction` interface that separates the logic of a `Join` from an `Activity`.

## 3. Conceptual Adjustments

### 3.1 Signals vs. Activities
The "Wait" functionality is powerful but has two different APIs: `WaitSignal` (step) and `workflow.Wait` (activity). 

**Recommendation:**
- Ensure these two share the exact same underlying logic (which they mostly do via `WaitState`). 
- Better document when to use which. `WaitSignal` is better for high-level process flow; `workflow.Wait` is better for low-level protocol coordination inside an activity.

### 3.2 "Path" Concept Clarity
The concept of "Paths" is central to the library's branching model but can be confusing to newcomers.

**Recommendation:**
- Rename "Path" to "Branch" or "Thread" in the public documentation? "Branch" is more standard for DAGs, while "Path" is very specific to this library's implementation.
- If sticking with "Path," ensure it's clearly defined as an "independent execution thread with its own state."

## 4. Go Idiomaticity & Quality Pass

### 4.1 Interface Narrowing
- `VariableContainer` is a bit generic. Consider if it should be internal or if its methods should be directly on `Context`.

### 4.2 Error Handling
- The library uses `fmt.Errorf` with `%w` correctly.
- Ensure all sentinel errors are exported and follow the `ErrXxx` naming convention. (e.g., `ErrNoCheckpoint` is good).

### 4.3 Documentation
- The `llms.txt` and `README.md` are excellent. 
- Recommendation: Add a "Production Checklist" to the docs (Checkpointer, ActivityLogger, SignalStore, Heartbeat).

## 5. Summary of Proposed Action Plan

1.  **Phase 1: Ergonomics (Low effort, high impact)**
    - Simplify Activity registration naming.
    - Implement `ActivityRegistry` type.
    - Add helper methods to `ExecutionResult`.

2.  **Phase 2: Refinement (Medium effort)**
    - Implement Functional Options for `NewExecution` and `NewRunner`.
    - Unify `Store` variable naming conventions.

3.  **Phase 3: Expansion (High effort)**
    - Implement the Fluent Builder API.
    - Refactor `Step` into a more polymorphic structure while maintaining JSON compatibility.

---
*Review concluded. Ready for production-readiness implementation.*
