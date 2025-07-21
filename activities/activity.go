package activities

import (
	"context"
	"errors"
	"fmt"
)

// PromptActivity handles user prompts (replaces "prompt" step type)
type PromptActivity struct{}

func (a *PromptActivity) Name() string {
	return "Prompt"
}

func (a *PromptActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	prompt, ok := params["prompt"].(string)
	if !ok {
		return nil, errors.New("prompt activity requires 'prompt' parameter")
	}

	// This is a placeholder - actual implementation would depend on the UI/CLI interface
	return fmt.Sprintf("Prompt: %s (response would come from user interaction)", prompt), nil
}
