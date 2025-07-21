package activities

import (
	"context"
	"time"
)

// GetTimeActivity implements getting the current time
type GetTimeActivity struct{}

func NewGetTimeActivity() *GetTimeActivity {
	return &GetTimeActivity{}
}

func (a *GetTimeActivity) Name() string {
	return "time.now"
}

func (a *GetTimeActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	return time.Now().Format(time.RFC3339), nil
}
