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

## Package Structure

The codebase is organized into a public API layer and internal implementations:

```
workflow/
├── # Public API (import as "github.com/deepnoodle-ai/workflow")
├── engine.go              # Engine facade (delegates to internal/engine)
├── engine_types.go        # Type aliases for ExecutionRecord, Status, etc.
├── engine_callbacks.go    # EngineCallbacks alias
├── store.go               # ExecutionStore interface + NewMemoryStore/NewPostgresStore
├── task.go                # Task type aliases (TaskRecord, TaskSpec, etc.)
├── runner.go              # Runner type aliases (InlineRunner, etc.)
├── workflow.go            # Workflow definition types
├── step.go                # Step definition types
├── execution.go           # Local execution without engine
│
├── # Internal implementation
├── internal/
│   ├── engine/            # Canonical Engine implementation
│   │   ├── engine.go      # Core engine logic
│   │   ├── types.go       # ExecutionRecord, Status, etc.
│   │   ├── store.go       # Store interface
│   │   └── callbacks.go   # Callbacks interface
│   ├── task/              # Task types and runners
│   │   ├── types.go       # Record, Spec, Result, Claimed
│   │   └── runner.go      # Runner implementations
│   ├── memory/            # In-memory store implementation
│   ├── postgres/          # PostgreSQL store implementation
│   ├── http/              # HTTP server and client
│   │   ├── server.go      # HTTP server for orchestrator
│   │   ├── handlers.go    # Request handlers
│   │   ├── client.go      # HTTP client for workers
│   │   └── auth.go        # Authentication middleware
│   └── services/          # Business logic layer
│       ├── task.go        # TaskService
│       ├── execution.go   # ExecutionService
│       └── reaper.go      # ReaperService
│
├── # Binaries
├── cmd/
│   ├── orchestrator/      # HTTP server for distributed execution
│   └── worker/            # Remote task executor
│
├── # API specification
├── api/
│   └── openapi.yaml       # OpenAPI 3.1 specification
│
└── docs/design/           # Design documents
```

### Type Re-exports

The public workflow package re-exports types from internal packages for backward compatibility:

```go
// These are equivalent:
workflow.ExecutionRecord == internal/engine.ExecutionRecord
workflow.TaskRecord == internal/task.Record
workflow.Runner == internal/task.Runner
```

For new internal code, prefer importing the internal packages directly.

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

### Local Execution

For testing and single-process deployments:

```go
// Create in-memory store (for testing)
store := workflow.NewMemoryStore()

// Or PostgreSQL store (for production)
db, _ := sql.Open("postgres", os.Getenv("DATABASE_URL"))
store := workflow.NewPostgresStore(db)
store.CreateSchema(ctx) // First time only

// Create runners for activities
runners := map[string]workflow.Runner{
    "fetch-data": &workflow.HTTPRunner{URL: "https://api.example.com/data"},
    "process":    &workflow.ContainerRunner{Image: "processor:latest"},
    "notify":     &workflow.InlineRunner{Func: notifyFunc},
}

// Create and start engine
engine, err := workflow.NewEngine(workflow.EngineOptions{
    Store:         store,
    Runners:       runners,
    WorkerID:      "engine-1",
    MaxConcurrent: 10,
})
engine.Start(ctx)

// Submit workflow
handle, _ := engine.Submit(ctx, workflow.SubmitRequest{
    Workflow: myWorkflow,
    Inputs:   map[string]any{"url": "https://example.com"},
})

// Graceful shutdown
engine.Shutdown(ctx)
```

## Distributed Execution

For production deployments with multiple workers:

### Orchestrator

The orchestrator provides HTTP endpoints for task distribution:

```bash
# Start orchestrator
WORKFLOW_STORE_DSN="postgres://user:pass@host/db" \
AUTH_TOKEN="secret-token" \
LISTEN_ADDR=":8080" \
./orchestrator serve

# Create database schema (first time only)
WORKFLOW_STORE_DSN="postgres://..." ./orchestrator migrate

# Run stale task detection manually (for cron jobs)
WORKFLOW_STORE_DSN="postgres://..." ./orchestrator reap
```

Environment variables:
- `WORKFLOW_STORE_DSN` (required): PostgreSQL connection string
- `LISTEN_ADDR` (default `:8080`): HTTP listen address
- `AUTH_TOKEN` (optional): Bearer token for authentication
- `HEARTBEAT_TIMEOUT` (default `2m`): Stale task threshold
- `REAPER_INTERVAL` (default `30s`): Stale task check interval

### Worker

Workers poll for tasks and execute them:

```bash
# Connect via HTTP to orchestrator
ORCHESTRATOR_URL="http://orchestrator:8080" \
WORKER_TOKEN="secret-token" \
./worker run

# Or connect directly to PostgreSQL
WORKFLOW_STORE_DSN="postgres://..." ./worker run

# Execute single task and exit (for batch/serverless)
./worker once
```

### API Endpoints

See `api/openapi.yaml` for the full specification. Key endpoints:

- `POST /tasks/claim` - Worker claims next available task
- `POST /tasks/{id}/complete` - Worker reports task result
- `POST /tasks/{id}/heartbeat` - Worker sends heartbeat
- `GET /executions/{id}` - Get execution status
- `GET /health` - Health check

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
- [x] Phase 9: HTTP orchestrator API and OpenAPI spec
- [ ] Phase 10: Sprites integration for isolated execution - Optional

## Migration Guide

### From Direct Store Usage

If you were creating stores directly from internal packages:

```go
// Old way
import "github.com/deepnoodle-ai/workflow/internal/memory"
store := memory.NewStore()

// New way - use convenient constructors
import "github.com/deepnoodle-ai/workflow"
store := workflow.NewMemoryStore()
// or
store := workflow.NewPostgresStore(db)
```

### From Inline Engine

If you had code creating the internal engine directly, it will continue to work.
The public `workflow.Engine` is now a thin facade that delegates to the internal
engine, maintaining full backward compatibility.
