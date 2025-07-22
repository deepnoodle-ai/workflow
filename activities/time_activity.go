package activities

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// TimeInput defines the input parameters for the get time activity
type TimeInput struct {
	UTC bool `json:"utc"`
}

// TimeActivity can be used to get the current time
type TimeActivity struct{}

func NewTimeActivity() workflow.Activity {
	return workflow.NewTypedActivity(&TimeActivity{})
}

func (a *TimeActivity) Name() string {
	return "time"
}

func (a *TimeActivity) Execute(ctx context.Context, params TimeInput) (time.Time, error) {
	if params.UTC {
		return time.Now().UTC(), nil
	}
	return time.Now(), nil
}
