package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StepID identifies one attempt at one unit of work. It is stable and
// readable:
//
//	<path>/<step>[@<visit>][[<index>]]#<attempt>
//
// For example main/verify[3]#1 is the first attempt at item 3 of verify on
// the main path, and main.tests/lint@2#1 is the second time the tests path
// reached lint. Hosts use it as the idempotency key for the work they create
// and the step record they upsert.
type StepID string

func makeStepID(node string, index, attempt int) StepID {
	if index >= 0 {
		return StepID(fmt.Sprintf("%s[%d]#%d", node, index, attempt))
	}
	return StepID(fmt.Sprintf("%s#%d", node, attempt))
}

// parse splits a StepID into its node key, item index (-1 for none) and
// attempt.
func (id StepID) parse() (node string, index, attempt int, ok bool) {
	s := string(id)
	hash := strings.LastIndexByte(s, '#')
	if hash < 0 {
		return "", 0, 0, false
	}
	attempt, err := strconv.Atoi(s[hash+1:])
	if err != nil || attempt < 1 {
		return "", 0, 0, false
	}
	s = s[:hash]
	index = -1
	if strings.HasSuffix(s, "]") {
		open := strings.LastIndexByte(s, '[')
		if open < 0 {
			return "", 0, 0, false
		}
		index, err = strconv.Atoi(s[open+1 : len(s)-1])
		if err != nil || index < 0 {
			return "", 0, 0, false
		}
		s = s[:open]
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

	// Start.
	Inputs  map[string]any `json:"inputs,omitempty"`
	Trigger map[string]any `json:"trigger,omitempty"` // read as `event`

	// StepCompleted, StepFailed, TimerFired.
	StepID StepID     `json:"step_id,omitempty"`
	Output any        `json:"output,omitempty"` // StepCompleted
	Error  *StepError `json:"error,omitempty"`  // StepFailed

	// Cancel.
	Reason string `json:"reason,omitempty"`
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
