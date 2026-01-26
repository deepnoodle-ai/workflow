# Agent Activity

`AgentActivity` wraps AI agent loops as workflow activities, providing checkpoint boundaries at tool calls for durability and recovery.

## Overview

The AgentActivity implements the workflow's `TypedActivity` interface, allowing agents to be used as workflow steps. Key features:

- **Checkpoint at tool calls** - Conversation state is checkpointed after each tool execution
- **Deterministic tool IDs** - Uses `ctx.DeterministicID()` for idempotent tool calls
- **Automatic recovery** - Resumes from checkpoint on workflow recovery
- **Event logging** - Emits reasoning events for observability

## Creating an Agent Activity

```go
agent := ai.NewAgentActivity("assistant", llmProvider, ai.AgentActivityOptions{
    SystemPrompt: "You are a helpful assistant that can search and analyze data.",
    Tools: map[string]ai.Tool{
        "search":    searchTool,
        "calculate": calcTool,
        "read_file": fileTool,
    },
    EventLog: eventLog,  // Optional: for reasoning traces
    OnToolCall: func(ctx context.Context, call ai.ToolCall) error {
        log.Printf("Tool called: %s", call.Name)
        return nil
    },
    OnToolResult: func(ctx context.Context, call ai.ToolCall, result *ai.ToolResult) error {
        log.Printf("Tool result: %v", result.Success)
        return nil
    },
})
```

## Options

### AgentActivityOptions

```go
type AgentActivityOptions struct {
    // SystemPrompt is the system message for the agent.
    SystemPrompt string

    // Tools available to the agent.
    Tools map[string]Tool

    // EventLog for recording reasoning events (optional).
    EventLog workflow.EventLog

    // OnToolCall is called before executing each tool (optional).
    OnToolCall func(ctx context.Context, call ToolCall) error

    // OnToolResult is called after executing each tool (optional).
    OnToolResult func(ctx context.Context, call ToolCall, result *ToolResult) error
}
```

## Parameters and Results

### AgentActivityParams

```go
type AgentActivityParams struct {
    // Input is the initial user message or task for the agent.
    Input string `json:"input"`

    // Checkpoint is restored conversation state from a previous execution.
    Checkpoint *ConversationState `json:"checkpoint,omitempty"`

    // MaxTurns limits the number of LLM turns (default: 10).
    MaxTurns int `json:"max_turns,omitempty"`

    // MaxTokens is the per-turn token limit (passed to LLM).
    MaxTokens int `json:"max_tokens,omitempty"`

    // Temperature controls LLM randomness.
    Temperature *float64 `json:"temperature,omitempty"`
}
```

### AgentActivityResult

```go
type AgentActivityResult struct {
    // Response is the agent's final text response.
    Response string `json:"response"`

    // Conversation is the final conversation state (for checkpointing).
    Conversation *ConversationState `json:"conversation"`

    // TurnsUsed is the number of LLM turns used.
    TurnsUsed int `json:"turns_used"`

    // TotalTokens is the total tokens used across all turns.
    TotalTokens int `json:"total_tokens"`

    // Artifacts contains any artifacts produced during execution.
    Artifacts map[string]any `json:"artifacts,omitempty"`
}
```

## Using in Workflows

### Register as Activity

```go
// Convert to workflow.Activity
activities := []workflow.Activity{
    agent.ToActivity(),
}

// Create workflow
wf, _ := workflow.New(workflow.Options{
    Name: "assistant_workflow",
    Steps: []*workflow.Step{
        {
            Name:     "ask_agent",
            Activity: agent.Name(),
            Parameters: map[string]any{
                "input": "$(inputs.question)",
            },
        },
    },
})

// Execute
execution, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:   wf,
    Activities: activities,
    Inputs:     map[string]any{"question": "What is the capital of France?"},
})
execution.Run(ctx)
```

### Chaining Agents

```go
wf, _ := workflow.New(workflow.Options{
    Name: "agent_pipeline",
    Steps: []*workflow.Step{
        {
            Name:       "research",
            Activity:   researcher.Name(),
            Parameters: map[string]any{"input": "$(inputs.topic)"},
            Store:      "research_result",
            Next:       []*workflow.Edge{{Step: "analyze"}},
        },
        {
            Name:       "analyze",
            Activity:   analyst.Name(),
            Parameters: map[string]any{"input": "$(state.research_result.response)"},
            Store:      "analysis_result",
            Next:       []*workflow.Edge{{Step: "report"}},
        },
        {
            Name:       "report",
            Activity:   writer.Name(),
            Parameters: map[string]any{"input": "$(state.analysis_result.response)"},
        },
    },
})
```

## Execution Flow

1. **Initialize or restore** - If checkpoint exists, restore conversation state
2. **Generate response** - Call LLM with messages and tools
3. **Check for tool calls** - If LLM requests tools, execute them
4. **Checkpoint** - After each tool call, save conversation state
5. **Loop** - Continue until LLM stops or max turns reached
6. **Return** - Return final response and conversation state

```
┌─────────────────┐
│ Initialize/     │
│ Restore         │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Generate        │◄──────────────┐
│ Response        │               │
└────────┬────────┘               │
         │                        │
         ▼                        │
    ┌────────────┐                │
    │ Tool Calls?│                │
    └──────┬─────┘                │
      No   │   Yes                │
           ▼                      │
    ┌─────────────┐               │
    │ Execute     │               │
    │ Tools       │               │
    └──────┬──────┘               │
           │                      │
           ▼                      │
    ┌─────────────┐               │
    │ Checkpoint  │───────────────┘
    └─────────────┘
         │
         ▼ (No more tool calls)
┌─────────────────┐
│ Return Result   │
└─────────────────┘
```

## Recovery

On workflow recovery, the agent resumes from checkpoint:

1. Conversation state is restored from params
2. DurableTool caches are restored from conversation artifacts
3. Execution continues from where it left off
4. Same tool calls return cached results (idempotency)

## Hooks

### OnToolCall

Called before each tool execution:

```go
OnToolCall: func(ctx context.Context, call ai.ToolCall) error {
    // Log the tool call
    log.Printf("Calling tool: %s with args: %v", call.Name, call.Arguments)

    // Optionally block certain tools
    if call.Name == "dangerous_tool" {
        return errors.New("tool not allowed")
    }

    return nil
}
```

### OnToolResult

Called after each tool execution:

```go
OnToolResult: func(ctx context.Context, call ai.ToolCall, result *ai.ToolResult) error {
    // Log the result
    if result.Success {
        log.Printf("Tool %s succeeded", call.Name)
    } else {
        log.Printf("Tool %s failed: %s", call.Name, result.Error)
    }

    // Track metrics
    metrics.ToolCallCompleted(call.Name, result.Success)

    return nil
}
```

## Best Practices

1. **Set appropriate max_turns** - Prevent runaway agents with reasonable limits

2. **Use EventLog for observability** - Enable debugging and auditing of agent reasoning

3. **Implement tool timeouts** - Ensure tools have reasonable timeouts to prevent hanging

4. **Handle errors gracefully** - Tools should return structured errors in ToolResult.Error

5. **Monitor token usage** - Track TotalTokens to stay within budget and context limits
