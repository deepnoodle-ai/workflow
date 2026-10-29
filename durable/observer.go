package durable

import (
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// Observer is a set of optional functions the worker calls as it claims and
// ends jobs, to feed the application's metrics. Set only the ones you need;
// the worker skips a nil function.
//
// Each claimed job fires Claimed, then exactly one ending event: Settled,
// Cancelled, LeaseLost, GivenBack or Abandoned. The one exception is a claim
// postponed because the run's definition cannot be loaded: it ends with no
// event, and the job comes back as a new claim. So Claimed minus the ending
// events is not a count of the work in flight.
//
// The functions run synchronously on the worker's goroutines, concurrently
// with each other, so they must be fast and safe for concurrent use. A slow
// Claimed delays the handler and its first heartbeat; a slow ending event
// holds one of the worker's Concurrency slots. The worker does not recover
// a panic in a function. Do not modify the job.
type Observer struct {
	// Claimed: the worker claimed the job. It fires before the worker loads
	// the run's definition or calls the handler. job.Claims counts this
	// claim; more than one means the job was claimed before.
	Claimed func(job workflow.Job)
	// Settled: the worker recorded the job's result and removed the job
	// from the outbox. It fires after the transaction commits. A handler
	// that returned workflow.ErrDeferred fires it with OutcomeDeferred even
	// when the run removed its job while this claim's lease was live: the
	// hand-off happened, and the result may already have arrived through
	// Store.Deliver. Its job found gone after the lease is LeaseLost.
	Settled func(job workflow.Job, s Settlement)
	// Cancelled: the run no longer wants the attempt, so the worker dropped
	// it. The step was cancelled or timed out, the run ended or was
	// deleted, the result arrived through Store.Deliver while the handler
	// ran, or the run rejected the result. The job was removed while this
	// claim's lease was live, and it does not run again. A handler that
	// returned workflow.ErrDeferred fires Settled instead.
	Cancelled func(job workflow.Job)
	// LeaseLost: the worker lost its claim, so another claim runs the
	// attempt again or has already run it. Another worker claimed the job
	// (and may have settled it: a job found gone after this claim's lease
	// ran out counts here), no heartbeat succeeded for a whole lease, the
	// result could not be recorded before the lease ran out, or a database
	// error made the worker leave the job to its lease.
	LeaseLost func(job workflow.Job)
	// GivenBack: on shutdown (Shutdown or a hard stop), or when the claim
	// gate closed while the claim query ran, the worker gave the claim back
	// without counting it toward MaxClaims. It fires too when the context
	// given to RunOnce or Execute ends (a request deadline) and the job is
	// given back; that claim counts toward MaxClaims.
	GivenBack func(job workflow.Job)
	// Abandoned: the worker gave up on the job. A step or timer fires it on
	// claim MaxClaims+1: the worker recorded the step as failed with type
	// abandoned, or failed the run as abandoned. A run whose definition
	// cannot be loaded fires it on claim MaxClaims, when the worker fails
	// the run as definition_unavailable.
	Abandoned func(job workflow.Job)
	// ClaimError: a claim query failed. It does not fire for an error
	// caused by shutdown, such as a hung claim query that Shutdown cancels
	// a second after its deadline.
	ClaimError func(err error)
}

// Settlement describes a recorded job.
type Settlement struct {
	Outcome Outcome
	// Duration is how long the handler ran, from its call to its return.
	// It is zero for a timer.
	Duration time.Duration
	// Error is the recorded step error, for OutcomeFailed only.
	Error *workflow.StepError
}

// Outcome is how a recorded job ended.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed" // the step's output was recorded
	OutcomeFailed    Outcome = "failed"    // the step's error was recorded
	OutcomeDeferred  Outcome = "deferred"  // the handler handed the work off
	OutcomeFired     Outcome = "fired"     // a timer fired
)

// QueueStat describes the jobs of one queue: the jobs workers claim
// together, all timers or the steps of one kind.
type QueueStat struct {
	// Kind is workflow.CommandDispatchStep for a queue of steps, or
	// workflow.CommandScheduleTimer for the timer queue.
	Kind workflow.CommandKind
	// StepKind is the kind of the queue's steps ("agent", "action", ...).
	// It is empty for the timer queue, which holds timers of every kind.
	StepKind string
	// Due counts the jobs that can be claimed now: their time has come and
	// no worker holds them under a live lease. A job whose lease expired
	// is due again.
	Due int
	// OldestDue is how long the oldest due job has been due, by the
	// store's clock. It is zero when Due is zero.
	OldestDue time.Duration
}
