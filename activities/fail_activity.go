package activities

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// FailInput defines the input parameters for the fail activity
type FailInput struct {
	Message string `json:"message"`
}

// FailOutput defines the output of the fail activity
type FailOutput struct{}

// FailActivity can be used to fail the workflow
type FailActivity struct{}

func NewFailActivity() workflow.Activity {
	return workflow.NewTypedActivity(&FailActivity{})
}

func (a *FailActivity) Name() string {
	return "fail"
}

func (a *FailActivity) Execute(ctx context.Context, params FailInput) (FailOutput, error) {
	message := params.Message
	if message == "" {
		message = "intentional failure for testing"
	}
	return FailOutput{}, fmt.Errorf("fail activity: %s", message)
}
