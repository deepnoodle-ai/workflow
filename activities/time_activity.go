package activities

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// TimeInput defines the input parameters for the get time activity
type TimeInput struct {
	Format string `json:"format"` // Optional time format
}

// TimeOutput defines the output of the get time activity
type TimeOutput struct {
	Time      time.Time `json:"time"`
	Formatted string    `json:"formatted"`
	Unix      int64     `json:"unix"`
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
	now := time.Now()
	// format := params.Format
	// if format == "" {
	// 	format = time.RFC3339
	// }
	// return TimeOutput{
	// 	Time:      now,
	// 	Formatted: now.Format(format),
	// 	Unix:      now.Unix(),
	// }, nil
	return now, nil
}
