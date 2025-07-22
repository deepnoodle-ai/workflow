package activities

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/workflow"
)

// PromptParams defines the parameters for the prompt activity
type PromptParams struct {
	Prompt string `mapstructure:"prompt"`
}

// PromptResult defines the result of the prompt activity
type PromptResult struct {
	Response string `json:"response"`
	Prompt   string `json:"prompt"`
}

// PromptActivity handles user prompts (replaces "prompt" step type)
type PromptActivity struct{}

func NewPromptActivity() workflow.Activity {
	return workflow.NewTypedActivity(&PromptActivity{})
}

func (a *PromptActivity) Name() string {
	return "Prompt"
}

func (a *PromptActivity) Execute(ctx context.Context, params PromptParams) (PromptResult, error) {
	if params.Prompt == "" {
		return PromptResult{}, fmt.Errorf("prompt activity requires 'prompt' parameter")
	}

	// This is a placeholder - actual implementation would depend on the UI/CLI interface
	response := fmt.Sprintf("Prompt: %s (response would come from user interaction)", params.Prompt)
	return PromptResult{
		Response: response,
		Prompt:   params.Prompt,
	}, nil
}
