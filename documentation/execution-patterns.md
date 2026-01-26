# Execution Patterns Guide

This guide explains when to use each execution pattern in the workflow engine.

## Quick Reference

| Pattern | Best For | Entry Point |
|---------|----------|-------------|
| `workflow.Run()` | Scripts, one-off executions | `Run(ctx, wf, inputs, activities...)` |
| `workflow.NewExecution()` | Tests needing control | `NewExecution(opts)` then `Run(ctx)` |
| `registry.Run()` | Multiple workflows | `registry.Run(ctx, name, inputs)` |
| `workflow.NewEngine()` | Production servers | `NewEngine(opts)` |
| `client.NewHTTPClient()` | Remote API calls | `Submit(ctx, wf, inputs)` |

## Pattern 1: Quick Scripts

For simple scripts that run one workflow and exit:

```go
result, err := workflow.Run(ctx, wf, inputs,
    activities.NewPrintActivity(),
    myCustomActivity,
)
if err != nil {
    log.Fatal(err)
}
fmt.Println("Outputs:", result.Outputs)
```

**When to use**: CLI tools, data migrations, one-off tasks.

## Pattern 2: Tests with Control

For tests that need custom IDs, mocked clocks, or callbacks:

```go
execution, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:    wf,
    Inputs:      inputs,
    Activities:  []workflow.Activity{mockActivity},
    Clock:       mockClock,
    ExecutionID: "test-123",
})
err := execution.Run(ctx)
```

**When to use**: Unit tests, integration tests, debugging.

## Pattern 3: Shared Activities via Registry

For applications with multiple workflows sharing activities:

```go
registry := workflow.NewRegistry()

// Register once at startup
registry.MustRegisterWorkflow(orderWorkflow)
registry.MustRegisterWorkflow(paymentWorkflow)
registry.MustRegisterActivity(httpActivity)

// Execute by name
result, _ := registry.Run(ctx, "order-workflow", inputs)
```

**When to use**: Microservices, multi-workflow applications.

## Pattern 4: Production Servers

For distributed execution with separate workers:

```go
engine, _ := workflow.NewEngine(workflow.EngineOptions{
    Store:    postgresStore,
    Registry: registry,
    Mode:     workflow.EngineModeDistributed,
})
engine.Start(ctx)

handle, _ := engine.Submit(ctx, workflow.SubmitRequest{
    Workflow: wf,
    Inputs:   inputs,
})
```

**When to use**: Production deployments, horizontal scaling.

## Pattern 5: Remote Client

For clients calling a workflow server over HTTP:

```go
client := client.NewHTTPClient(client.HTTPClientOptions{
    BaseURL: "http://server:8080",
    Token:   "auth-token",
})

id, _ := client.Submit(ctx, wf, inputs)
result, _ := client.Wait(ctx, id)
```

**When to use**: Separate client applications, microservice communication.

## Combining Registry and Direct Activities

You can use Registry for common activities and override specific ones:

```go
execution, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:   wf,
    Registry:   registry,           // Base activities
    Activities: []workflow.Activity{
        mockHTTPActivity,           // Override HTTP for testing
    },
})
```

Direct Activities take precedence over Registry for duplicate names.
This is useful for testing where you want to mock specific activities
while keeping all others from the registry.

## Decision Tree

```
Do you need to run a workflow?
│
├─ Single workflow, simple script?
│  └─ Use workflow.Run()
│
├─ Need custom execution ID, clock, or callbacks?
│  └─ Use workflow.NewExecution()
│
├─ Multiple workflows sharing activities?
│  ├─ Local execution? → Use registry.Run()
│  └─ Server deployment? → Use workflow.NewEngine()
│
└─ Calling a remote server?
   └─ Use client.NewHTTPClient()
```
