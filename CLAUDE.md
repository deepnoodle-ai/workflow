# Workflow Engine

A Go library for building durable, recoverable workflows with an optional Engine layer for production deployments.

## Architecture Overview

The library has two layers:

1. **Core Workflow Library** - `Execution`, `Path`, `Step`, `Activity` for defining and running workflows
2. **Engine Layer** (optional) - Adds durability, bounded concurrency, crash recovery, and distributed execution

```
┌─────────────────────────────────────────────────────────────────┐
│                           ENGINE                                │
│  ┌────────────────┐  ┌───────────────┐  ┌─────────────────────┐ │
│  │ ExecutionStore │  │   WorkQueue   │  │ ExecutionEnvironment│ │
│  │   (State)      │  │   (Flow)      │  │     (Compute)       │ │
│  └───────┬────────┘  └──────┬────────┘  └────────┬────────────┘ │
└──────────┼──────────────────┼────────────────────┼──────────────┘
           │                  │                    │
           ▼                  ▼                    ▼
    ┌─────────────┐    ┌─────────────┐    ┌──────────────────┐
    │  Memory /   │    │  Memory /   │    │  Local / Remote  │
    │  Postgres   │    │  Postgres   │    │    (workers)     │
    └─────────────┘    └─────────────┘    └──────────────────┘
```

## Key Components

### ExecutionStore Interface
Source of truth for execution state. Implementations:
- `MemoryStore` - In-memory, for testing
- `PostgresStore` - PostgreSQL-backed, for production

Key methods:
- `ClaimExecution` - Fenced claiming with attempt-based tokens
- `CompleteExecution` - Fenced completion to prevent stale writes
- `Heartbeat` - Liveness tracking for running executions
- `ListStaleRunning` / `ListStalePending` - For reaper detection

### WorkQueue Interface
At-least-once delivery with lease semantics. Implementations:
- `MemoryQueue` - Channel-based, for testing
- `PostgresQueue` - PostgreSQL with `FOR UPDATE SKIP LOCKED`

Key methods:
- `Enqueue` / `Dequeue` - Add and claim work items
- `Ack` / `Nack` - Acknowledge or return items
- `Extend` - Extend lease for long-running work

### ExecutionEnvironment Interface
Where workflows run. Implementations:
- `LocalEnvironment` - In-process execution (blocking)
- Future: `SpritesEnvironment` - Remote execution via Sprites

### Clock Interface
Abstraction for time operations, enabling deterministic testing:
- `RealClock` - Uses system time (production)
- `FakeClock` - Manually controlled time (testing)

### EventLog Interface
Observability layer for workflow events:
- `MemoryEventLog` - In-memory, for testing
- `PostgresEventLog` - PostgreSQL-backed, for production

## Engine Features

### Recovery Modes
- `RecoveryResume` - Resume orphaned executions from checkpoint
- `RecoveryFail` - Mark orphaned executions as failed

### Reaper Loop
Background goroutine that detects and recovers:
- Stale running executions (missed heartbeats)
- Stale pending executions (dispatched but never claimed)

### Fenced Operations
All claiming and completion uses attempt-based fencing to prevent:
- Double-claiming of executions
- Stale workers overwriting newer attempts

## Context Helpers

The `workflow.Context` provides deterministic helpers:
- `Now()` - Current time from injected clock
- `DeterministicID(prefix)` - Reproducible IDs based on execution/path/step
- `Rand()` - Seeded random source for reproducibility
- `Clock()` - Access to the clock for timer operations

## Timer Activities

Durable delays that survive recovery:
- `TimerActivity` - Fixed duration timer with checkpointed deadline
- `SleepActivity` - Runtime-specified duration via params

## File Organization

```
workflow/
├── engine.go              # Engine struct and lifecycle
├── engine_types.go        # Types: ExecutionRecord, EngineExecutionStatus
├── engine_callbacks.go    # EngineCallbacks interface
├── engine_process.go      # Process loop, recovery, reaper
├── engine_test.go         # Engine tests
├── store.go               # ExecutionStore interface
├── store_memory.go        # In-memory implementation
├── store_postgres.go      # PostgreSQL implementation
├── queue.go               # WorkQueue interface
├── queue_memory.go        # In-memory implementation
├── queue_postgres.go      # PostgreSQL implementation
├── environment.go         # ExecutionEnvironment interface
├── environment_local.go   # Local (blocking) implementation
├── clock.go               # Clock interface, RealClock, FakeClock
├── timer.go               # TimerActivity, SleepActivity
├── event_log.go           # EventLog interface, MemoryEventLog
├── event_log_postgres.go  # PostgreSQL EventLog
├── context.go             # workflow.Context with helpers
└── docs/design/
    ├── engine-design.md   # Full design specification
    └── engine-test-plan.md # Test plan
```

