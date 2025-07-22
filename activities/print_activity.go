package activities

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// PrintParams defines the parameters for the print activity
type PrintParams struct {
	Message interface{} `mapstructure:"message"`
}

// PrintResult defines the result of the print activity
type PrintResult struct {
	Success bool `json:"success"`
}

// PrintActivity implements a typed print activity
type PrintActivity struct{}

func NewPrintActivity() workflow.Activity {
	return workflow.NewTypedActivity(&PrintActivity{})
}

func (a *PrintActivity) Name() string {
	return "print"
}

func (a *PrintActivity) Execute(ctx context.Context, params PrintParams) (PrintResult, error) {
	if params.Message == nil {
		return PrintResult{Success: false}, fmt.Errorf("print activity requires 'message' parameter")
	}
	
	fmt.Println(params.Message)
	return PrintResult{Success: true}, nil
}
