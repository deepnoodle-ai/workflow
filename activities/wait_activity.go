package activities

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// WaitInput defines the input parameters for the wait activity
type WaitInput struct {
	Duration float64 `json:"duration"`
}

// WaitOutput defines the output of the wait activity
type WaitOutput struct{}

// WaitActivity can be used to wait for a duration
type WaitActivity struct{}

func NewWaitActivity() workflow.Activity {
	return workflow.NewTypedActivity(&WaitActivity{})
}

func (a *WaitActivity) Name() string {
	return "wait"
}

func (a *WaitActivity) Execute(ctx context.Context, params WaitInput) (WaitOutput, error) {
	duration := time.Duration(params.Duration * float64(time.Second))

	if duration <= 0 {
		return WaitOutput{}, nil
	}

	select {
	case <-ctx.Done():
		return WaitOutput{}, ctx.Err()
	case <-time.After(duration):
		return WaitOutput{}, nil
	}
}
