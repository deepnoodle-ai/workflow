package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StepID identifies one attempt at one unit of work within a run. It is
// stable and readable:
//
//	<path>/<step>[@<visit>][[<index>]]#<attempt>
//
// For example main/verify[3]#1 is the first attempt at item 3 of verify on
// the main path, and main.tests/lint@2#1 is the first attempt at the second
// time the tests path reached lint. on_failure steps run on the path
// failure and finally steps on the path finally (failure/page#1).
//
// A StepID is unique within its run, not across runs. It names two keys a
// host needs:
//
//   - The attempt, the whole StepID: the key for the job and the step
//     record of this attempt. A retry is a new attempt with a new StepID.
//   - The unit, [StepID.Unit]: the same for every attempt at the unit, so
//     a retry keeps it. Combined with the run ID, it is the idempotency
//     key for side effects outside the host, such as a payment provider's
//     idempotency key: an attempt that charged a card and then timed out
//     must not charge it again on the retry.
type StepID string

// Unit is the StepID without its attempt: the same for every attempt at
// the unit of work (main/charge#1 and main/charge#2 are both main/charge).
// A new visit of the step in a loop, or another each item, is another unit.
func (id StepID) Unit() string {
	node, index, _, ok := id.parse()
	if !ok {
		return ""
	}
	if index >= 0 {
		return fmt.Sprintf("%s[%d]", node, index)
	}
	return node
}

// Step is the name of the definition step the StepID is an attempt at.
func (id StepID) Step() string {
	node, _, _, ok := id.parse()
	if !ok {
		return ""
	}
	_, step, _ := strings.CutLast(node, "/")
	step, _, _ = strings.Cut(step, "@")
	return step
}

// Attempt is the attempt number, from 1, or 0 for a malformed StepID.
func (id StepID) Attempt() int {
	_, _, attempt, _ := id.parse()
	return attempt
}

func makeStepID(node string, index, attempt int) StepID {
	if index >= 0 {
		return StepID(fmt.Sprintf("%s[%d]#%d", node, index, attempt))
	}
	return StepID(fmt.Sprintf("%s#%d", node, attempt))
}

// parse splits a StepID into its node key, item index (-1 for none) and
// attempt. It accepts only the canonical form, so a StepID Apply matches
// is the one it emits in commands.
func (id StepID) parse() (node string, index, attempt int, ok bool) {
	s, n, found := strings.CutLast(string(id), "#")
	if !found {
		return "", 0, 0, false
	}
	attempt, err := strconv.Atoi(n)
	if err != nil || attempt < 1 {
		return "", 0, 0, false
	}
	index = -1
	if rest, ok := strings.CutSuffix(s, "]"); ok {
		s, n, found = strings.CutLast(rest, "[")
		if !found {
			return "", 0, 0, false
		}
		index, err = strconv.Atoi(n)
		if err != nil || index < 0 {
			return "", 0, 0, false
		}
	}

	// Only the form makeStepID writes names an attempt: no leading zeros
	// or signs, and a node key with a path and a step.
	path, step, found := strings.CutLast(s, "/")
	if !found || path == "" || step == "" || makeStepID(s, index, attempt) != id {
		return "", 0, 0, false
	}
	return s, index, attempt, true
}

// EventKind is what happened to a run.
type EventKind string

const (
	EventStart         EventKind = "start"
	EventStepCompleted EventKind = "step_completed"
	EventStepFailed    EventKind = "step_failed"
	EventTimerFired    EventKind = "timer_fired"
	EventCancel        EventKind = "cancel"
)

// Event is one input to [Apply]. At is the only clock Apply reads; an At
// earlier than the state's UpdatedAt is treated as UpdatedAt.
type Event struct {
	At   time.Time `json:"at"`
	Kind EventKind `json:"kind"`

	// Start. Input is the run's input, read as `input`.
	Input map[string]any `json:"input,omitempty"`

	// StepCompleted, StepFailed, TimerFired. Apply takes a StepID. A host's
	// Deliver also takes the name of a step (approve) and resolves it with
	// [Resolve] before it applies and stores the event, so the event log
	// holds the full StepID.
	StepID StepID     `json:"step_id,omitempty"`
	Output any        `json:"output,omitempty"` // StepCompleted
	Error  *StepError `json:"error,omitempty"`  // StepFailed

	// Cancel. Reason is why. A cancel decides the run's outcome, so a
	// second cancel, such as a redelivery of the first, changes nothing:
	// the finally steps it started run to the end. Force also stops the
	// finally steps, or skips them if they have not started; each is then
	// reported in FinallyErrors with type cancelled.
	Reason string `json:"reason,omitempty"`
	Force  bool   `json:"force,omitempty"`
}

// CommandKind is what the host must do.
type CommandKind string

const (
	// CommandDispatchStep asks the host to run a step attempt, not before
	// NotBefore, and report the result as StepCompleted or StepFailed.
	CommandDispatchStep CommandKind = "dispatch_step"
	// CommandCancelStep tells the host the attempt's result is no longer
	// wanted. The host should stop the work if it can and drop any pending
	// timer for the StepID. A late result is ignored.
	CommandCancelStep CommandKind = "cancel_step"
	// CommandScheduleTimer asks the host to deliver TimerFired for StepID
	// at NotBefore. A step's timeout timer has its attempt's StepID and is
	// obsolete once the attempt settles: the host may drop it, and a
	// TimerFired for a settled attempt is ignored.
	CommandScheduleTimer CommandKind = "schedule_timer"
	// CommandRunCompleted, CommandRunFailed and CommandRunCancelled say the
	// run has finished: its outcome is decided and its on_failure and
	// finally steps are done.
	CommandRunCompleted CommandKind = "run_completed"
	CommandRunFailed    CommandKind = "run_failed"
	CommandRunCancelled CommandKind = "run_cancelled"
)

// Command is one output of [Apply]. Hosts persist commands in the same
// transaction as the state and carry them out after commit.
type Command struct {
	Kind     CommandKind `json:"kind"`
	StepID   StepID      `json:"step_id,omitempty"`
	Step     string      `json:"step,omitempty"`      // definition step name
	StepKind string      `json:"step_kind,omitempty"` // "agent", "action", ...
	// Params is the step's with: block after evaluation.
	Params map[string]any `json:"params,omitempty"`
	// Config is the step's kind configuration, unevaluated.
	Config map[string]any `json:"config,omitempty"`
	// Item and Index are the element of an each step. Index is -1 for a
	// step without each.
	Item      any       `json:"item,omitempty"`
	Index     int       `json:"index"`
	Attempt   int       `json:"attempt,omitempty"`
	NotBefore time.Time `json:"not_before,omitzero"`
	// OutputSchema is the step's output schema, if it has one, for a
	// host that asks a model for structured output. Apply validates the
	// output against it.
	OutputSchema *Schema `json:"output_schema,omitempty"`

	Outputs map[string]any `json:"outputs,omitempty"` // RunCompleted
	Error   *RunError      `json:"error,omitempty"`   // RunFailed
	Reason  string         `json:"reason,omitempty"`  // RunCancelled
	// FinallyErrors are the finally steps that failed or did not run, on
	// any terminal command. They never change the outcome; hosts should
	// surface them (alert, log) because cleanup did not finish.
	FinallyErrors []*RunError `json:"finally_errors,omitempty"`
}
