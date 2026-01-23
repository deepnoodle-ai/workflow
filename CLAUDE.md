# Workflow Engine

A Go library for building durable, recoverable workflows with an optional Engine layer for production deployments.

## Architecture Overview

The library has two layers:

1. **Core Workflow Library** - `Execution`, `Path`, `Step`, `Activity` for defining and running workflows
2. **Engine Layer** (optional) - Adds durability, bounded concurrency, crash recovery, and distributed execution

```
┌─────────────────────────────────────────────────────────────────┐
│                           ENGINE                                │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ ExecutionStore (State + Task Distribution)                 │ │
│  │   - Executions: workflow instances                         │ │
│  │   - Tasks: units of work for workers                       │ │
│  └───────────────────────────────────────────────────────────┘ │
│  ┌────────────────┐  ┌────────────────┐  ┌─────────────────┐   │
│  │   Runners      │  │    Workers     │  │   Reaper        │   │
│  │ (Spec->Result) │  │ (Claim Tasks)  │  │ (Stale detect)  │   │
│  └────────────────┘  └────────────────┘  └─────────────────┘   │
└────────────────────────────────────────────────────────────────┘
           │
           ▼
    ┌─────────────┐
    │  Memory /   │
    │  Postgres   │
    └─────────────┘
```

## Key Components

### ExecutionStore Interface
Unified interface for both execution state and task distribution. Implementations:
- `MemoryStore` - In-memory, for testing
- `PostgresStore` - PostgreSQL-backed, for production

Key methods:
- Execution lifecycle: `CreateExecution`, `GetExecution`, `UpdateExecution`, `ListExecutions`
- Task lifecycle: `CreateTask`, `ClaimTask`, `CompleteTask`, `ReleaseTask`, `HeartbeatTask`
- Recovery: `ListStaleTasks`, `ResetTask`

### Runner Interface
Converts activity parameters to TaskSpec and interprets results:
- `ContainerRunner` - Execute as Docker container
- `ProcessRunner` - Execute as local process
- `HTTPRunner` - Execute as HTTP request
- `InlineRunner` - Execute in-process (for testing)

```go
type Runner interface {
    ToSpec(ctx context.Context, params map[string]any) (*TaskSpec, error)
    ParseResult(result *TaskResult) (map[string]any, error)
}
```

### Task Types
Workers receive `TaskSpec` and return `TaskResult`:
```go
type TaskSpec struct {
    Type    string            // "container", "process", "http", "inline"
    Image   string            // container
    Command []string          // container
    Program string            // process
    Args    []string          // process
    URL     string            // http
    Method  string            // http
    Env     map[string]string // all types
    Input   map[string]any    // all types
}

type TaskResult struct {
    Success  bool
    Output   string
    Error    string
    ExitCode int
    Data     map[string]any
}
```

### Engine Modes
The engine can run in two modes:
- `EngineModeLocal` - Claims and executes tasks directly in-process
- `EngineModeOrchestrator` - Only creates tasks for remote workers to claim

### Clock Interface
Abstraction for time operations, enabling deterministic testing:
- `RealClock` - Uses system time (production)
- `FakeClock` - Manually controlled time (testing)

### EventLog Interface
Observability layer for workflow events:
- `MemoryEventLog` - In-memory, for testing
- `PostgresEventLog` - PostgreSQL-backed, for production

## Engine Features

### Task-Based Execution
- Workflows submit -> create execution + first task
- Workers poll and claim tasks with `ClaimTask` (uses `FOR UPDATE SKIP LOCKED` in Postgres)
- Workers heartbeat while executing
- Workers complete with result using `CompleteTask`
- Engine advances to next step or marks execution complete

### Recovery
- **Stale task detection**: Tasks with old heartbeats are reset for retry
- **Visibility delay**: Failed tasks can have delayed retry via `visible_at` field

### Fenced Operations
- Task claiming is atomic via database transactions
- Workers must match expected worker ID for completion/heartbeat

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
├── engine_types.go        # ExecutionRecord, EngineExecutionStatus
├── engine_callbacks.go    # EngineCallbacks interface
├── engine_test.go         # Engine tests
├── store.go               # ExecutionStore interface
├── store_memory.go        # In-memory implementation
├── store_postgres.go      # PostgreSQL implementation
├── task.go                # TaskRecord, TaskSpec, TaskResult, ClaimedTask
├── runner.go              # Runner interface and implementations
├── clock.go               # Clock interface, RealClock, FakeClock
├── timer.go               # TimerActivity, SleepActivity
├── event_log.go           # EventLog interface, MemoryEventLog
├── event_log_postgres.go  # PostgreSQL EventLog
├── context.go             # workflow.Context with helpers
├── cmd/worker/            # Worker binary for remote execution
└── docs/design/
    ├── engine-design.md   # Full design specification
    ├── unified-store-plan.md # Task-based store design
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
// Create store
store := NewPostgresStore(PostgresStoreOptions{DB: db})

// Create runners for activities
runners := map[string]Runner{
    "fetch-data": &HTTPRunner{URL: "https://api.example.com/data"},
    "process":    &ContainerRunner{Image: "processor:latest"},
    "notify":     &InlineRunner{Func: notifyFunc},
}

// Create engine
engine, err := NewEngine(EngineOptions{
    Store:         store,
    Runners:       runners,
    WorkerID:      "engine-1",
    MaxConcurrent: 10,
    Mode:          EngineModeLocal, // or EngineModeOrchestrator
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

## Remote Worker

For distributed execution, run workers on separate machines:

```bash
# Worker polls for tasks and executes them
WORKFLOW_STORE_DSN="postgres://..." ./worker run

# Or execute a single task and exit
WORKFLOW_STORE_DSN="postgres://..." ./worker once
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
- [x] Phase 2: PostgreSQL implementations (Store)
- [x] Phase 3: Recovery and reaper loop
- [x] Phase 4: Clock interface and timers
- [x] Phase 5: Event logging
- [x] Phase 6: Deterministic context helpers
- [x] Phase 7: AI-native extensions (ai/ package)
- [x] Phase 8: Unified store with task-based execution
- [ ] Phase 9: HTTP orchestrator API - Optional
- [ ] Phase 10: Sprites integration for isolated execution - Optional
