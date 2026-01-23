// Package main demonstrates a simple agent running as a workflow activity.
// This shows the "Workflow ABOVE Agents" perspective where the workflow
// orchestrates agent activities.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/ai"
	"github.com/deepnoodle-ai/workflow/ai/tools"
)

func main() {
	ctx := context.Background()
	logger := slog.Default()

	// Create a mock LLM provider for demonstration
	// In production, use ai.NewDiveLLMProvider with a real LLM
	llm := &MockLLMProvider{model: "mock-model"}

	// Create tools for the agent
	agentTools := map[string]ai.Tool{
		"read_file":  tools.NewFileReadTool(tools.FileReadToolOptions{}),
		"list_files": tools.NewFileListTool(tools.FileListToolOptions{}),
	}

	// Create the agent activity
	agent := ai.NewAgentActivity("file_assistant", llm, ai.AgentActivityOptions{
		SystemPrompt: "You are a helpful file assistant. Help users explore and understand files.",
		Tools:        agentTools,
	})

	// Create a workflow that uses the agent
	wf, err := workflow.New(workflow.Options{
		Name:        "file_assistant_workflow",
		Description: "A workflow that uses an AI agent to help with files",
		Steps: []*workflow.Step{
			{
				Name:     "ask_agent",
				Activity: agent.Name(),
			},
		},
	})
	if err != nil {
		log.Fatalf("failed to create workflow: %v", err)
	}

	// Create activity list with the agent
	activities := []workflow.Activity{
		agent.ToActivity(),
	}

	// Create and run execution
	execution, err := workflow.NewExecution(workflow.ExecutionOptions{
		Workflow:   wf,
		Activities: activities,
		Inputs: map[string]any{
			"input": "What files are in the current directory?",
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

	fmt.Printf("Workflow completed!\n")
	fmt.Printf("Status: %s\n", execution.Status())
}

// MockLLMProvider is a simple mock for demonstration purposes.
// In real usage, you would use ai.NewDiveLLMProvider with a real LLM.
type MockLLMProvider struct {
	model string
}

func (m *MockLLMProvider) Name() string  { return "mock" }
func (m *MockLLMProvider) Model() string { return m.model }

func (m *MockLLMProvider) Generate(ctx context.Context, messages []ai.Message, opts ai.GenerateOptions) (*ai.GenerateResponse, error) {
	// For demonstration, return a simple response
	return &ai.GenerateResponse{
		Content:    "I'll help you list the files. Let me check the current directory.",
		StopReason: ai.StopReasonToolUse,
		ToolCalls: []ai.ToolCall{
			{
				ID:        "call_1",
				Name:      "list_files",
				Arguments: map[string]any{"path": "."},
			},
		},
		Usage: ai.Usage{InputTokens: 10, OutputTokens: 20},
	}, nil
}
