# Workflow

A durable workflow automation library for Go. Supports conditional branching, parallel execution, embedded scripting, execution checkpointing, and production-ready features like crash recovery and PostgreSQL persistence.

Think of it like a lightweight hybrid of Temporal and AWS Step Functions.

## Features

- **Durable Execution** - Workflows survive crashes and can resume from checkpoints
- **Conditional Branching** - Dynamic flow control based on step results
- **Parallel Execution** - Run multiple paths concurrently
- **Retry with Backoff** - Configurable retry policies per step
- **Embedded Scripting** - Use [Risor](https://risor.io) for dynamic expressions
- **Engine Layer** - Optional production features: bounded concurrency, PostgreSQL persistence, crash recovery

## Main Concepts

| Concept | Description |
|---------|-------------|
| **Workflow**   | A repeatable process defined as a directed graph of steps |
| **Steps**      | Individual nodes in the workflow graph |
| **Activities** | Functions that perform the actual work |
| **Edges**      | Define flow between steps |
| **Execution**  | A single run of a workflow |
| **State**      | Shared mutable state that persists for the duration of an execution |
| **Engine**     | (Optional) Supervisor layer for production deployments |

### How They Work Together

**Workflows** define **Steps** that execute **Activities**. An **Execution** is
a single run of a workflow. When a step finishes, its outgoing **Edges** are
evaluated and the next step(s) are selected based on any associated conditions.
The **State** may be read and written to by the activities.

The optional **Engine** layer manages multiple executions with bounded concurrency,
durable submission, and crash recovery.

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
					"message": "Workflow completed! Result: ${state.result}",
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

## Engine Layer (Production)

For production deployments, the Engine layer adds:

- **Durable Submission** - Inputs persisted before acknowledging submit
- **Bounded Concurrency** - Control max parallel executions
- **Crash Recovery** - Automatically resume or fail orphaned executions
- **PostgreSQL Persistence** - Store and queue backed by PostgreSQL
- **Heartbeat Monitoring** - Detect and recover stale executions

### Engine Example

```go
// Create PostgreSQL-backed components
store := stores.NewPostgresStore(db)
queue := workflow.NewPostgresQueue(workflow.PostgresQueueOptions{
	DB:           db,
	WorkerID:     "worker-1",
	PollInterval: 100 * time.Millisecond,
	LeaseTTL:     5 * time.Minute,
})
env := workflow.NewLocalEnvironment(workflow.LocalEnvironmentOptions{
	Checkpointer: checkpointer,
	Logger:       logger,
})

// Create and start engine
engine, _ := workflow.NewEngine(workflow.EngineOptions{
	Store:           store,
	Queue:           queue,
	Environment:     env,
	WorkerID:        "worker-1",
	MaxConcurrent:   10,
	ShutdownTimeout: 30 * time.Second,
	RecoveryMode:    workflow.RecoveryResume,
	Logger:          logger,
})

engine.Start(ctx)
defer engine.Shutdown(ctx)

// Submit workflows
handle, _ := engine.Submit(ctx, workflow.SubmitRequest{
	Workflow: myWorkflow,
	Inputs:   map[string]any{"url": "https://example.com"},
})

// Check status
record, _ := engine.Get(ctx, handle.ID)
fmt.Printf("Status: %s\n", record.Status)
```

## Distributed Execution

For scaling beyond a single process, the engine supports dispatching work to remote workers via [Sprites](https://sprites.dev/):

```go
import "github.com/deepnoodle-ai/workflow/internal/sprites"

// Create Sprites-backed environment
env, _ := sprites.NewEnvironment(sprites.EnvironmentOptions{
	Token:    os.Getenv("SPRITE_API_TOKEN"),
	StoreDSN: "postgres://...",
})

engine, _ := workflow.NewEngine(workflow.EngineOptions{
	Store:       store,
	Queue:       queue,
	Environment: env,  // Dispatch mode
	// ...
})
```

The worker binary runs in each Sprite:

```bash
# Worker claims, runs, and completes executions
worker run <execution-id> <attempt> [-w worker-id]

# Configuration via environment
export WORKFLOW_STORE_DSN="postgres://..."
export WORKFLOW_HEARTBEAT_INTERVAL="30s"
```

Communication between engine and workers is via PostgreSQL:
- Engine writes execution records and enqueues work
- Workers claim executions with fencing (attempt-based)
- Workers send heartbeats; engine reaper detects stale executions

## Timers

Durable delays that survive workflow recovery:

```go
// Fixed duration timer
timer := workflow.NewTimerActivity("rate-limit", 1*time.Second)

// Runtime-specified duration via params
sleep := workflow.NewSleepActivity()
// Use with params: {"duration": "5s"}
```

## Deterministic Helpers

The `workflow.Context` provides helpers for writing recoverable workflows:

```go
func (a *MyActivity) Execute(ctx workflow.Context, params map[string]any) (any, error) {
	// Use context helpers instead of non-deterministic operations
	id := ctx.DeterministicID("order")  // Reproducible ID
	delay := ctx.Rand().Intn(10)        // Seeded random
	now := ctx.Now()                    // From injected clock
	return result, nil
}
```

## Testing

Use `FakeClock` for deterministic time-based tests:

```go
func TestWorkflowWithTimer(t *testing.T) {
	clock := workflow.NewFakeClock(time.Now())

	// Create execution with fake clock
	ctx := workflow.NewContext(context.Background(), workflow.ExecutionContextOptions{
		Clock: clock,
	})

	// Advance time instantly
	clock.Advance(1 * time.Hour)
}
```

## Documentation

- [CLAUDE.md](CLAUDE.md) - Architecture and implementation details
- [docs/design/engine-design.md](docs/design/engine-design.md) - Full engine specification
- [documentation/](documentation/) - Core workflow concepts

## Installation

```bash
go get github.com/deepnoodle-ai/workflow
```

## License

MIT
