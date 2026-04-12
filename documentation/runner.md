# Runner

The Runner is the recommended entry point for production consumers. It
composes lifecycle concerns — heartbeating, timeouts, crash recovery, and
completion hooks — into a single `Run` call on top of the core `Execute`
method.

For one-shot scripts and tests, calling `exec.Execute(ctx)` directly is
fine. The Runner adds the operational scaffolding you need for long-running,
distributed, or recoverable workflows.

## Basic usage

```go
runner := workflow.NewRunner(
    workflow.WithRunnerLogger(slog.Default()),
    workflow.WithDefaultTimeout(30 * time.Minute),
)

exec, _ := workflow.NewExecution(wf, reg,
    workflow.WithInputs(inputs),
    workflow.WithCheckpointer(cp),
)

result, err := runner.Run(ctx, exec)
if err != nil {
    log.Fatal(err) // infrastructure failure
}
if result.Completed() {
    fmt.Println(result.Outputs)
}
```

## Runner options

Options set on `NewRunner` apply to every `Run` call:

| Option | Description |
|--------|-------------|
| `WithRunnerLogger(logger)` | Structured logger for Runner-level events |
| `WithDefaultTimeout(d)` | Default timeout for every Run (negative disables) |

## Run options

Options on individual `Run` calls override Runner defaults:

| Option | Description |
|--------|-------------|
| `WithResumeFrom(execID)` | Resume from a prior checkpoint; falls back to fresh run if not found |
| `WithHeartbeat(config)` | Periodic liveness check |
| `WithCompletionHook(hook)` | Called after successful completion |
| `WithRunTimeout(d)` | Per-call timeout override |

## Resume-or-run

`WithResumeFrom` implements a single code path for both fresh runs and
crash recovery:

```go
result, err := runner.Run(ctx, exec,
    workflow.WithResumeFrom(priorExecutionID),
)
```

If a checkpoint exists for the prior ID, the execution resumes from where
it left off. If no checkpoint exists, the execution starts fresh. This means
your worker loop can always call `Run` the same way regardless of whether
this is the first attempt or a retry after a crash.

## Heartbeating

Heartbeat proves liveness in distributed systems. Configure it with a
function that renews a distributed lease:

```go
result, err := runner.Run(ctx, exec,
    workflow.WithHeartbeat(&workflow.HeartbeatConfig{
        Interval: 10 * time.Second,
        Func: func(ctx context.Context) error {
            return leaseManager.Renew(ctx, jobID, workerID)
        },
    }),
)
```

The heartbeat runs in a separate goroutine. If the function returns an
error (e.g., lease lost), the Runner cancels the execution's context.
Activities that respect context cancellation will stop promptly.

This integrates with fenced checkpointing — if a worker loses its lease,
the heartbeat failure cancels the execution before it can write a stale
checkpoint.

### Heartbeat + fencing pattern

```go
// Fenced checkpointer: rejects saves if lease is lost
cp := workflow.WithFencing(baseCheckpointer, func(ctx context.Context) error {
    if !leaseManager.StillHoldsLease(ctx, workerID) {
        return fmt.Errorf("lease lost")
    }
    return nil
})

exec, _ := workflow.NewExecution(wf, reg, workflow.WithCheckpointer(cp))

// Heartbeat: proactively cancels execution if lease is lost
result, err := runner.Run(ctx, exec,
    workflow.WithHeartbeat(&workflow.HeartbeatConfig{
        Interval: 10 * time.Second,
        Func: func(ctx context.Context) error {
            return leaseManager.Renew(ctx, jobID, workerID)
        },
    }),
)
```

