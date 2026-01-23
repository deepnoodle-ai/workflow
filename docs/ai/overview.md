# AI-Native Workflow Extensions

The `ai/` package extends the workflow engine with AI-native execution patterns, enabling durable, recoverable agent-based systems.

## Design Philosophy

The AI extensions are built on four key principles:

1. **Build on existing interfaces** - No changes to the core engine
2. **Support all three perspectives** - Orchestration, cognitive, and tool-invocation patterns
3. **Incremental adoption** - Each component usable independently
4. **Leverage existing patterns** - Timer's deadline-in-params, EventLog's flexible Data field, Context's DeterministicID

## Three Perspectives

### 1. Workflow ABOVE Agents

Workflows orchestrate agents as activities. The workflow controls the overall flow, and agents are invoked as steps.

```go
wf, _ := workflow.New(workflow.Options{
    Steps: []*workflow.Step{
        {Name: "analyze", Activity: "analyzer_agent"},
        {Name: "fix", Activity: "fixer_agent"},
        {Name: "review", Activity: "reviewer_agent"},
    },
})
```

**Use cases:**
- Multi-agent pipelines
- Human-in-the-loop workflows
- Orchestrated AI assistants

### 2. Workflow = Agent

The workflow IS the agent's cognitive loop. Each step represents a cognitive operation.

```go
wf, _ := workflow.New(workflow.Options{
    Steps: []*workflow.Step{
        {Name: "perceive", Activity: "observe"},
        {Name: "reason", Activity: "think"},
        {Name: "act", Activity: "respond"},
    },
})
```

**Use cases:**
- Cognitive architectures
- ReAct-style agents
- Custom reasoning loops

### 3. Workflow BELOW Agents

Agents invoke workflows as tools. Complex operations are encapsulated as durable workflows.

```go
workflowTool := ai.NewWorkflowTool(dataWorkflow, engine, ai.WorkflowToolOptions{
    Name:        "process_data",
    Description: "Process data through a durable workflow",
})

agent := ai.NewAgentActivity("orchestrator", llm, ai.AgentActivityOptions{
    Tools: map[string]ai.Tool{"process_data": workflowTool},
})
```

**Use cases:**
- Complex tool operations
- Durable sub-tasks
- Isolated processing pipelines

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                      AI Extensions                              │
│  ┌────────────────┐  ┌───────────────┐  ┌─────────────────────┐ │
│  │ AgentActivity  │  │  DurableTool  │  │  WorkflowTool       │ │
│  │  (Execution)   │  │  (Idempotent) │  │  (Integration)      │ │
│  └───────┬────────┘  └──────┬────────┘  └────────┬────────────┘ │
│          │                  │                    │              │
│  ┌───────┴──────────────────┴────────────────────┴────────────┐ │
│  │                    LLMProvider                              │ │
│  │         (Dive, OpenAI, Anthropic adapters)                  │ │
│  └─────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
           │                  │                    │
           ▼                  ▼                    ▼
    ┌─────────────┐    ┌─────────────┐    ┌──────────────────┐
    │  Workflow   │    │  EventLog   │    │   Sprites        │
    │  Engine     │    │  (Traces)   │    │   (Isolation)    │
    └─────────────┘    └─────────────┘    └──────────────────┘
```

## Quick Start

### Basic Agent Activity

```go
// Create LLM provider (using Dive)
llm := ai.NewDiveLLMProvider(diveLLM, ai.DiveLLMProviderOptions{
    Model: "claude-3-opus",
})

// Create tools
tools := map[string]ai.Tool{
    "search": searchTool,
    "calculate": calcTool,
}

// Create agent activity
agent := ai.NewAgentActivity("assistant", llm, ai.AgentActivityOptions{
    SystemPrompt: "You are a helpful assistant.",
    Tools:        tools,
})

// Use in workflow
wf, _ := workflow.New(workflow.Options{
    Name: "assistant_workflow",
    Steps: []*workflow.Step{
        {Name: "ask", Activity: agent.Name()},
    },
})

// Execute
execution, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:   wf,
    Activities: []workflow.Activity{agent.ToActivity()},
    Inputs:     map[string]any{"input": "Help me analyze this data"},
})
execution.Run(ctx)
```

## Package Structure

```
workflow/ai/
├── conversation.go       # ConversationState, Message types
├── llm.go               # LLMProvider interface
├── dive_provider.go     # Dive LLM adapter
├── agent_activity.go    # AgentActivity implementation
├── durable_tool.go      # Tool interface, DurableTool
├── workflow_tool.go     # WorkflowTool for agent->workflow
├── reasoning.go         # Event types for reasoning traces
├── reasoning_callbacks.go # ReasoningCallbacks
├── sprite_environment.go # Sprites isolation for agents
└── tools/               # Built-in tools
    ├── file_tool.go
    ├── http_tool.go
    └── script_tool.go
```

## Related Documentation

- [Conversation State](./conversation-state.md) - Message handling and checkpointing
- [Agent Activity](./agent-activity.md) - Running agents as workflow activities
- [Tools](./tools.md) - Tool interface and built-in tools
- [LLM Providers](./llm-providers.md) - LLM integration
- [Reasoning Events](./reasoning-events.md) - Observability for AI agents
- [Sprites Integration](./sprites-integration.md) - Isolated agent execution
