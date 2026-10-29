package workflow

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Errors Apply returns for events the host should not have sent. A run's own
// failures (a step failed, an expression failed) never come back as errors:
// they are RunFailed commands.
var (
	ErrNotValidated       = errors.New("workflow: definition was not made by Load")
	ErrDefinitionMismatch = errors.New("workflow: state belongs to a different definition")
	ErrNotStarted         = errors.New("workflow: run has not started")
	ErrUnknownStep        = errors.New("workflow: unknown step id")
	ErrInvalidEvent       = errors.New("workflow: invalid event")
	ErrInvalidInput       = errors.New("workflow: invalid input")
)

// Error types the library assigns. Hosts choose their own types for the
// failures they report.
const (
	ErrorTypeTimeout        = "timeout"
	ErrorTypeExpression     = "expression"
	ErrorTypeOutputTooLarge = "output_too_large"
	ErrorTypeStateTooLarge  = "state_too_large"
	ErrorTypeJoin           = "join"
	ErrorTypeMaxVisits      = "max_visits"
	ErrorTypeSwitch         = "switch"
	ErrorTypeInvalidOutput  = "invalid_output"
	ErrorTypeInvalidParams  = "invalid_params"
	// ErrorTypeCancelled marks a finally step a cancel stopped or kept
	// from running, in FinallyErrors.
	ErrorTypeCancelled = "cancelled"
)

// StepError describes a failed step attempt. Hosts report one in a
// StepFailed event. Retryable says whether trying again could succeed; a
// step is retried only if it also declares retry.
type StepError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e *StepError) Error() string {
	if e.Type == "" {
		return e.Message
	}
	return e.Type + ": " + e.Message
}

func (e *StepError) value() map[string]any {
	return map[string]any{"type": e.Type, "message": e.Message}
}

// RunError is why a run failed. OnFailure is set when an on_failure step
// failed too.
type RunError struct {
	Step      string    `json:"step,omitempty"`
	StepID    StepID    `json:"step_id,omitempty"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	OnFailure *RunError `json:"on_failure,omitempty"`
	At        time.Time `json:"at"`
}

func (e *RunError) Error() string {
	var b strings.Builder
	if e.Step != "" {
		fmt.Fprintf(&b, "step %s: ", e.Step)
	}
	b.WriteString(e.Type)
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.OnFailure != nil {
		b.WriteString("; on_failure: ")
		b.WriteString(e.OnFailure.Error())
	}
	return b.String()
}

func (e *RunError) value() map[string]any {
	return map[string]any{
		"step": e.Step, "step_id": string(e.StepID),
		"type": e.Type, "message": e.Message,
	}
}

// ValidationError lists every problem found in a definition.
type ValidationError struct {
	Problems []Problem
}

// Problem is one validation finding. Step is empty for workflow-level
// problems.
type Problem struct {
	Step    string
	Message string
}

func (e *ValidationError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		if p.Step == "" {
			lines[i] = p.Message
		} else {
			lines[i] = "step " + p.Step + ": " + p.Message
		}
	}
	return "workflow: invalid definition:\n  " + strings.Join(lines, "\n  ")
}

// problems collects findings while parsing and validating.
type problems struct {
	list []Problem
}

func (p *problems) add(step, format string, args ...any) {
	p.list = append(p.list, Problem{Step: step, Message: fmt.Sprintf(format, args...)})
}

func (p *problems) err() error {
	if len(p.list) == 0 {
		return nil
	}
	slices.SortStableFunc(p.list, func(a, b Problem) int {
		return cmp.Or(strings.Compare(a.Step, b.Step), strings.Compare(a.Message, b.Message))
	})
	return &ValidationError{Problems: p.list}
}

func (p *problems) str(step, key string, v any) string {
	s, ok := v.(string)
	if !ok {
		p.add(step, "%s must be a string", key)
	}
	return s
}

func (p *problems) boolean(step, key string, v any) bool {
	b, ok := v.(bool)
	if !ok {
		p.add(step, "%s must be a boolean", key)
	}
	return b
}

// integer reads a JSON integer, reporting anything else. Past 2^53 a
// float64 no longer holds every integer, and past the int range the
// conversion is platform-dependent, so a larger value is reported and
// clamped: the checks that follow see its sign, not a wrapped value.
func (p *problems) integer(step, key string, v any) int {
	const limit = 1 << 53
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.Abs(f) > limit {
		p.add(step, "%s must be an integer", key)
	}
	return int(max(-limit, min(f, limit)))
}

func (p *problems) obj(step, key string, v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		p.add(step, "%s must be an object", key)
	}
	return m
}

func (p *problems) strList(step, key string, v any) []string {
	list, ok := v.([]any)
	if !ok {
		p.add(step, "%s must be a list of strings", key)
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			p.add(step, "%s must be a list of strings", key)
			return nil
		}
		out = append(out, s)
	}
	return out
}

func (p *problems) duration(step, key string, v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		p.add(step, "%s must be a duration string such as \"30s\"", key)
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		p.add(step, "%s: %v", key, err)
	}
	return d
}
