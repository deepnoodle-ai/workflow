package activities

import (
	"fmt"
	"io"
	"os"

	"github.com/deepnoodle-ai/workflow"
)

// PrintInput defines the input parameters for the print activity
type PrintInput struct {
	Message string `json:"message"`
	Args    []any  `json:"args"`
}

// PrintActivity prints a formatted message to a configurable writer.
// The default writer is os.Stdout; tests and embedded use cases can
// pass a different writer via NewPrintActivityTo.
type PrintActivity struct {
	w io.Writer
}

// NewPrintActivity returns a print activity that writes to os.Stdout.
func NewPrintActivity() workflow.Activity {
	return NewPrintActivityTo(os.Stdout)
}

// NewPrintActivityTo returns a print activity that writes to w. If w is
// nil, os.Stdout is used.
func NewPrintActivityTo(w io.Writer) workflow.Activity {
	if w == nil {
		w = os.Stdout
	}
	return workflow.NewTypedActivity(&PrintActivity{w: w})
}

func (a *PrintActivity) Name() string {
	return "print"
}

func (a *PrintActivity) Execute(ctx workflow.Context, params PrintInput) (string, error) {
	message := fmt.Sprintf(params.Message, params.Args...)
	fmt.Fprintln(a.w, message)
	return message, nil
}
