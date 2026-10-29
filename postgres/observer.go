package postgres

import (
	"context"
	"errors"
	"fmt"
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
	// from the outbox. It fires after the transaction commits.
	Settled func(job workflow.Job, s Settlement)
	// Cancelled: the run no longer wants the attempt, so the worker dropped
	// it. The step was cancelled or timed out, the run ended or was
	// deleted, the result arrived through Store.Deliver, or the run
	// rejected the result. The job is gone and does not run again.
	Cancelled func(job workflow.Job)
	// LeaseLost: the worker lost its claim, and the job stays in the outbox
	// for another claim. Another worker claimed it, no heartbeat succeeded
	// for a whole lease, the result could not be recorded before the lease
	// ran out, or a database error made the worker leave the job to its
	// lease. The attempt can run again.
	LeaseLost func(job workflow.Job)
	// GivenBack: on shutdown, the worker gave the claim back without
	// counting it toward MaxClaims.
	GivenBack func(job workflow.Job)
	// Abandoned: the job was claimed more than MaxClaims times. The worker
	// recorded the step as failed with type abandoned, or failed the run
	// (type abandoned or definition_unavailable).
	Abandoned func(job workflow.Job)
	// ClaimError: a claim query failed. It does not fire for an error
	// caused by shutdown.
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

// errJobGone is a lost lease whose job no longer exists: the run no longer
// wants the attempt. It matches errLeaseLost, so code that only needs to
// know the claim is over treats both alike.
var errJobGone = fmt.Errorf("%w: the job is gone", errLeaseLost)

// lostClaim says why a fenced write by claim t changed no row: errJobGone
// when the job no longer exists, else errLeaseLost (a newer claim holds it).
func (w *Worker) lostClaim(ctx context.Context, q conn, t claimedJob) error {
	var exists bool
	err := q.QueryRow(ctx, w.Store.sql(`SELECT EXISTS (SELECT 1 FROM {{schema}}.jobs WHERE id = $1)`),
		t.ID).Scan(&exists)
	if err == nil && !exists {
		return errJobGone
	}
	return errLeaseLost
}

func (w *Worker) observeClaimed(t claimedJob) {
	if f := w.Observer.Claimed; f != nil {
		f(t.Job)
	}
}

// observeSettled fires the ending event of a job whose event was recorded:
// Abandoned for the failure that abandons a step, else Settled.
func (w *Worker) observeSettled(t claimedJob, ev workflow.Event) {
	if ev.Kind == workflow.EventStepFailed && t.Claims > w.MaxClaims {
		w.observeAbandoned(t)
		return
	}
	f := w.Observer.Settled
	if f == nil {
		return
	}
	s := Settlement{Duration: t.ran}
	switch ev.Kind {
	case workflow.EventTimerFired:
		s.Outcome = OutcomeFired
	case workflow.EventStepFailed:
		s.Outcome, s.Error = OutcomeFailed, ev.Error
	default:
		s.Outcome = OutcomeCompleted
	}
	f(t.Job, s)
}

func (w *Worker) observeDeferred(t claimedJob) {
	if f := w.Observer.Settled; f != nil {
		f(t.Job, Settlement{Outcome: OutcomeDeferred, Duration: t.ran})
	}
}

// observeDropped fires the ending event of a claim that ended without a
// recorded result: Cancelled when the job or its run is gone, else
// LeaseLost.
func (w *Worker) observeDropped(t claimedJob, err error) {
	if errors.Is(err, errJobGone) || errors.Is(err, ErrRunNotFound) {
		w.observeCancelled(t)
		return
	}
	if f := w.Observer.LeaseLost; f != nil {
		f(t.Job)
	}
}

func (w *Worker) observeCancelled(t claimedJob) {
	if f := w.Observer.Cancelled; f != nil {
		f(t.Job)
	}
}

func (w *Worker) observeAbandoned(t claimedJob) {
	if f := w.Observer.Abandoned; f != nil {
		f(t.Job)
	}
}
