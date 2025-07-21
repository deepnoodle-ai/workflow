package activities

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// WaitActivity handles delays (replaces "wait" step type)
type WaitActivity struct{}

func (a *WaitActivity) Name() string {
	return "wait"
}

func (a *WaitActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	var duration time.Duration
	var err error

	// Check for duration parameter (new format)
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
		return nil, errors.New("wait activity requires 'duration' parameter")
	}

	if duration <= 0 {
		return "no delay specified", nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(duration):
		return fmt.Sprintf("waited %s", duration), nil
	}
}
