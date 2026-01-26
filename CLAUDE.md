# Workflow Engine

A Go library for building durable, recoverable workflows with a client-server architecture for production deployments.

## Architecture Overview

The library is designed around a clean client-server separation:

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLIENTS                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ workflow/client - HTTP client for remote API               │ │
│  │   Submit(name, inputs) → ID                                │ │
│  │   Get(id) → Status                                         │ │
│  │   Cancel(id)                                               │ │
│  │   List(filter) → []Status                                  │ │
│  └────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼ HTTP/WebSocket
┌─────────────────────────────────────────────────────────────────┐
│                         SERVER                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ Engine + Store (State + Task Distribution)                 │ │
│  │   - Executions: workflow instances                         │ │
│  │   - Tasks: units of work for workers                       │ │
│  └────────────────────────────────────────────────────────────┘ │
│  ┌────────────────┐  ┌────────────────┐  ┌─────────────────┐    │
│  │   Runners      │  │    Workers     │  │   Reaper        │    │
│  │ (Spec→Result)  │  │ (Claim Tasks)  │  │ (Stale detect)  │    │
│  └────────────────┘  └────────────────┘  └─────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌─────────────────┐
                    │  Memory /       │
                    │  PostgreSQL     │
                    └─────────────────┘
```

## Package Structure

The codebase is organized with clear separation of concerns:

```
workflow/
├── # Public API - Workflow Definition (for workflow authors)
├── workflow.go            # Workflow, Options
├── step.go                # Step, Input, Output, Edge, RetryConfig, CatchConfig
├── activity.go            # Activity interface
├── context.go             # Context interface for activities
├── execution.go           # Local execution (for testing/development)
├── engine.go              # Engine facade for server operators
├── engine_types.go        # SubmitRequest, ExecutionHandle
│
├── # Domain Layer - Shared Business Types
├── domain/
│   ├── execution.go       # ExecutionRecord, ExecutionStatus, ExecutionFilter
│   ├── task.go            # TaskRecord, TaskInput, TaskOutput, TaskClaimed
│   ├── store.go           # Store interface (ExecutionRepository + TaskRepository)
│   ├── runner.go          # Runner interface
│   ├── event.go           # Event, EventType, EventLog interface
│   └── callbacks.go       # Callbacks interface
│
├── # Client Package - For HTTP Clients
├── client/
│   ├── client.go          # Client interface: Submit, Get, Cancel, List, Wait
│   └── http.go            # HTTPClient implementation
│
├── # Infrastructure - Store Implementations
├── stores/
│   └── stores.go          # NewMemoryStore(), NewPostgresStore()
│
├── # Infrastructure - Runner Implementations
├── runners/
│   └── runners.go         # ContainerRunner, ProcessRunner, HTTPRunner, InlineRunner
│
├── # Server Binaries
├── cmd/
│   ├── server/      # HTTP server for distributed execution
│   └── worker/            # Remote task executor
│
├── # Internal Implementation
├── internal/
│   ├── engine/            # Engine implementation
│   │   ├── engine.go      # Core engine with task processing
│   │   ├── state.go       # EngineExecutionState (serialized workflow state)
│   │   ├── graph.go       # Edge evaluation and next step calculation
│   │   └── params.go      # Parameter resolution ($(inputs.x), $(steps.y.z))
│   ├── memory/            # In-memory store
│   ├── postgres/          # PostgreSQL store
│   ├── http/              # HTTP server and handlers
│   └── services/          # Business logic layer
│
├── # API Specification
├── api/
│   └── openapi.yaml       # OpenAPI 3.1 specification
│
└── # AI Extensions
    ai/                    # AI-native workflow extensions
```

## Import Guide

### For Workflow Authors (defining workflows)
```go
import "github.com/deepnoodle-ai/workflow"

// Use: Workflow, Step, Activity, Context, Input, Output
```

### For Server Operators (running the engine)
```go
import (
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/domain"
    "github.com/deepnoodle-ai/workflow/stores"
    "github.com/deepnoodle-ai/workflow/runners"
)

// workflow: Engine, EngineOptions, Workflow definitions
// domain: Store, Runner, ExecutionFilter, TaskInput, etc.
// stores: NewMemoryStore(), NewPostgresStore()
// runners: ContainerRunner, ProcessRunner, HTTPRunner, InlineRunner
```

### For HTTP Clients
```go
import "github.com/deepnoodle-ai/workflow/client"

// Use: Client interface, HTTPClient, Status, Result
```

## Key Components

### Store Interface (domain.Store)
Unified interface for execution state and task distribution:
```go
// Import from domain package
import "github.com/deepnoodle-ai/workflow/domain"

