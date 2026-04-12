// Package worker turns the in-process workflow engine into a durable,
// queue-backed runner. It provides:
//
//   - A small QueueStore interface describing the persistence contract
//     a backing store must satisfy (claim, heartbeat, complete, reap).
//   - A Worker that drives a claim loop, a heartbeat goroutine, and a
//     reaper against any QueueStore implementation.
//   - A Handler interface that the consumer implements to turn a
//     claimed Spec blob into an executed [github.com/deepnoodle-ai/workflow.Execution].
//
// The worker package is intentionally transport-agnostic: it knows
// nothing about SQL, HTTP, multitenancy, billing, or workflow types.
// All of those concerns live either in the QueueStore implementation
// (persistence) or in the Handler (domain logic).
//
// Pair this package with github.com/deepnoodle-ai/workflow/postgres
// for a Postgres-backed store and checkpointer, or provide your own
// QueueStore.
package worker
