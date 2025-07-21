package activities

import (
	"context"
	"errors"
	"fmt"
)

type PrintActivity struct{}

func NewPrintActivity() *PrintActivity {
	return &PrintActivity{}
}

func (a *PrintActivity) Name() string {
	return "print"
}

func (a *PrintActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	message, ok := params["message"]
	if !ok {
		return nil, errors.New("print activity requires 'message' parameter")
	}
	fmt.Println(message)
	return nil, nil
}
