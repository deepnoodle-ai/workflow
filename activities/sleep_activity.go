package activities

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SleepActivity implements a configurable sleep/delay
type SleepActivity struct{}

func NewSleepActivity() *SleepActivity {
	return &SleepActivity{}
}

func (a *SleepActivity) Name() string {
	return "sleep"
}

func (a *SleepActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	var duration time.Duration
	var err error

	if durationParam, ok := params["duration"]; ok {
		switch v := durationParam.(type) {
		case string:
			duration, err = time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("invalid duration format: %w", err)
			}
		case time.Duration:
			duration = v
		case float64:
			// Handle seconds as float
			duration = time.Duration(v * float64(time.Second))
		default:
			return nil, fmt.Errorf("duration must be string, time.Duration, or float64 (seconds)")
		}
	} else {
		return nil, errors.New("either duration or seconds parameter is required")
	}

	if duration <= 0 {
		return nil, errors.New("duration must be positive")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(duration):
		return fmt.Sprintf("slept for %s", duration), nil
	}
}
