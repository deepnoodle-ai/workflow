# Signals, Sleep, and Pause

The library provides four primitives for coordinating a workflow with external
events. All of them produce a **hard suspension**: goroutines exit, the
checkpoint is saved, and the host process can die and restart without losing
progress.

| Primitive | Trigger to resume | Use case |
|-----------|-------------------|----------|
| Signal wait (declarative) | Signal delivered to a topic | Approval gates, async callbacks |
| Signal wait (imperative) | Signal delivered to a topic | Dynamic callbacks inside activities |
| Durable sleep | Wall clock passes the deadline | Retry delays, cool-down periods |
| Pause | Operator explicitly unpauses | Manual holds, investigations |

## SignalStore

Signals are delivered through a `SignalStore`. The library ships an in-memory
implementation for development; production consumers implement a durable
version (typically backed by Postgres or Redis).

```go
type SignalStore interface {
    Send(ctx context.Context, executionID, topic string, payload any) error
    Receive(ctx context.Context, executionID, topic string) (*Signal, error)
}
```

Key properties:
- **FIFO per `(executionID, topic)`** with exactly-once consumption.
- **Store-first delivery**: `Send` writes to the store; `Wait`/`WaitSignal`
  call `Receive` before blocking. A signal delivered before the wait
  registers is not lost.

Configure it on the execution:

```go
signals := workflow.NewMemorySignalStore()

exec, err := workflow.NewExecution(wf, reg,
    workflow.WithSignalStore(signals),
)
```

## Declarative WaitSignal step

When the step graph is the right place to express "park until X arrives":

```go
{
    Name: "Await Approval",
    WaitSignal: &workflow.WaitSignalConfig{
        Topic:     "approval-${inputs.release}",  // expression template
        Timeout:   24 * time.Hour,                 // required
        Store:     "approval",                     // store signal payload here
        OnTimeout: "timeout-handler",              // optional fallback step
    },
    Next: []*workflow.Edge{{Step: "Deploy"}},
}
```

- **Topic** is an expression template evaluated when the step is entered.
  The resolved value is persisted in the checkpoint, not re-templated on
  replay. This handles "UUID-per-callback" patterns cleanly.
- **Timeout is required**. There are no unbounded waits.
- With `OnTimeout` set, a timeout routes the path to that step. Without
  it, the step fails with `ErrorTypeTimeout`, which catch handlers can
  match.
- **Store** names the branch variable where the signal payload is saved.

### Sending a signal

From outside the workflow (a webhook handler, an operator UI, another
service):

```go
err := signals.Send(ctx, executionID, "approval-v1.2.3", "alice@example.com")
```

After delivering the signal, resume the execution:

```go
exec2, err := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(cp),
    workflow.WithSignalStore(signals),
    workflow.WithExecutionID(execID),
)
runner := workflow.NewRunner()
result, err := runner.Run(ctx, exec2, workflow.WithResumeFrom(execID))
```

### Full example

```go
// Run 1: execution suspends at the WaitSignal step
exec1, _ := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(cp),
    workflow.WithSignalStore(signals),
)
execID := exec1.ID()

runner := workflow.NewRunner()
res1, _ := runner.Run(ctx, exec1)

// res1.NeedsResume() == true
// res1.Topics() == ["approval-v1.2.3"]
// res1.WaitReason() == "waiting_signal"

// Deliver the signal
signals.Send(ctx, execID, res1.Topics()[0], "approved by alice")

// Run 2: resume from checkpoint
exec2, _ := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(cp),
    workflow.WithSignalStore(signals),
    workflow.WithExecutionID(execID),
)
res2, _ := runner.Run(ctx, exec2, workflow.WithResumeFrom(execID))
// res2.Completed() == true
```

## Imperative Wait (inside activities)

When the signal topic is dynamic or the wait is part of a larger activity:

```go
func(ctx workflow.Context, params map[string]any) (any, error) {
    callbackID := generateUniqueID()
    postWebhook(ctx, callbackID)

    reply, err := ctx.Wait(callbackID, 7*24*time.Hour)
    if err != nil {
        return nil, err  // context cancellation or ErrWaitTimeout
    }
    return processReply(reply), nil
}
```

`ctx.Wait` is **durable**: if no signal is present, the activity unwinds
via a sentinel error, the branch hard-suspends, the checkpoint is saved,
and the execution returns with `Status = Suspended`. On resume, the
entire activity **re-executes from its entry point** — the second
`Wait` call finds the delivered signal and returns immediately.

### Replay safety

Any code an activity runs before a `Wait` call may execute more than
once. Wrap expensive or non-idempotent work in
`ctx.History().RecordOrReplay`:

```go
func(ctx workflow.Context, params map[string]any) (any, error) {
    h := ctx.History()

    // This LLM call runs once; on replay it returns the cached result
    plan, err := h.RecordOrReplay("plan", func() (any, error) {
        return llm.Plan(ctx, params)
    })
    if err != nil {
        return nil, err
    }

    // This webhook POST is non-idempotent; cache prevents double-posting
    _, err = h.RecordOrReplay("post-callback", func() (any, error) {
        return nil, postCallback(ctx, callbackID, plan)
    })
    if err != nil {
        return nil, err
    }

    reply, err := ctx.Wait(callbackID, 7*24*time.Hour)
    if err != nil {
        return nil, err
    }
    return processResult(plan, reply), nil
}
```

