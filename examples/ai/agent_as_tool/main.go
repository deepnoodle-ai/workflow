// Package main demonstrates the "Workflow BELOW Agents" perspective where
// an agent can invoke workflows as tools.
//
// This is a conceptual example showing how to create a custom tool that
// wraps a workflow, allowing agents to invoke complex workflows during
// their reasoning process.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/workflow/ai"
)

func main() {
	// Create a custom tool that simulates workflow execution.
	// In production, you would use ai.NewWorkflowTool with a real Engine.
	dataProcessorTool := &CustomWorkflowTool{
		name:        "process_data",
		description: "Process data through a durable workflow pipeline",
	}

	// Create an agent with the workflow tool
	mockLLM := &MockLLMProvider{}
	agent := ai.NewAgentActivity("orchestrator", mockLLM, ai.AgentActivityOptions{
		SystemPrompt: "You are an orchestrator agent. Use the process_data tool for complex data operations.",
		Tools: map[string]ai.Tool{
			dataProcessorTool.Name(): dataProcessorTool,
		},
	})

	// Demonstrate the tool schema
	fmt.Println("=== Workflow as Tool Example ===\n")
	fmt.Printf("Agent: %s\n", agent.Name())
	fmt.Printf("\nAvailable tools:\n")
	fmt.Printf("  - %s: %s\n", dataProcessorTool.Name(), dataProcessorTool.Description())

	schema := dataProcessorTool.Schema()
	schemaJSON, _ := json.MarshalIndent(schema, "    ", "  ")
	fmt.Printf("  - Schema:\n    %s\n", schemaJSON)

	// Simulate tool execution
	fmt.Println("\n=== Simulating Tool Execution ===")
	result, _ := dataProcessorTool.Execute(context.Background(), map[string]any{
		"data": "sample input data",
	})
	fmt.Printf("Tool result: %s\n", result.Output)

	fmt.Println("\n=== Production Usage ===")
	fmt.Println("In production, use ai.NewWorkflowTool with a real workflow.Engine:")
	fmt.Println(`
    // Create workflow
    wf, _ := workflow.New(workflow.Options{...})

    // Create engine
    engine, _ := workflow.NewEngine(workflow.EngineOptions{...})

    // Create workflow tool
    tool := ai.NewWorkflowTool(wf, engine, ai.WorkflowToolOptions{
        Name:        "process_data",
        Description: "Process data through workflow",
    })

    // Add to agent
    agent := ai.NewAgentActivity("orchestrator", llm, ai.AgentActivityOptions{
        Tools: map[string]ai.Tool{tool.Name(): tool},
    })
`)
}

// CustomWorkflowTool demonstrates a custom tool that wraps workflow execution.
type CustomWorkflowTool struct {
	name        string
	description string
}

func (t *CustomWorkflowTool) Name() string        { return t.name }
func (t *CustomWorkflowTool) Description() string { return t.description }

func (t *CustomWorkflowTool) Schema() *ai.ToolSchema {
	return ai.NewObjectSchema().
		AddProperty("data", ai.StringProperty("The data to process")).
		AddRequired("data")
}

func (t *CustomWorkflowTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
	data, _ := args["data"].(string)

	// In production, this would submit to a real workflow engine
	result := map[string]any{
		"status":    "completed",
		"processed": fmt.Sprintf("Processed: %s", data),
		"metadata": map[string]any{
			"workflow": "process_data",
			"steps":    3,
		},
	}

	resultJSON, _ := json.Marshal(result)
	return &ai.ToolResult{
		Output:  string(resultJSON),
		Success: true,
	}, nil
}

// MockLLMProvider for demonstration.
type MockLLMProvider struct{}

func (m *MockLLMProvider) Name() string  { return "mock" }
func (m *MockLLMProvider) Model() string { return "mock-model" }

func (m *MockLLMProvider) Generate(ctx context.Context, messages []ai.Message, opts ai.GenerateOptions) (*ai.GenerateResponse, error) {
	return &ai.GenerateResponse{
		Content:    "I've processed your data using the workflow.",
		StopReason: ai.StopReasonEndTurn,
	}, nil
}

// Verify interface compliance.
var _ ai.Tool = (*CustomWorkflowTool)(nil)
var _ ai.LLMProvider = (*MockLLMProvider)(nil)