## Testing

### Unit Tests
```bash
go test ./...
```

### PostgreSQL Integration Tests
Requires Docker for testcontainers:
```bash
go test -run "TestPostgres" ./...
```

## Usage Example

```go
// Create components
store := NewPostgresStore(PostgresStoreOptions{DB: db})
queue := NewPostgresQueue(PostgresQueueOptions{
    DB:           db,
    WorkerID:     "engine-1",
    PollInterval: 100 * time.Millisecond,
    LeaseTTL:     5 * time.Minute,
})
env := NewLocalEnvironment(LocalEnvironmentOptions{
    Checkpointer: checkpointer,
    Logger:       logger,
})

// Create engine
engine, err := NewEngine(EngineOptions{
    Store:           store,
    Queue:           queue,
    Environment:     env,
    WorkerID:        "engine-1",
    MaxConcurrent:   10,
    ShutdownTimeout: 30 * time.Second,
    RecoveryMode:    RecoveryResume,
    Logger:          logger,
})

// Start and submit
engine.Start(ctx)
handle, _ := engine.Submit(ctx, SubmitRequest{
    Workflow: myWorkflow,
    Inputs:   map[string]any{"url": "https://example.com"},
})

// Graceful shutdown
engine.Shutdown(ctx)
```

## AI-Native Extensions

The `ai/` package provides AI-native workflow extensions for building agent-based systems.

### Three Perspectives Supported

1. **Workflow ABOVE Agents** - Workflows orchestrate agent activities
2. **Workflow = Agent** - The workflow IS the agent's cognitive loop
3. **Workflow BELOW Agents** - Agents invoke workflows as tools

### Core Components

#### ConversationState
JSON-serializable conversation context for checkpointing:
```go
conv := ai.NewConversationState()
conv.SystemPrompt = "You are a helpful assistant"
conv.AddUserMessage("Hello")
conv.AddAssistantMessage("Hi there!")
```

#### AgentActivity
Wraps AI agent loops as workflow activities with checkpoint boundaries at tool calls:
```go
agent := ai.NewAgentActivity("assistant", llmProvider, ai.AgentActivityOptions{
    SystemPrompt: "You are a helpful assistant",
    Tools: map[string]ai.Tool{
        "search": searchTool,
    },
})

// Use in workflow
wf, _ := workflow.New(workflow.Options{
    Steps: []*workflow.Step{
        {Name: "ask", Activity: agent.Name()},
    },
})
```

#### DurableTool
Wraps tools with idempotency via cached results:
```go
tool := ai.NewDurableTool(myTool)
// Same callID returns cached result on recovery
result, _ := tool.Execute(ctx, callID, args)
```

#### LLMProvider Interface
Generic interface for LLM backends:
```go
type LLMProvider interface {
    Generate(ctx context.Context, messages []Message, opts GenerateOptions) (*GenerateResponse, error)
    Name() string
    Model() string
}
```

#### Dive Integration
Adapter for the Dive LLM library:
```go
provider := ai.NewDiveLLMProvider(diveLLM, ai.DiveLLMProviderOptions{
    Model:        "claude-3-opus",
    ProviderName: "anthropic",
})
```

#### WorkflowTool
Exposes workflows as tools that agents can invoke:
```go
tool := ai.NewWorkflowTool(wf, engine, ai.WorkflowToolOptions{
    Name:        "process_data",
    Description: "Process data through a durable workflow",
})
```

### Built-in Tools (ai/tools/)

- `FileReadTool`, `FileWriteTool`, `FileListTool` - File operations
- `HTTPTool` - HTTP requests
- `ShellTool`, `PythonTool` - Script execution

### Reasoning Events

New event types for AI observability:
- `EventAgentThinking` - Agent's internal reasoning
- `EventAgentToolCall` - Tool invocations
- `EventAgentToolResult` - Tool results
- `EventAgentDecision` - High-level decisions

### File Organization

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

### AI Example Programs

```bash
# Simple agent in workflow
go run ./examples/ai/simple_agent

# Multi-agent pipeline
go run ./examples/ai/multi_agent

# Workflow as agent tool
go run ./examples/ai/agent_as_tool
```

## Implementation Status

- [x] Phase 1: Core Engine (Submit, Get, List, process loop)
- [x] Phase 2: PostgreSQL implementations (Store, Queue)
- [x] Phase 3: Recovery and reaper loop
- [x] Phase 4: Clock interface and timers
- [x] Phase 5: Event logging
- [x] Phase 6: Deterministic context helpers
- [x] Phase 7: AI-native extensions (ai/ package)
- [ ] Phase 8: Distributed execution (SpritesEnvironment) - Optional
