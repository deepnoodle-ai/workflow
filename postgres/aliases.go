package postgres

import "github.com/deepnoodle-ai/workflow/durable"

// The types, constants and errors this package shares with every durable
// host backend. Each is the durable package's own, so code written against
// package durable takes these values as they are.

// NewRun describes a run to start. See [durable.NewRun].
type NewRun = durable.NewRun

// Result is what an event did to a run. Its terminal commands are reported
// to WithOnFinished and RunsChannel. See [durable.Result].
type Result = durable.Result

// Finished describes a run that has just reached a terminal status. See
// [durable.Finished].
type Finished = durable.Finished

// SQLTx is a database/sql transaction on the store's Postgres database. The
// supported driver is pgx's stdlib (github.com/jackc/pgx/v5/stdlib). See
// [durable.SQLTx].
type SQLTx = durable.SQLTx

// Run is a run as stored. See [durable.Run].
type Run = durable.Run

// RunFilter selects the runs ListRuns returns. See [durable.RunFilter].
type RunFilter = durable.RunFilter

// RunPage is one page of ListRuns. See [durable.RunPage].
type RunPage = durable.RunPage

// RunSummary is a run as ListRuns returns it. See [durable.RunSummary].
type RunSummary = durable.RunSummary

// StepStatus is what became of a step attempt. See [durable.StepStatus].
type StepStatus = durable.StepStatus

// StepRecord is one step attempt as stored. See [durable.StepRecord].
type StepRecord = durable.StepRecord

// JobRef names one job in the outbox. See [durable.JobRef].
type JobRef = durable.JobRef

// Execution is what [Worker.Execute] did. See [durable.Execution].
type Execution = durable.Execution

// Observer is the set of functions a Worker calls as it claims and ends
// jobs. See [durable.Observer].
type Observer = durable.Observer

// Settlement describes a recorded job. See [durable.Settlement].
type Settlement = durable.Settlement

// Outcome is how a recorded job ended. See [durable.Outcome].
type Outcome = durable.Outcome

// QueueStat describes the jobs of one queue. See [durable.QueueStat].
type QueueStat = durable.QueueStat

const (
	StepDispatched = durable.StepDispatched
	StepCompleted  = durable.StepCompleted
	StepFailed     = durable.StepFailed
	StepCancelled  = durable.StepCancelled

	OutcomeCompleted = durable.OutcomeCompleted
	OutcomeFailed    = durable.OutcomeFailed
	OutcomeDeferred  = durable.OutcomeDeferred
	OutcomeFired     = durable.OutcomeFired

	// EventAborted: see [durable.EventAborted].
	EventAborted = durable.EventAborted
	// ErrorTypeDefinitionUnavailable: see
	// [durable.ErrorTypeDefinitionUnavailable].
	ErrorTypeDefinitionUnavailable = durable.ErrorTypeDefinitionUnavailable
)

var (
	ErrRunNotFound = durable.ErrRunNotFound
	ErrRunExists   = durable.ErrRunExists
	// ErrUnstorable: see [durable.ErrUnstorable].
	ErrUnstorable = durable.ErrUnstorable
	// ErrDefinitionUnavailable: see [durable.ErrDefinitionUnavailable].
	ErrDefinitionUnavailable = durable.ErrDefinitionUnavailable
	// ErrNotClaimable: see [durable.ErrNotClaimable].
	ErrNotClaimable = durable.ErrNotClaimable
	// ErrHardStop: see [durable.ErrHardStop].
	ErrHardStop = durable.ErrHardStop
)
