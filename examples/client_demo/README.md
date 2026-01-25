# Client Demo

This example demonstrates the Client interface pattern for workflow interaction. The Client interface provides a clean separation between workflow submission and execution, supporting both local and remote backends.

## The Client Interface

The `client.Client` interface provides these operations:

```go
type Client interface {
    Submit(ctx context.Context, wf *workflow.Workflow, inputs map[string]any) (string, error)
    Get(ctx context.Context, id string) (*Status, error)
    Cancel(ctx context.Context, id string) error
    Wait(ctx context.Context, id string) (*Result, error)
    List(ctx context.Context, filter ListFilter) ([]*Status, error)
}
```

## Client Implementations

### LocalClient

For development, testing, and simple deployments:

```go
import "github.com/deepnoodle-ai/workflow/client"

registry := workflow.NewRegistry()
registry.MustRegisterWorkflow(myWorkflow)
registry.MustRegisterActivity(myActivity)

c, _ := client.NewLocalClient(client.LocalClientOptions{
    Registry: registry,
})
c.Start(ctx)
defer c.Stop(ctx)

execID, _ := c.Submit(ctx, myWorkflow, inputs)
result, _ := c.Wait(ctx, execID)
```

### HTTPClient

For production deployments with remote orchestrator:

```go
import "github.com/deepnoodle-ai/workflow/client"

c := client.NewHTTPClient(client.HTTPClientOptions{
    BaseURL: "http://orchestrator:8080",
    Token:   "your-auth-token",
})

execID, _ := c.Submit(ctx, myWorkflow, inputs)
status, _ := c.Get(ctx, execID)
```

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                 Your Application                    │
│  ┌──────────────────────────────────────────────┐   │
│  │            client.Client interface           │   │
│  │   Submit() | Get() | Wait() | Cancel()       │   │
│  └──────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
                          │
            ┌─────────────┴─────────────┐
            │                           │
            ▼                           ▼
┌───────────────────────┐   ┌───────────────────────┐
│     LocalClient       │   │     HTTPClient        │
│  (in-process engine)  │   │  (remote server)      │
└───────────────────────┘   └───────────────────────┘
                                        │
                                        ▼ HTTP
                            ┌───────────────────────┐
                            │     Orchestrator      │
                            │   (Engine + Workers)  │
                            └───────────────────────┘
```

## When to Use Each

| Scenario | Client |
|----------|--------|
| Unit tests | LocalClient |
| Integration tests | LocalClient |
| Development | LocalClient |
| Single-process deployment | LocalClient |
| Distributed deployment | HTTPClient |
| Multiple applications | HTTPClient |

## Running the Example

```bash
go run main.go
```

Expected output:
```
=== Workflow Client Demo ===

Submitting workflow...
Workflow submitted with ID: exec_abc123
Polling for completion...
  [greet] Generated: Hello, World!
  Status: running
  [transform] Transformed: *** Hello, World! ***
  Status: completed

Getting final result...
Workflow completed!
  State: completed
  Duration: 15ms
  Outputs: map[greeting:Hello, World! transformed:*** Hello, World! ***]

Listing all executions...
  - exec_abc123: greeting-workflow (completed)
```

## Key Concepts

### Registry

The `Registry` holds workflow definitions and activities. When using LocalClient, workflows and activities are registered locally. When using HTTPClient, workflows must be pre-registered on the server.

### Workflow Outputs

Use the `Outputs` field in workflow options to declare which state variables should be returned:

```go
workflow.Options{
    Outputs: []*workflow.Output{
        {Name: "greeting"},
        {Name: "transformed"},
    },
}
```

### Error Handling

The Client interface uses idiomatic Go error handling:

```go
result, err := c.Wait(ctx, execID)
if err != nil {
    // Handle error
}
if result.State == client.StateFailed {
    fmt.Printf("Workflow failed: %s\n", result.Error)
}
```