### Multiple waits in one activity

If an activity calls `Wait` more than once, each `Wait` **must** be wrapped
in `RecordOrReplay`. Without wrapping, the second suspension causes the
activity to replay from the top and re-call the first `Wait` against an
empty store — the first signal has already been consumed.

```go
h := ctx.History()
v1, err := h.RecordOrReplay("wait1", func() (any, error) {
    return ctx.Wait(topic1, time.Hour)
})
v2, err := h.RecordOrReplay("wait2", func() (any, error) {
    return ctx.Wait(topic2, time.Hour)
})
```

### ActivityHistory rules

- Errors from the recorded function are **not** cached. A failing `fn`
  followed by a retry reruns `fn`.
- History is **scoped per step**. A key "work" in step A does not collide
  with the same key in step B.
- History is **cleared** when the path advances past the activity's step.

## Durable Sleep

A sleep step suspends the branch for a fixed wall-clock duration:

```go
{
    Name:  "Cool Down",
    Sleep: &workflow.SleepConfig{Duration: 1 * time.Hour},
    Next:  []*workflow.Edge{{Step: "Retry"}},
}
```

The engine records an absolute `WakeAt` in the checkpoint. On resume:
- Before `WakeAt`: the path re-suspends with the same deadline.
- At or after `WakeAt`: the path wakes immediately.

### Scheduling the resume

When a sleep suspends the execution, `result.NextWakeAt()` returns the
deadline. The consumer is responsible for scheduling a resume job:

```go
result, _ := runner.Run(ctx, exec)
if result.NeedsResume() {
    if wakeAt, ok := result.NextWakeAt(); ok {
        scheduler.EnqueueAt(wakeAt, ResumeJob{ExecID: execID})
    }
}
```

## Pause and Unpause

Pause is a manual hold point. Unlike signals and sleeps, a paused path has
no declared resumption condition — an external actor must clear the flag.

### Declarative Pause step

A gate in the step graph:

```go
{
    Name:  "Deploy Gate",
    Pause: &workflow.PauseConfig{Reason: "awaiting deploy approval"},
    Next:  []*workflow.Edge{{Step: "Deploy"}},
}
```

A Pause step must declare at least one `Next` edge. When the step fires, it
advances `CurrentStep` past itself, so on unpause the path continues at the
successor rather than re-entering the pause.

### External (operator) pause

Pause a running execution's branch from outside:

```go
err := exec.PauseBranch("main", "operator investigation")
// returns ErrBranchNotFound if the branch ID is unknown
```

### Unpausing

For a running execution:

```go
err := exec.UnpauseBranch("main")
```

For a non-loaded execution (from a UI or CLI in a separate process):

```go
// These helpers mutate the checkpoint directly
workflow.PauseBranchInCheckpoint(ctx, checkpointer, execID, "main", "reason")
workflow.UnpauseBranchInCheckpoint(ctx, checkpointer, execID, "main")
```

After unpausing, resume the execution:

```go
exec2, _ := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(cp),
    workflow.WithExecutionID(execID),
)
result, _ := runner.Run(ctx, exec2, workflow.WithResumeFrom(execID))
```

### Pause freezes the wait clock

When a branch is paused while it has an active sleep or signal timeout,
the remaining duration is captured. On unpause, `WakeAt` is rebased so
the pause period does not consume the timeout budget.

## SuspensionInfo

When `result.NeedsResume()` is true, `result.Suspension` describes what
the execution is waiting for:

```go
type SuspensionInfo struct {
    Reason            SuspensionReason    // dominant: Paused > Sleeping > WaitingSignal
    SuspendedBranches []SuspendedBranch   // per-branch breakdown
    Topics            []string            // union of signal topics
    WakeAt            time.Time           // earliest deadline across all branches
}
```

Each suspended branch has its own detail:

```go
type SuspendedBranch struct {
    BranchID    string
    StepName    string
    Reason      SuspensionReason  // waiting_signal, sleeping, or paused
    Topic       string            // set for waiting_signal
    WakeAt      time.Time         // zero if no deadline
    PauseReason string            // set for paused
}
```

### Consumer pattern

```go
result, _ := runner.Run(ctx, exec)
if !result.NeedsResume() {
    return // completed or failed
}

// 1. Subscribe to signal topics
for _, topic := range result.Topics() {
    signalRouter.Subscribe(execID, topic)
}

// 2. Schedule a wake-up timer
if wakeAt, ok := result.NextWakeAt(); ok {
    scheduler.EnqueueAt(wakeAt, ResumeJob{ExecID: execID})
}

// 3. When the trigger fires, resume
// runner.Run(ctx, newExec, workflow.WithResumeFrom(execID))
```

## Known-good patterns

| Pattern | Approach |
|---------|----------|
| AI agent callback | Declarative `WaitSignal` when the callback ID is known at step-definition time; imperative `ctx.Wait` + UUID when the ID is dynamic. Wrap LLM calls in `RecordOrReplay`. |
| Human-in-the-loop approval | Declarative `Pause` step. Operator unpauses via `UnpauseBranchInCheckpoint`. |
| Long retry delay | Declarative `Sleep` step. Consumer schedules a delayed job at `result.NextWakeAt()`. |
| Distributed signal delivery | Postgres-backed `SignalStore` via `WithSignalStore`; the rest of the machinery works unchanged. |
