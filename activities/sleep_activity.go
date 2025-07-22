package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// SleepParams defines the parameters for the sleep activity
type SleepParams struct {
	Duration interface{} `mapstructure:"duration"`
}

// SleepResult defines the result of the sleep activity
type SleepResult struct {
	Message  string        `json:"message"`
	Duration time.Duration `json:"duration"`
}

// SleepActivity implements a configurable sleep/delay
type SleepActivity struct{}

func NewSleepActivity() workflow.Activity {
	return workflow.NewTypedActivity(&SleepActivity{})
}

func (a *SleepActivity) Name() string {
	return "sleep"
}

func (a *SleepActivity) Execute(ctx context.Context, params SleepParams) (SleepResult, error) {
	var duration time.Duration
	var err error

	if params.Duration == nil {
		return SleepResult{}, fmt.Errorf("duration parameter is required")
	}

	switch v := params.Duration.(type) {
	case string:
		duration, err = time.ParseDuration(v)
		if err != nil {
			return SleepResult{}, fmt.Errorf("invalid duration format: %w", err)
		}
	case time.Duration:
		duration = v
	case float64:
		// Handle seconds as float
		duration = time.Duration(v * float64(time.Second))
	default:
		return SleepResult{}, fmt.Errorf("duration must be string, time.Duration, or float64 (seconds)")
	}

	if duration <= 0 {
		return SleepResult{}, fmt.Errorf("duration must be positive")
	}

	select {
	case <-ctx.Done():
		return SleepResult{}, ctx.Err()
	case <-time.After(duration):
		return SleepResult{
			Message:  fmt.Sprintf("slept for %s", duration),
			Duration: duration,
		}, nil
	}
}