The heartbeat provides fast detection (cancel within one interval). The
fence provides correctness (even if the heartbeat goroutine is slow, stale
checkpoints won't overwrite fresh ones).

## Completion hooks

Completion hooks run after a successful execution and produce follow-up
workflow descriptors:

```go
result, err := runner.Run(ctx, exec,
    workflow.WithCompletionHook(func(ctx context.Context, result *workflow.ExecutionResult) ([]workflow.FollowUpSpec, error) {
        count, _ := result.Outputs["record_count"].(int)
        if count > 100 {
            return []workflow.FollowUpSpec{{
                WorkflowName: "generate-report",
                Inputs:       map[string]any{"record_count": count},
                Metadata:     map[string]any{"priority": "high"},
            }}, nil
        }
        return nil, nil
    }),
)

// Follow-ups are attached to the result
for _, followUp := range result.FollowUps {
    fmt.Printf("Follow-up: %s with inputs %v\n", followUp.WorkflowName, followUp.Inputs)
}
```

### FollowUpSpec

```go
type FollowUpSpec struct {
    WorkflowName string         // name of the workflow to run next
    Inputs       map[string]any // inputs for the follow-up workflow
    Metadata     map[string]any // for routing, dedup, prioritization
}
```

Hooks run synchronously after completion. The consumer owns persisting
follow-ups to their durable outbox — the library does not execute them
automatically. This keeps the library a pure execution engine while enabling
workflow chaining patterns.

If a hook returns an error, it is logged but does not change the execution
result. The partial follow-up list is still attached to `result.FollowUps`.

## Result interpretation

The Runner's `Run` method returns `(*ExecutionResult, error)`:

- **`error` non-nil** = infrastructure failure (couldn't start, heartbeat
  setup failed, etc.). Result is nil.
- **`error` nil** = execution ran. Inspect the result:

```go
result, err := runner.Run(ctx, exec)
if err != nil {
    return err // infrastructure problem
}

switch {
case result.Completed():
    // Success — read outputs
    fmt.Println(result.Outputs)

case result.Failed():
    // Workflow-level failure — read the error
    fmt.Printf("failed: %s\n", result.Error.Cause)

case result.Suspended():
    // Waiting on a signal or sleep — schedule a resume
    topics := result.Topics()
    wakeAt, _ := result.NextWakeAt()

case result.Paused():
    // Manually paused — notify an operator
    for _, b := range result.Suspension.SuspendedBranches {
        fmt.Printf("branch %s paused: %s\n", b.BranchID, b.PauseReason)
    }
}
```

## Timeouts

Timeouts prevent runaway executions:

```go
// Default timeout for all runs
runner := workflow.NewRunner(workflow.WithDefaultTimeout(30 * time.Minute))

// Override per-run
result, err := runner.Run(ctx, exec, workflow.WithRunTimeout(5 * time.Minute))

// Disable timeout for a specific run
result, err := runner.Run(ctx, exec, workflow.WithRunTimeout(-1))
```

When a timeout fires, the execution's context is canceled. Activities that
respect context cancellation will stop. The execution result will have
`Status = Failed`.

## Production worker pattern

A typical worker loop using the Runner:

```go
runner := workflow.NewRunner(
    workflow.WithRunnerLogger(logger),
    workflow.WithDefaultTimeout(30 * time.Minute),
)

for job := range jobQueue {
    exec, err := workflow.NewExecution(job.Workflow, registry,
        workflow.WithInputs(job.Inputs),
        workflow.WithCheckpointer(checkpointer),
        workflow.WithSignalStore(signalStore),
        workflow.WithExecutionID(job.ExecutionID),
    )
    if err != nil {
        logger.Error("invalid execution", "error", err)
        continue
    }

    result, err := runner.Run(ctx, exec,
        workflow.WithResumeFrom(job.ExecutionID),
        workflow.WithHeartbeat(&workflow.HeartbeatConfig{
            Interval: 10 * time.Second,
            Func:     func(ctx context.Context) error { return renewLease(ctx, job.ID) },
        }),
        workflow.WithCompletionHook(enqueueFollowUps),
    )

    if err != nil {
        logger.Error("infrastructure failure", "job", job.ID, "error", err)
        continue
    }

    if result.NeedsResume() {
        scheduleResume(job.ExecutionID, result.Suspension)
    }
}
```