// Create stores via stores package
import "github.com/deepnoodle-ai/workflow/stores"

store := stores.NewMemoryStore()           // For testing
store := stores.NewPostgresStore(db)       // For production
```

### Runner Interface (domain.Runner)
Converts activity parameters to TaskInput and interprets results:
```go
import (
    "github.com/deepnoodle-ai/workflow/domain"
    "github.com/deepnoodle-ai/workflow/runners"
)

// Built-in runners
var r domain.Runner = &runners.ContainerRunner{Image: "my-image"}
var r domain.Runner = &runners.ProcessRunner{Program: "python", Args: []string{"script.py"}}
var r domain.Runner = &runners.HTTPRunner{URL: "https://api.example.com"}
var r domain.Runner = &runners.InlineRunner{Func: myFunc}
```

### Task Execution: Runners vs Worker Executor

Tasks can be executed in two places depending on engine mode:

```
                         TASK CREATED
                 TaskInput { Type: "http", ... }
                              │
         ┌────────────────────┴────────────────────┐
         │                                          │
         ▼                                          ▼
┌─────────────────────┐                 ┌─────────────────────┐
│   EMBEDDED MODE     │                 │  DISTRIBUTED MODE   │
│                     │                 │                     │
│ Engine calls        │                 │ Worker claims task  │
│ Runner.Execute()    │                 │ Executor.Execute()  │
│ (runners/runners.go)│                 │ (cmd/worker/        │
│                     │                 │  executor.go)       │
└─────────────────────┘                 └─────────────────────┘
```

**Why two implementations?**
- **Runners** (`runners/runners.go`) - Part of the main library, used when the engine executes tasks in-process (embedded mode)
- **Worker Executor** (`cmd/worker/executor.go`) - Part of the standalone worker binary, used by remote workers claiming tasks from the server

They're intentionally separate because:
1. The worker binary should be lean and not import the full workflow library
2. Workers may run in different environments (containers, remote machines)
3. Worker has specific concerns (output size limits, heartbeating)

**Task type support:**

| Task Type   | Runner (embedded) | Worker Executor (distributed) |
|-------------|-------------------|-------------------------------|
| `inline`    | ✅ Calls Go func  | ❌ Error (can't run remotely) |
| `http`      | ✅ HTTP request   | ✅ HTTP request               |
| `process`   | ✅ Subprocess     | ✅ Subprocess                 |
| `container` | ✅ docker run     | ✅ docker run                 |

**Important:** `inline` tasks only work in embedded mode because they require the actual Go function to be present. For distributed mode, use `http`, `process`, or `container` runners.

### Task Types (domain package)
Workers receive `TaskInput` and return `TaskOutput`:
```go
type TaskInput struct {
    Type    string            // "container", "process", "http", "inline"
    Image   string            // container
    Command []string          // container
    Program string            // process
    Args    []string          // process
    Dir     string            // process working directory
    URL     string            // http
    Method  string            // http
    Headers map[string]string // http
    Body    string            // http
    Env     map[string]string // all types
    Timeout time.Duration     // all types
    Input   map[string]any    // all types
}

type TaskOutput struct {
    Success  bool
    Output   string
    Error    string
    ExitCode int
    Data     map[string]any
}
```

### Execution Modes

Choose the right entry point based on your use case:

| Mode              | Use Case                | Entry Point               |
| ----------------- | ----------------------- | ------------------------- |
| Quick scripts     | One-off executions      | `workflow.Run()`          |
| Local testing     | Tests with control      | `workflow.NewExecution()` |
| Multi-workflow    | Registry-based apps     | `registry.Run()`          |
| Server deployment | Production with workers | `workflow.NewEngine()`    |
| Remote client     | HTTP API calls          | `client.NewHTTPClient()`  |

For detailed guidance on when to use each pattern, see `documentation/execution-patterns.md`.

### Engine Modes
The engine can run in two modes:
- `EngineModeEmbedded` - Claims and executes tasks directly in-process (use for testing/development)
- `EngineModeDistributed` - Only creates tasks for remote workers to claim (use for production servers)

### Event-Driven Multi-Step Execution

The engine is **event-driven**: task completion triggers state transitions. All execution state is serialized to `ExecutionRecord.StateData` between tasks, enabling:
- Crash recovery (state survives restarts)
- Distributed execution (any worker can continue)
- No goroutines per execution path

```
Task Completes → HandleTaskCompletion()
                        │
                        ▼
        ┌───────────────────────────────────┐
        │ 1. Load state from StateData      │
        │ 2. Store step output              │
        │ 3. Check retry config (if failed) │
        │ 4. Check catch config (if failed) │
        │ 5. Evaluate edges for next steps  │
        │ 6. Handle branching/joins         │
        │ 7. Create tasks for next steps    │
        │ 8. Save state back to StateData   │
        └───────────────────────────────────┘
                        │
                        ▼
              (repeat until done)
