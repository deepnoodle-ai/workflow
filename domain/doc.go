// Package domain contains the core business types and repository interfaces
// for the workflow engine.
//
// This package is the heart of the workflow system and follows clean architecture:
//   - Contains pure domain logic with no external dependencies
//   - Defines repository interfaces that infrastructure packages implement
//   - Other packages depend on domain, not the other way around
//
// # Core Types
//
// Execution types:
//   - ExecutionRecord: Represents a workflow execution instance
//   - ExecutionStatus: The state of an execution (pending, running, completed, etc.)
//   - ExecutionFilter: Criteria for listing executions
//
// Task types:
//   - TaskRecord: A unit of work for workers to execute
//   - TaskSpec: Defines what a worker should execute
//   - TaskResult: The result reported by a worker
//   - TaskClaimed: Information returned when a worker claims a task
//
// Event types:
//   - Event: Workflow events for observability
//   - EventType: The type of event (workflow_started, step_completed, etc.)
//   - EventFilter: Criteria for listing events
//
// # Repository Interfaces
//
//   - ExecutionRepository: CRUD operations for executions
//   - TaskRepository: Task lifecycle including claiming and completion
//   - EventRepository: Event append and query operations
//   - Store: Composite interface combining all repositories
//
// # Runner Interface
//
//   - Runner: Converts activity parameters to TaskSpec and interprets results
//   - InlineExecutor: Optional interface for runners that can execute in-process
package domain
