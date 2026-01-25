// Package execution contains the internal execution runtime for workflows.
// This package is not intended for direct use by clients - use the Engine
// or Client packages instead.
//
// The execution package provides:
//   - Execution: The runtime that executes workflow steps
//   - Path: An execution path through a workflow (for branching)
//   - ExecutionState: Consolidated state management
//   - Context implementation for activities
package execution