```

**State Structure:**
```go
type EngineExecutionState struct {
    PathStates  map[string]*PathState  // Per-path tracking
    JoinStates  map[string]*JoinState  // Join coordination
    PathCounter int                     // For generating path IDs
}
```

### Error Handling: Retry and Catch

Steps can configure both retry and catch behavior for error handling.

**Retry Configuration:**
```go
&workflow.Step{
    Name:     "call-api",
    Activity: "http_request",
    Retry: []*workflow.RetryConfig{{
        ErrorEquals: []string{workflow.ErrorTypeTimeout},
        MaxRetries:  3,
        BaseDelay:   1 * time.Second,
        BackoffRate: 2.0,
    }},
}
```

**Catch Configuration:**
```go
&workflow.Step{
    Name:     "risky-operation",
    Activity: "process_data",
    Catch: []*workflow.CatchConfig{{
        ErrorEquals: []string{workflow.ErrorTypeAll},
        Next:        "error-handler",
        Store:       "last_error",  // optional: store error info
    }},
}
```

**Error Types:**
- `workflow.ErrorTypeAll` - Matches any error
- `workflow.ErrorTypeActivityFailed` - Matches any non-timeout error
- `workflow.ErrorTypeTimeout` - Matches timeout/deadline errors
- Custom strings - Matched via substring in error message

When a step fails:
1. Retry configs are checked first (in order)
2. If retries exhausted or no match, catch configs are checked
3. If a catch matches, execution transitions to the catch step
4. If no catch matches, the path fails

### Client Interface (client package)
Clean interface for remote workflow operations:
```go
type Client interface {
    Submit(ctx context.Context, wf *workflow.Workflow, inputs map[string]any) (string, error)
    Get(ctx context.Context, id string) (*Status, error)
    Cancel(ctx context.Context, id string) error
    Wait(ctx context.Context, id string) (*Result, error)
    List(ctx context.Context, filter ListFilter) ([]*Status, error)
}
```

## Usage Examples

### Server-Side: Running the Engine

```go
import (
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/domain"
    "github.com/deepnoodle-ai/workflow/stores"
    "github.com/deepnoodle-ai/workflow/runners"
)

// Create store
store := stores.NewMemoryStore() // or stores.NewPostgresStore(db)

// Create runners for activities
activityRunners := map[string]domain.Runner{
    "fetch-data": &runners.HTTPRunner{URL: "https://api.example.com/data"},
    "process":    &runners.ContainerRunner{Image: "processor:latest"},
    "notify":     &runners.InlineRunner{Func: notifyFunc},
}

