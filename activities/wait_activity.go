package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// WaitParams defines the parameters for the wait activity
type WaitParams struct {
	Duration interface{} `mapstructure:"duration"`
}

// WaitResult defines the result of the wait activity
type WaitResult struct {
	Message  string        `json:"message"`
	Duration time.Duration `json:"duration"`
}

// WaitActivity handles delays (replaces "wait" step type)
type WaitActivity struct{}

func NewWaitActivity() workflow.Activity {
	return workflow.NewTypedActivity(&WaitActivity{})
}

func (a *WaitActivity) Name() string {
	return "wait"
}

func (a *WaitActivity) Execute(ctx context.Context, params WaitParams) (WaitResult, error) {
	var duration time.Duration
	var err error

	if params.Duration == nil {
		return WaitResult{}, fmt.Errorf("wait activity requires 'duration' parameter")
	}

	// Check for duration parameter (new format)
	switch v := params.Duration.(type) {
	case string:
		duration, err = time.ParseDuration(v)
		if err != nil {
			return WaitResult{}, fmt.Errorf("invalid duration format: %w", err)
		}
	case time.Duration:
		duration = v
	case float64:
		// Handle seconds as float
		duration = time.Duration(v * float64(time.Second))
	default:
		return WaitResult{}, fmt.Errorf("duration must be string, time.Duration, or float64 (seconds)")
	}

	if duration <= 0 {
		return WaitResult{
			Message:  "no delay specified",
			Duration: 0,
		}, nil
	}

	select {
	case <-ctx.Done():
		return WaitResult{}, ctx.Err()
	case <-time.After(duration):
		return WaitResult{
			Message:  fmt.Sprintf("waited %s", duration),
			Duration: duration,
		}, nil
	}
}
