package activities

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// GetTimeParams defines the parameters for the get time activity (none required)
type GetTimeParams struct {
	Format string `mapstructure:"format"` // Optional time format
}

// GetTimeResult defines the result of the get time activity
type GetTimeResult struct {
	Time      time.Time `json:"time"`
	Formatted string    `json:"formatted"`
	Unix      int64     `json:"unix"`
}

// GetTimeActivity implements getting the current time
type GetTimeActivity struct{}

func NewGetTimeActivity() workflow.Activity {
	return workflow.NewTypedActivity(&GetTimeActivity{})
}

func (a *GetTimeActivity) Name() string {
	return "time.now"
}

func (a *GetTimeActivity) Execute(ctx context.Context, params GetTimeParams) (GetTimeResult, error) {
	now := time.Now()
	format := params.Format
	if format == "" {
		format = time.RFC3339
	}

	return GetTimeResult{
		Time:      now,
		Formatted: now.Format(format),
		Unix:      now.Unix(),
	}, nil
}
