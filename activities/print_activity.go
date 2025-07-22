package activities

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// PrintInput defines the input parameters for the print activity
type PrintInput struct {
	Message string `json:"message"`
	Args    []any  `json:"args"`
}

// PrintActivity can be used to print a message to the console
type PrintActivity struct{}

func NewPrintActivity() workflow.Activity {
	return workflow.NewTypedActivity(&PrintActivity{})
}

func (a *PrintActivity) Name() string {
	return "print"
}

func (a *PrintActivity) Execute(ctx context.Context, params PrintInput) (string, error) {
	message := fmt.Sprintf(params.Message, params.Args...)
	fmt.Println(message)
	return message, nil
}
