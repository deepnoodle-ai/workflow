// Package main demonstrates a multi-agent workflow where multiple agents
// work together in a pipeline. This shows agent composition using workflows.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/ai"
)

func main() {
	ctx := context.Background()
	logger := slog.Default()

	// Create mock LLM providers for each agent
	// In production, these could be different models or same model with different prompts
	analyzerLLM := &MockLLMProvider{model: "analyzer", response: "Analysis: The code has a potential null pointer issue."}
	fixerLLM := &MockLLMProvider{model: "fixer", response: "Fixed: Added null check before dereferencing."}
	reviewerLLM := &MockLLMProvider{model: "reviewer", response: "Review: The fix looks good. Approved."}

	// Create the three agents for our pipeline
	analyzer := ai.NewAgentActivity("analyzer", analyzerLLM, ai.AgentActivityOptions{
		SystemPrompt: "You are a code analyzer. Analyze the given code for issues.",
	})

	fixer := ai.NewAgentActivity("fixer", fixerLLM, ai.AgentActivityOptions{
		SystemPrompt: "You are a code fixer. Fix the issues identified in the analysis.",
	})

	reviewer := ai.NewAgentActivity("reviewer", reviewerLLM, ai.AgentActivityOptions{
		SystemPrompt: "You are a code reviewer. Review the proposed fix and approve or request changes.",
	})

	// Create a workflow that chains the agents
	// Each step uses Next to define the sequence
	wf, err := workflow.New(workflow.Options{
		Name:        "code_review_pipeline",
		Description: "Multi-agent code review pipeline",
		Inputs: []*workflow.Input{
			{Name: "code", Type: "string", Description: "Code to review"},
		},
		Steps: []*workflow.Step{
			{
				Name:       "analyze",
				Activity:   analyzer.Name(),
				Parameters: map[string]any{"input": "$(inputs.code)"},
				Store:      "analysis",
				Next:       []*workflow.Edge{{Step: "fix"}},
			},
			{
				Name:       "fix",
				Activity:   fixer.Name(),
				Parameters: map[string]any{"input": "$(state.analysis.response)"},
				Store:      "fix_result",
				Next:       []*workflow.Edge{{Step: "review"}},
			},
			{
				Name:       "review",
				Activity:   reviewer.Name(),
				Parameters: map[string]any{"input": "$(state.fix_result.response)"},
				Store:      "review_result",
			},
		},
	})
	if err != nil {
		log.Fatalf("failed to create workflow: %v", err)
	}

	// Create activity list
	activities := []workflow.Activity{
		analyzer.ToActivity(),
		fixer.ToActivity(),
		reviewer.ToActivity(),
	}

	// Create and run execution
	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:   wf,
		Activities: activities,
		Inputs: map[string]any{
			"code": `func process(data *Data) {
				result := data.Value  // potential null pointer
				fmt.Println(result)
			}`,
		},
		Logger: logger,
	})
	if err != nil {
		log.Fatalf("failed to create execution: %v", err)
	}

	// Run the workflow
	if err := execution.Run(ctx); err != nil {
		log.Fatalf("workflow failed: %v", err)
	}

	fmt.Printf("Multi-agent pipeline completed!\n")
	fmt.Printf("Status: %s\n", execution.Status())
}

// MockLLMProvider is a simple mock that returns a configured response.
type MockLLMProvider struct {
	model    string
	response string
}

func (m *MockLLMProvider) Name() string  { return "mock" }
func (m *MockLLMProvider) Model() string { return m.model }

func (m *MockLLMProvider) Generate(ctx context.Context, messages []ai.Message, opts ai.GenerateOptions) (*ai.GenerateResponse, error) {
	return &ai.GenerateResponse{
		Content:    m.response,
		StopReason: ai.StopReasonEndTurn,
		Usage:      ai.Usage{InputTokens: 10, OutputTokens: 20},
	}, nil
}
