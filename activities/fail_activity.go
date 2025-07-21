package activities

import (
	"context"
	"fmt"
)

// FailActivity implements a configurable failure for testing
type FailActivity struct{}

func NewFailActivity() *FailActivity {
	return &FailActivity{}
}

func (a *FailActivity) Name() string {
	return "fail"
}

func (a *FailActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	message, ok := params["message"].(string)
	if !ok {
		message = "intentional failure for testing"
	}
	return nil, fmt.Errorf("fail activity: %s", message)
}
