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

// EnhancedPrintActivity demonstrates the new WorkflowContext approach
type EnhancedPrintActivity struct{}

func NewEnhancedPrintActivity() workflow.Activity {
	return workflow.NewTypedWorkflowActivity(&EnhancedPrintActivity{})
}

func (a *EnhancedPrintActivity) Name() string {
	return "enhanced_print"
}

// Execute demonstrates the cleaner approach with direct state access
func (a *EnhancedPrintActivity) Execute(ctx workflow.WorkflowContext, params PrintInput) (string, error) {
	message := fmt.Sprintf(params.Message, params.Args...)
	
	// Direct access to logger - much cleaner!
	logger := ctx.GetLogger()
	logger.Info("printing message", 
		"step", ctx.GetStepName(),
		"path", ctx.GetPathID(),
		"message", message)
	
	// Direct state access - increment print counter
	counter, exists := ctx.GetVariable("print_count")
	if !exists {
		counter = 0
	}
	
	// Type assertion with fallback
	printCount, ok := counter.(int)
	if !ok {
		printCount = 0
	}
	
	// Increment and store back directly
	newCount := printCount + 1
	ctx.SetVariable("print_count", newCount)
	ctx.SetVariable("last_printed_message", message)
	
	fmt.Println(message)
	
	logger.Info("message printed", "print_count", newCount)
	return message, nil
}
