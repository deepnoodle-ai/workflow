// Package postgres provides a Postgres-backed Store that implements
// the four interfaces the workflow engine and its worker need:
//
//   - [github.com/deepnoodle-ai/workflow/experimental/worker.QueueStore] — the run
//     queue, claim, heartbeat, reaper, and terminal state.
//   - [github.com/deepnoodle-ai/workflow.Checkpointer] — via
//     [Store.NewCheckpointer], lease-fenced checkpoint persistence
//     per claimed run.
//   - [github.com/deepnoodle-ai/workflow.StepProgressStore] — step
//     progress observability.
//   - [github.com/deepnoodle-ai/workflow.ActivityLogger] — activity
//     operation log.
//
// The Store keeps one table for runs (workflow_runs), one for step
// progress (workflow_step_progress), and one for activity history
// (workflow_activity_log). Schema migrations are applied idempotently
// by [Store.Migrate].
//
// All writes to workflow_runs that belong to a running execution
// fence on (claimed_by, attempt) so that a worker that has lost its
// lease cannot corrupt a newer attempt's state. Fencing failures
// surface as [github.com/deepnoodle-ai/workflow/experimental/worker.ErrLeaseLost].
package postgres