// Create and start engine
engine, err := workflow.NewEngine(workflow.EngineOptions{
    Store:         store,
    Runners:       activityRunners,
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

### Client-Side: Submitting Workflows

```go
import "github.com/deepnoodle-ai/workflow/client"

// Create HTTP client
c := client.NewHTTPClient(client.HTTPClientOptions{
    BaseURL: "http://server:8080",
    Token:   "secret-token",
})

// Submit workflow (requires workflow object, not just name)
id, err := c.Submit(ctx, myWorkflow, map[string]any{
    "input": "value",
})

// Wait for completion
result, err := c.Wait(ctx, id)
if result.Status == client.ExecutionStatusCompleted {
    fmt.Println("Outputs:", result.Outputs)
}
```

### Local Execution (Testing/Development)

```go
import "github.com/deepnoodle-ai/workflow"

// Define activities
activities := []workflow.Activity{
    workflow.NewActivityFunction("greet", func(ctx workflow.Context, params map[string]any) (any, error) {
        name := params["name"].(string)
        return fmt.Sprintf("Hello, %s!", name), nil
    }),
}

// Create and run execution
execution, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:   myWorkflow,
    Inputs:     map[string]any{"name": "World"},
    Activities: activities,
})
err := execution.Run(ctx)
```

## Distributed Execution

### Server

The server provides HTTP endpoints for task distribution.

**For multi-step workflows**, the server uses the Engine internally:

```go
import (
    "github.com/deepnoodle-ai/workflow/internal/engine"
    workflowhttp "github.com/deepnoodle-ai/workflow/internal/http"
)

// Create engine with workflow definitions (distributed mode)
eng, _ := engine.New(engine.Options{
    Store:     store,
    Workflows: workflows,  // map[string]domain.WorkflowDefinition
    WorkerID:  "server",
    Mode:      engine.ModeDistributed,
})

// Create HTTP server - Engine handles workflow advancement automatically
server := workflowhttp.NewServer(workflowhttp.ServerOptions{
    Engine:      eng,
    TaskService: taskService,
})
```

**Command line (simple task queue mode):**

```bash
# Start server
WORKFLOW_STORE_DSN="postgres://user:pass@host/db" \
AUTH_TOKEN="secret-token" \
LISTEN_ADDR=":8080" \
./server serve

# Create database schema (first time only)
WORKFLOW_STORE_DSN="postgres://..." ./server migrate
```

### Worker

Workers poll for tasks via HTTP and execute them using the built-in executor (`cmd/worker/executor.go`).

**Supported task types:**
- **http** - Makes HTTP requests to external APIs
- **process** - Runs local processes (params passed via env vars and stdin)
- **container** - Runs Docker containers (params passed via env vars)

**Not supported:**
- **inline** - Returns error (inline tasks require the Go function in-process)

See "Task Execution: Runners vs Worker Executor" above for the full architecture.

```bash
SERVER_URL="http://server:8080" \
WORKER_TOKEN="secret-token" \
./worker run
```

**Worker commands:**
- `worker run` - Poll continuously for tasks
- `worker once` - Claim and execute a single task, then exit

### Docker Deployment

The repository includes Docker support for containerized deployments:

```bash
# Start everything (PostgreSQL + server + 2 workers)
docker compose up -d

# View logs
docker compose logs -f

# Scale workers
docker compose up -d --scale worker=5

# Stop and remove volumes (clean slate)
docker compose down -v
```

**Environment variables:**
- `AUTH_TOKEN` - Authentication token (default: `dev-token`)

```bash
# With custom auth token
AUTH_TOKEN=my-secret-token docker compose up -d
```

**Docker images:**
```bash
# Build server image
docker build --target server -t workflow-server .

# Build worker image
docker build --target worker -t workflow-worker .
```

### API Endpoints

See `api/openapi.yaml` for the full specification. Key endpoints:

**For Workers:**
- `POST /tasks/claim` - Claim next available task
- `POST /tasks/{id}/complete` - Report task result
- `POST /tasks/{id}/heartbeat` - Send heartbeat

**For Clients:**
- `POST /executions` - Submit workflow
- `GET /executions/{id}` - Get execution status
- `POST /executions/{id}/cancel` - Cancel execution
- `GET /executions` - List executions

**Health:**
- `GET /health` - Health check

## Context Helpers

The `workflow.Context` provides deterministic helpers for activities:
- `Now()` - Current time from injected clock
- `DeterministicID(prefix)` - Reproducible IDs based on execution/path/step
- `Rand()` - Seeded random source for reproducibility
- `Clock()` - Access to the clock for timer operations

## AI-Native Extensions

The `ai/` package provides AI-native workflow extensions for building agent-based systems.

### Core Components

#### AgentActivity
Wraps AI agent loops as workflow activities:
```go
agent := ai.NewAgentActivity("assistant", llmProvider, ai.AgentActivityOptions{
    SystemPrompt: "You are a helpful assistant",
    Tools: map[string]ai.Tool{"search": searchTool},
})
```

#### Reasoning Events
Event types for AI observability (import from `domain`):
- `EventAgentThinking` - Agent's internal reasoning
- `EventAgentToolCall` - Tool invocations
- `EventAgentToolResult` - Tool results
- `EventAgentDecision` - High-level decisions

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

## Implementation Status

- [x] Core Engine (Submit, Get, List, process loop)
- [x] PostgreSQL store implementation
- [x] Recovery and reaper loop
- [x] Clock interface and timers
- [x] Event logging
- [x] Deterministic context helpers
- [x] AI-native extensions (ai/ package)
- [x] Unified store with task-based execution
- [x] HTTP server API and OpenAPI spec
- [x] Client-server architecture separation
- [x] Event-driven multi-step workflow execution
- [x] Branching and conditional edges
- [x] Join steps for path convergence
- [x] Retry logic with configurable backoff
- [x] Catch error handlers with step transitions
- [x] Server callback for distributed multi-step workflows
- [x] Docker deployment (Dockerfile + docker-compose.yml)
- [x] Worker task executors (HTTP, process, container)
- [ ] Sprites integration for isolated execution (optional)
