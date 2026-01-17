package workflow

import (
	"fmt"
	"time"
)

// TimerActivity is an activity that waits for a specified duration before completing.
// The deadline is stored in the activity parameters so it survives workflow recovery.
// On recovery, the timer resumes with the remaining time.
type TimerActivity struct {
	name     string
	duration time.Duration
}

// NewTimerActivity creates a new timer activity with the given name and duration.
// The timer will wait for the specified duration before completing.
// If the workflow is recovered mid-timer, it will resume with the remaining time.
func NewTimerActivity(name string, duration time.Duration) *TimerActivity {
	return &TimerActivity{
		name:     name,
		duration: duration,
	}
}

// Name returns the activity name.
func (t *TimerActivity) Name() string {
	return t.name
}

// Execute waits for the timer to elapse.
// On first execution, it computes the deadline from the current time.
// On recovery, it loads the deadline from params and waits for the remaining time.
func (t *TimerActivity) Execute(ctx Context, params map[string]any) (any, error) {
	deadlineKey := "timer_deadline"

	// Get or set the deadline
	var deadline time.Time
	if deadlineVal, ok := params[deadlineKey]; ok {
		// Deadline was checkpointed - load it
		switch v := deadlineVal.(type) {
		case time.Time:
			deadline = v
		case string:
			// Handle JSON deserialization of time
			parsed, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, fmt.Errorf("failed to parse timer deadline: %w", err)
			}
			deadline = parsed
		default:
			return nil, fmt.Errorf("unexpected deadline type: %T", deadlineVal)
		}
	} else {
		// First execution - compute deadline
		deadline = ctx.Clock().Now().Add(t.duration)
		// Store deadline in params for checkpointing
		// Note: the workflow system should checkpoint this value
		params[deadlineKey] = deadline.Format(time.RFC3339Nano)
	}

	// Calculate remaining time
	remaining := deadline.Sub(ctx.Clock().Now())
	if remaining <= 0 {
		// Already elapsed
		return map[string]any{
			"elapsed":  true,
			"deadline": deadline.Format(time.RFC3339Nano),
		}, nil
	}

	// Wait for the timer
	select {
	case <-ctx.Clock().After(remaining):
		return map[string]any{
			"elapsed":  true,
			"deadline": deadline.Format(time.RFC3339Nano),
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SleepActivity is a simple timer activity that just sleeps for the duration specified in params.
// This is useful when you want to specify the duration at runtime via step parameters.
type SleepActivity struct{}

// NewSleepActivity creates a new sleep activity.
// Usage in workflow:
//
//	b.Activity("wait", "sleep", map[string]any{"duration": "5s"})
func NewSleepActivity() *SleepActivity {
	return &SleepActivity{}
}

// Name returns "sleep".
func (s *SleepActivity) Name() string {
	return "sleep"
}

// Execute sleeps for the duration specified in params["duration"].
// The duration can be a time.Duration or a string parseable by time.ParseDuration.
func (s *SleepActivity) Execute(ctx Context, params map[string]any) (any, error) {
	durationVal, ok := params["duration"]
	if !ok {
		return nil, fmt.Errorf("sleep activity requires 'duration' parameter")
	}

	var duration time.Duration
	switch v := durationVal.(type) {
	case time.Duration:
		duration = v
	case string:
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", v, err)
		}
		duration = parsed
	case float64:
		// Handle JSON number (seconds)
		duration = time.Duration(v * float64(time.Second))
	case int:
		// Handle integer (seconds)
		duration = time.Duration(v) * time.Second
	default:
		return nil, fmt.Errorf("invalid duration type: %T", durationVal)
	}

	// Create an internal timer activity with the parsed duration
	timer := NewTimerActivity("sleep_internal", duration)
	return timer.Execute(ctx, params)
}
