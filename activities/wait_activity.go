package activities

import (
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// WaitInput defines the input parameters for the wait activity
type WaitInput struct {
	Seconds float64 `json:"seconds"`
}

// WaitActivity can be used to wait for a duration
type WaitActivity struct{}

func NewWaitActivity() workflow.Activity {
	return workflow.NewTypedActivity(&WaitActivity{})
}

func (a *WaitActivity) Name() string {
	return "wait"
}

func (a *WaitActivity) Execute(ctx workflow.Context, params WaitInput) (string, error) {
	duration := time.Duration(params.Seconds * float64(time.Second))
	if duration <= 0 {
		return "done", nil
	}
	select {
	case <-ctx.Done():
		return "done", ctx.Err()
	case <-time.After(duration):
		return "done", nil
	}
}
