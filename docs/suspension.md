# Suspension model

A workflow execution can end in three states that are not "completed"
or "failed": **suspended**, **paused**, and **canceled**. Suspended
and paused are dormant — the execution is durable, the consumer is
expected to schedule a resume. Canceled is terminal.

This document covers suspended and paused. Together they're the
"needs resume" cases, and `ExecutionResult.NeedsResume()` collapses
both into one boolean.

## The three reasons

| Reason                             | Trigger                                       | What advances it                                  |
| ---------------------------------- | --------------------------------------------- | ------------------------------------------------- |
| `SuspensionReasonWaitingSignal`    | `Context.Wait` or a `WaitSignal` step         | A signal delivered to the topic, then `Resume`    |
| `SuspensionReasonSleeping`         | A `Sleep` step                                | Wall-clock time reaches `WakeAt`, then `Resume`   |
| `SuspensionReasonPaused`           | `PauseBranch` call or a `Pause` step          | `UnpauseBranch` (or in-checkpoint variant), then `Resume` |

The first two are **suspended** (`Status = ExecutionStatusSuspended`).
The third is **paused** (`Status = ExecutionStatusPaused`). Both ride
through `Suspension *SuspensionInfo` on the result; `WaitReason()`,
`Topics()`, and `NextWakeAt()` are convenience accessors.

## Lifecycle

```
                  +----------+
   Execute(ctx) ->| Running  |--success-> Completed
                  |          |--failure-> Failed
                  +----+-----+
                       |
                       | Wait/Sleep/Pause
                       v
                  +----------+
                  | Dormant  |   <-- checkpoint persisted, goroutines exited
                  +----+-----+
                       |
                       | external trigger arrives
                       v
                  +----------+
                  | Resume   |
                  +----+-----+
                       |
                       v
                  ... (loop)
```

The dormant state is **fully durable**: every variable, every wait
deadline, every pause flag is in the checkpoint. The process can
restart, the resume can run on a different worker, and the execution
picks up where it left off.

## Replay-safety contract

The single most important rule for activities that call `Wait`:

> Code that runs **before** `Wait` will run again on resume.

The wait sentinel triggers an unwind that the engine catches at the
activity boundary. The activity goroutine exits, the checkpoint is
persisted, and the execution ends suspended. When `Resume` runs, the
engine re-enters the same step and the same activity. Everything in
the activity body that ran before the `Wait` runs again.

Use `ctx.History().RecordOrReplay` to memoize side effects:

```go
func myActivity(ctx workflow.Context, p Params) (any, error) {
    // Runs once. On replay, returns the cached value from the
    // checkpoint without re-executing the closure.
    requestIDAny, _ := ctx.History().RecordOrReplay("request_id", func() (any, error) {
        return externalAPI.CreateRequest(ctx, p)
    })
    requestID := requestIDAny.(string)

    // Suspends here. On resume, externalAPI.CreateRequest is NOT
    // called again — `requestID` comes from the cache.
    payload, err := ctx.Wait("callback-" + requestID, 24 * time.Hour)
    if err != nil {
        return nil, err
    }

    // Runs only once, after the signal arrives.
    return processPayload(payload), nil
}
```

The `History` cache is per-step: it lives on `BranchState.ActivityHistory`
and is cleared when the step advances past the activity. There is no
cross-step leakage.

## Scheduling a resume from `WakeAt`

```go
result, _ := runner.Run(ctx, exec)

if result.NeedsResume() {
    if wakeAt, ok := result.NextWakeAt(); ok {
        // Wall-clock resume — sleeps and signal-wait timeouts.
        time.AfterFunc(time.Until(wakeAt), func() {
            // Build a fresh execution from the checkpointer and
            // call runner.Run with WithResumeFrom(exec.ID()).
        })
    }

    for _, topic := range result.Topics() {
        // Signal-wait resume — register a listener that calls
        // signalStore.Deliver(topic, payload) and then schedules a
        // resume of exec.ID().
    }
}
```

Wall-clock and signal triggers are not mutually exclusive — a single
execution can be parked on a signal *with* a timeout. The `Topics()`
slice and the `NextWakeAt()` deadline are both populated. Whichever
trigger fires first wins; the other becomes a no-op when the
execution next checkpoints.

## Dominant reason precedence

When multiple branches are dormant for different reasons, the
result reports a single dominant reason via `Suspension.Reason`.
The full per-branch breakdown is in `Suspension.SuspendedBranches`.

The precedence rule, from highest to lowest:

1. `SuspensionReasonPaused` — operator intent always wins. If any
   branch was explicitly paused, the execution reports paused.
2. `SuspensionReasonWaitingSignal` — outranks sleep because a
   signal can resolve faster than a wall-clock wake.
3. `SuspensionReasonSleeping` — only the dominant reason if every
   dormant branch is sleeping.

The dominant-reason rule is a hint to the consumer: "schedule the
right kind of resume." Consumers that need to handle multiple
reasons in one execution should iterate `SuspendedBranches`.
