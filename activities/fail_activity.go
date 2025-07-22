package activities

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// FailParams defines the parameters for the fail activity
type FailParams struct {
	Message string `mapstructure:"message"`
}

// FailResult defines the result of the fail activity (never returned due to error)
type FailResult struct {
	// This will never be returned since the activity always fails
}

// FailActivity implements a configurable failure for testing
type FailActivity struct{}

func NewFailActivity() workflow.Activity {
	return workflow.NewTypedActivity(&FailActivity{})
}

func (a *FailActivity) Name() string {
	return "fail"
}

func (a *FailActivity) Execute(ctx context.Context, params FailParams) (FailResult, error) {
	message := params.Message
	if message == "" {
		message = "intentional failure for testing"
	}
	return FailResult{}, fmt.Errorf("fail activity: %s", message)
}
