# Workflow

A durable workflow automation library for Go with a clean client-server architecture. Supports conditional branching, parallel execution, embedded scripting, and production features like crash recovery and PostgreSQL persistence.

Think of it like a lightweight hybrid of Temporal and AWS Step Functions.

## Features

- **Durable Execution** - Workflows survive crashes and can resume from checkpoints
- **Client-Server Architecture** - Clean separation between clients and server
- **Conditional Branching** - Dynamic flow control based on step results
- **Parallel Execution** - Run multiple paths concurrently
- **Retry with Backoff** - Configurable retry policies per step
- **Expression Syntax** - Dynamic parameter resolution with `$(inputs.x)`, `$(steps.y.z)` syntax
- **Task-Based Workers** - Distributed execution with heartbeating

## Main Concepts

| Concept        | Description                                               |
| -------------- | --------------------------------------------------------- |
| **Workflow**   | A repeatable process defined as a directed graph of steps |
| **Steps**      | Individual nodes in the workflow graph                    |
| **Activities** | Functions that perform the actual work                    |
| **Edges**      | Define flow between steps                                 |
| **Execution**  | A single run of a workflow                                |
| **Engine**     | Server-side supervisor for production deployments         |
| **Client**     | HTTP client for remote workflow operations                |

## Quick Example

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/activities"
)

func main() {
    attempt := 0

    myOperation := func(ctx workflow.Context, input map[string]any) (string, error) {
        attempt++
        if attempt < 3 {
            return "", fmt.Errorf("service is temporarily unavailable")
        }
        return "SUCCESS", nil
    }

    w, err := workflow.New(workflow.Options{
        Name: "demo",
        Steps: []*workflow.Step{
            {
                Name:     "Call My Operation",
                Activity: "my_operation",
                Store:    "result",
                Retry:    []*workflow.RetryConfig{{MaxRetries: 2}},
                Next:     []*workflow.Edge{{Step: "Finish"}},
            },
            {
                Name:     "Finish",
                Activity: "print",
                Parameters: map[string]any{
                    "message": "Workflow completed! Result: $(state.result)",
                },
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    execution, err := workflow.NewExecution(workflow.ExecutionOptions{
        Workflow: w,
        Logger:   workflow.NewLogger(),
        Activities: []workflow.Activity{
            workflow.NewTypedActivityFunction("my_operation", myOperation),
            activities.NewPrintActivity(),
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    if err := execution.Run(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

## Package Structure

```
workflow/                  # Workflow definitions, Activity, Context
workflow/domain/           # Shared types: Store, Runner, Task*, Event*
workflow/client/           # HTTP client for remote operations
workflow/stores/           # Store implementations: Memory, PostgreSQL
workflow/runners/          # Runner implementations: Container, Process, HTTP, Inline
workflow/cmd/server/ # HTTP server binary
workflow/cmd/worker/       # Task worker binary
```

## Server-Side: Running the Engine

```go
import (
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/domain"
    "github.com/deepnoodle-ai/workflow/stores"
    "github.com/deepnoodle-ai/workflow/runners"
)

// Create store (PostgreSQL for production)
store := stores.NewPostgresStore(db)
stores.CreateSchema(ctx, store)

// Create runners for activities
activityRunners := map[string]domain.Runner{
    "fetch-data": &runners.HTTPRunner{URL: "https://api.example.com/data"},
    "process":    &runners.ContainerRunner{Image: "processor:latest"},
    "notify":     &runners.InlineRunner{Func: notifyFunc},
}

// Create and start engine
engine, _ := workflow.NewEngine(workflow.EngineOptions{
    Store:         store,
    Runners:       activityRunners,
    WorkerID:      "engine-1",
    MaxConcurrent: 10,
})
engine.Start(ctx)
defer engine.Shutdown(ctx)

// Submit workflow
handle, _ := engine.Submit(ctx, workflow.SubmitRequest{
    Workflow: myWorkflow,
    Inputs:   map[string]any{"url": "https://example.com"},
})

fmt.Printf("Execution ID: %s\n", handle.ID)
```

## Client-Side: Submitting Workflows

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
if result.State == client.StateCompleted {
    fmt.Println("Outputs:", result.Outputs)
}
```

## Distributed Execution

### Docker Compose (Recommended)

The fastest way to run the full stack locally:

```bash
# Start PostgreSQL + server + workers
docker compose up -d

# View logs
docker compose logs -f

# Scale workers
docker compose up -d --scale worker=5

# Stop everything
docker compose down -v
```

Set `AUTH_TOKEN` environment variable to customize the authentication token (default: `dev-token`).

### Manual Setup

#### Server

```bash
# Start server
WORKFLOW_STORE_DSN="postgres://user:pass@host/db" \
AUTH_TOKEN="secret-token" \
LISTEN_ADDR=":8080" \
./server serve

# Create database schema (first time only)
WORKFLOW_STORE_DSN="postgres://..." ./server migrate
```

#### Worker

The worker executes tasks claimed from the server. Supports HTTP requests, process execution, and Docker containers.

```bash
# Connect via HTTP to server
SERVER_URL="http://server:8080" \
WORKER_TOKEN="secret-token" \
./worker run

# Or connect directly to PostgreSQL
WORKFLOW_STORE_DSN="postgres://..." ./worker run
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
- `GET /executions` - List executions

## Deterministic Helpers

The `workflow.Context` provides helpers for writing recoverable workflows:

```go
func (a *MyActivity) Execute(ctx workflow.Context, params map[string]any) (any, error) {
    id := ctx.DeterministicID("order")  // Reproducible ID
    delay := ctx.Rand().Intn(10)        // Seeded random
    now := ctx.Now()                    // From injected clock
    return result, nil
}
```

## Testing

```bash
# Run all tests
go test ./...

# Run PostgreSQL integration tests (requires Docker)
go test -run "TestPostgres" ./...
```

Use `FakeClock` for deterministic time-based tests:

```go
func TestWorkflowWithTimer(t *testing.T) {
    clock := workflow.NewFakeClock(time.Now())

    ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
        Clock: clock,
    })

    // Advance time instantly
    clock.Advance(1 * time.Hour)
}
```

## Documentation

- [CLAUDE.md](CLAUDE.md) - Architecture and implementation details
- [docs/design/](docs/design/) - Design documents
- [documentation/](documentation/) - Core workflow concepts

## Installation

```bash
go get github.com/deepnoodle-ai/workflow
```

### Building Docker Images

```bash
# Build server image
docker build --target server -t workflow-server .

# Build worker image
docker build --target worker -t workflow-worker .
```
