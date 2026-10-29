package durable

import (
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// JobRef names one job in the outbox: a step attempt, or a timer. A host
// that pushes jobs to a task queue (such as Cloud Tasks) sends JobRefs, as
// JSON, and passes each one to the worker's Execute. The run ID, StepID and
// Timer identify the job; StepKind and RunAt say where and when to run it.
type JobRef struct {
	RunID  string          `json:"run_id"`
	StepID workflow.StepID `json:"step_id"`
	// Timer is true for a timer (a sleep, or a step's timeout), false for
	// a step attempt. A timer and the attempt it guards share a StepID.
	Timer bool `json:"timer,omitempty"`
	// StepKind is the step's kind. Route a step to a service that has a
	// handler for it; any worker fires a timer.
	StepKind string `json:"step_kind,omitempty"`
	// RunAt is when the job is due. Zero, or a time past, means at once.
	// Push it with this schedule time; Execute does not run a job early.
	RunAt time.Time `json:"run_at,omitzero"`
}

// Jobs returns a reference to each job the event wrote to the outbox: one
// per DispatchStep and ScheduleTimer in Commands. Push them after the
// transaction commits; for a Tx method, after you commit. A duplicate or
// late event wrote none.
func (r *Result) Jobs() []JobRef {
	if r == nil {
		return nil
	}
	var refs []JobRef
	for _, c := range r.Commands {
		if c.Kind != workflow.CommandDispatchStep && c.Kind != workflow.CommandScheduleTimer {
			continue
		}
		refs = append(refs, JobRef{
			RunID:    r.RunID,
			StepID:   c.StepID,
			Timer:    c.Kind == workflow.CommandScheduleTimer,
			StepKind: c.StepKind,
			RunAt:    c.NotBefore,
		})
	}
	return refs
}

// Execution is what Execute did.
type Execution struct {
	// Settled is true when this call ran the job and recorded what came
	// of it: a result, a failure, a hand-off, or a failed run. False means
	// there was nothing to do: the job is gone (done, cancelled, its run
	// finished), or another worker took it over.
	Settled bool
	// Jobs are the jobs the job's result wrote: its run's next steps and
	// timers. Push them, as for [Result.Jobs].
	Jobs []JobRef
}
