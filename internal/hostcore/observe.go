package hostcore

import (
	"errors"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/durable"
)

func (w *Worker) observeClaimed(t Claimed) {
	if f := w.Observer.Claimed; f != nil {
		f(t.Job)
	}
}

// observeSettled fires the ending event of a job whose event was recorded:
// Abandoned for the failure that abandons a step, else Settled.
func (w *Worker) observeSettled(t Claimed, ev workflow.Event) {
	if ev.Kind == workflow.EventStepFailed && t.Claims > w.MaxClaims {
		w.observeAbandoned(t)
		return
	}
	f := w.Observer.Settled
	if f == nil {
		return
	}
	s := durable.Settlement{Duration: t.ran}
	switch ev.Kind {
	case workflow.EventTimerFired:
		s.Outcome = durable.OutcomeFired
	case workflow.EventStepFailed:
		s.Outcome, s.Error = durable.OutcomeFailed, ev.Error
	default:
		s.Outcome = durable.OutcomeCompleted
	}
	f(t.Job, s)
}

func (w *Worker) observeDeferred(t Claimed) {
	if f := w.Observer.Settled; f != nil {
		f(t.Job, durable.Settlement{Outcome: durable.OutcomeDeferred, Duration: t.ran})
	}
}

// observeDropped fires the ending event of a claim that ended without a
// recorded result: Cancelled when the job or its run is gone, else
// LeaseLost.
func (w *Worker) observeDropped(t Claimed, err error) {
	if errors.Is(err, ErrJobGone) || errors.Is(err, durable.ErrRunNotFound) {
		w.observeCancelled(t)
		return
	}
	if f := w.Observer.LeaseLost; f != nil {
		f(t.Job)
	}
}

func (w *Worker) observeCancelled(t Claimed) {
	if f := w.Observer.Cancelled; f != nil {
		f(t.Job)
	}
}

func (w *Worker) observeAbandoned(t Claimed) {
	if f := w.Observer.Abandoned; f != nil {
		f(t.Job)
	}
}
