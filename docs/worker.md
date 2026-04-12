# Worker package

The `github.com/deepnoodle-ai/workflow/experimental/worker` package
turns the in-process workflow engine into a durable, queue-backed
runner. It gives you a claim loop, a heartbeat lease, a reaper for
stuck runs, and panic recovery — so a fleet of worker processes can
safely share a single queue and pick up where a crashed peer left
off.

The package is small on purpose. It owns coordination, not storage or
domain logic:

- **You provide** a `QueueStore` (persistence) and a `Handler` (how to
  turn a claimed spec into a workflow execution).
- **The worker owns** the claim loop, heartbeat goroutine, reaper,
  panic recovery, and detached finalization.

Pair this package with
`github.com/deepnoodle-ai/workflow/experimental/store/postgres` for a
Postgres-backed store, or implement `QueueStore` against whatever
persistence you already run (Redis, DynamoDB, an in-memory test
store, etc.). An in-memory store for tests and local development
lives at
`github.com/deepnoodle-ai/workflow/experimental/worker/memstore`.

## Installation

The worker is a separate module so the root `workflow` module can
stay stdlib-only.

```
go get github.com/deepnoodle-ai/workflow/experimental/worker
```

If you are working inside the main `workflow` repository, the
top-level `go.work` file already wires the submodules together — no
extra setup needed.

## The big picture

A worker goes through this loop for every run:

```
 Enqueue ──► ClaimQueued ──► Handler.Handle ──► Complete
              (attempt++)     (heartbeat)        (terminal
              (lease taken)                       or dormant)
```

At any point, if a worker process dies, the **reaper** running on
surviving workers notices the stale heartbeat and either reclaims the
run (so another worker picks it up) or, if attempts are exhausted,
dead-letters it to `StatusFailed`.

All writes to a claimed run are fenced on `(WorkerID, Attempt)`. A
worker that has lost its lease — because it was reaped, or because
its network blinked out long enough for the heartbeat to lapse — will
see `worker.ErrLeaseLost` on its next store write and can safely
drop the run.

## Run lifecycle and statuses

`worker.Status` values:

| Status            | Meaning                                                              |
| ----------------- | -------------------------------------------------------------------- |
| `StatusQueued`    | Waiting to be claimed.                                               |
| `StatusRunning`   | Claimed by a worker, actively executing under a heartbeat lease.    |
| `StatusCompleted` | Terminal success.                                                    |
| `StatusFailed`    | Terminal failure.                                                    |
| `StatusSuspended` | Dormant. Waiting on a signal, sleep, or pause. **Not** reaped.       |

`StatusSuspended` is a first-class state. The reaper ignores
suspended runs on purpose: the external trigger (signal delivery,
wake-up timer, operator unpause) is the thing that re-enqueues them,
not a stale-heartbeat timeout. See the suspension section below.

## Writing a Handler

A `Handler` receives a `Claim` and returns an `Outcome`. The claim
carries the run's `ID`, opaque `Spec` bytes you chose at `Enqueue`
time, and the 1-based `Attempt` counter.

```go
import (
    "context"
    "encoding/json"

    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/experimental/store/postgres"
    "github.com/deepnoodle-ai/workflow/experimental/worker"
)

type runSpec struct {
    Workflow *workflow.Workflow `json:"workflow"`
    Inputs   map[string]any     `json:"inputs"`
}

func handleRun(pgStore *postgres.Store, reg *workflow.ActivityRegistry) worker.HandlerFunc {
    return func(ctx context.Context, c *worker.Claim) worker.Outcome {
        var spec runSpec
        if err := json.Unmarshal(c.Spec, &spec); err != nil {
            return worker.Outcome{
                Status:       worker.StatusFailed,
                ErrorMessage: "decode spec: " + err.Error(),
            }
        }

        lease := worker.Lease{RunID: c.ID, WorkerID: "w", Attempt: c.Attempt}

        exec, err := workflow.NewExecution(spec.Workflow, reg,
            workflow.WithExecutionID(c.ID),
            workflow.WithCheckpointer(pgStore.NewCheckpointer(lease)),
            workflow.WithStepProgressStore(pgStore),
            workflow.WithActivityLogger(pgStore),
        )
        if err != nil {
            return worker.Outcome{Status: worker.StatusFailed, ErrorMessage: err.Error()}
        }

        var opts []workflow.ExecuteOption
        if c.Attempt > 1 {
            opts = append(opts, workflow.ResumeFrom(c.ID))
        }
        result, err := exec.Execute(ctx, opts...)
        if err != nil {
            return worker.Outcome{Status: worker.StatusFailed, ErrorMessage: err.Error()}
        }

        switch result.Status {
        case workflow.ExecutionStatusCompleted:
            body, _ := json.Marshal(result.Outputs)
            return worker.Outcome{Status: worker.StatusCompleted, Result: body}
        case workflow.ExecutionStatusSuspended, workflow.ExecutionStatusPaused:
            body, _ := json.Marshal(result.Suspension)
            return worker.Outcome{Status: worker.StatusSuspended, Result: body}
        default:
            msg := ""
            if result.Error != nil {
                msg = result.Error.Error()
            }
            return worker.Outcome{Status: worker.StatusFailed, ErrorMessage: msg}
        }
    }
}
```

Things to know about Handlers:

- **The `ctx` is scoped to the run.** It is cancelled when the
  worker's parent context is cancelled, when the run timeout expires,
  or when the heartbeat detects lease loss. Always honor it.
- **Do not call `QueueStore` methods directly.** The worker writes the
  terminal status for you based on the returned `Outcome`.
- **Spec is opaque.** The worker never inspects it. Pick any format
  you like — JSON is common, but protobuf or raw bytes work just as
  well.
- **Panics become `StatusFailed`.** One bad run cannot take down the
  worker; the panic value lands in `Outcome.ErrorMessage`.
- **Use `c.Attempt` to pick run vs. resume.** First attempt is `1`
  (fresh run); any higher value indicates the run was reclaimed and
  you should `ResumeFrom` the prior execution ID.

## Constructing and running a Worker

Only `QueueStore` and `Handler` are required; every other field has
a sensible default.

```go
w, err := worker.New(worker.Config{
    QueueStore: pgStore,
    Handler:    handleRun(pgStore, registry),

    // Optional — these are the defaults.
    Concurrency:       10,
    PollInterval:      5 * time.Second,
    HeartbeatInterval: 30 * time.Second,
    StaleAfter:        2 * time.Minute,
    ReaperInterval:    60 * time.Second,
    MaxAttempts:       3,
    RunTimeout:        30 * time.Minute,
    Logger:            slog.Default(),
})
if err != nil {
    log.Fatal(err)
}

ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()

if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
    log.Fatal(err)
}
```

`Run` blocks until the context is cancelled. It launches the reaper,
starts the claim loop, and — for each claimed run — spawns a pair of
goroutines: one for the handler and one for the heartbeat.

### Configuration reference

| Field               | Default           | Purpose                                                                                  |
| ------------------- | ----------------- | ---------------------------------------------------------------------------------------- |
| `QueueStore`        | required          | Backing persistence.                                                                     |
| `Handler`           | required          | Executes claimed runs.                                                                   |
| `WorkerID`          | `worker-<host>-<rand>` | Stable identifier used for lease fencing. Set this if you need deterministic IDs.   |
| `Concurrency`       | `10`              | Max in-flight runs per worker process.                                                   |
| `PollInterval`      | `5s`              | How often the claim loop wakes when idle. `Notify()` shortcuts this.                     |
| `HeartbeatInterval` | `30s`             | How often an active run's lease is refreshed.                                            |
| `StaleAfter`        | `2m`              | Threshold at which a run with no recent heartbeat is reclaimed or dead-lettered. Must be strictly greater than `HeartbeatInterval`. |
| `ReaperInterval`    | `60s`             | How often the reaper scans for stale runs.                                               |
| `MaxAttempts`       | `3`               | Runs reclaimed this many times are dead-lettered instead of re-queued.                   |
| `RunTimeout`        | `30m`             | Wall-clock timeout for a single `Handle` invocation.                                     |
| `Logger`            | discard           | Structured logger.                                                                       |
| `Clock`             | `time.Now`        | Injected time source for tests.                                                          |

`worker.New` rejects a `StaleAfter` that is not strictly greater than
`HeartbeatInterval`. This is deliberate — if they were equal, a
single missed heartbeat would look stale and the reaper could fight
healthy workers.

### Enqueueing work

Enqueueing is a plain `QueueStore.Enqueue` call — the worker is not
involved:

```go
err := pgStore.Enqueue(ctx, worker.NewRun{
    ID:   workflow.NewExecutionID(),
    Spec: mustJSON(runSpec{Workflow: wf, Inputs: inputs}),
})
```

If your producer shares a process with a `Worker`, call `w.Notify()`
right after `Enqueue` to skip the poll interval and pick the run up
immediately.

## Lease fencing

Every claim produces a `Lease{RunID, WorkerID, Attempt}`. The
`QueueStore` contract says that `Heartbeat`, `Complete`, and any
lease-protected checkpoint writes must reject operations whose lease
tuple does not match the current claim on the run.

Rejected writes surface as `worker.ErrLeaseLost`. Treat it as normal:

- **Stop writing for that run.** Another worker has taken over, or
  the run has been dead-lettered.
- **Do not retry.** Your claim is gone.
- **Log it at info level.** It is expected during reaper reclaims and
  transient network partitions.

The worker is careful here. The final status write runs under a
fresh 30-second `context.Background()` — even if the run's own
context was just cancelled by the heartbeat detecting a lease loss,
the `Complete` call still gets through, and lease fencing stops it
from trampling another worker's progress.

## Suspension and re-enqueueing

When a handler returns `StatusSuspended`, the run row is durably
marked as suspended and the reaper will **not** touch it. The
external event that unblocks the workflow is responsible for getting
it back onto the queue.

The typical pattern:

1. Handler runs the execution; it ends with
   `ExecutionStatusSuspended` and a populated `SuspensionInfo`.
2. Handler serializes the suspension info into `Outcome.Result`
   alongside `StatusSuspended`.
3. Your application — separately — watches for the trigger
   (signal delivery, timer, operator unpause) and re-enqueues the
   same run ID with a new spec or the same spec. The next claim
   arrives with `Attempt > 1`, and your handler resumes from the
   stored checkpoint.

See [`docs/suspension.md`](./suspension.md) for the full picture of
what suspension means inside the workflow engine.

## The reaper

The reaper scans `StatusRunning` rows in the background on a timer.
For each row whose `heartbeat_at` is older than `StaleAfter`:

- If `attempt < MaxAttempts` → **reclaim** (transition back to
  `StatusQueued`). A surviving worker can pick the run up on the next
  claim cycle.
- If `attempt >= MaxAttempts` → **dead-letter** (transition to
  `StatusFailed` with an exhaustion error message).

The reaper passes an `excludeIDs` list of runs currently in-flight
on **this** worker, so a slow DB write cannot trick a healthy worker
into reaping its own run. This is belt-and-suspenders protection
against delayed writes, not a substitute for the lease fence.

Suspended runs are invisible to the reaper — they have no
`heartbeat_at` filter match and would not be reclaimed even if they
did.

## Graceful shutdown

`Run` stops when its context is cancelled. It waits for the reaper
goroutine and all in-flight claim goroutines before returning. The
run-handler goroutines each see their `ctx` cancelled, so they
should return promptly — the worker does not force-kill handlers.

There is no separate "drain" mode. If you need zero-interruption
deployments, hand shutdown signals to the parent context and size
`RunTimeout` so in-flight handlers can finish within your grace
window.

## The QueueStore contract

If you want to implement `QueueStore` against your own persistence,
the contract is:

- **`Enqueue`** inserts a new run in `StatusQueued` with attempt `0`.
  Returns an error if the ID already exists.
- **`ClaimQueued`** is atomic. Two workers calling it concurrently
  must never receive the same run. Returns `(nil, nil)` when empty.
  Increments `attempt` and sets `claimed_by` on the claimed row.
- **`Heartbeat`** and **`Complete`** must fence on
  `(WorkerID, Attempt)`. Return `ErrLeaseLost` on mismatch.
- **`ReclaimStale`** transitions running rows with
  `heartbeat_at < staleBefore AND attempt < maxAttempts` back to
  `StatusQueued`, honoring `excludeIDs`.
- **`DeadLetterStale`** transitions running rows with
  `heartbeat_at < staleBefore AND attempt >= maxAttempts` to
  `StatusFailed`, honoring `excludeIDs`, returning the IDs for
  observability.

Everything else — indexes, transactions, isolation level, retry on
transient errors — is up to the implementation. The Postgres store
uses `SELECT ... FOR UPDATE SKIP LOCKED` for the claim and plain
fenced `UPDATE`s for everything else; see
[`docs/postgres.md`](./postgres.md) for the details.

## Testing with memstore

`experimental/worker/memstore` is an in-process `QueueStore` intended for tests
and local development. It is goroutine-safe within a single process
but has no cross-process coordination.

```go
store := memstore.New()
_ = store.Enqueue(ctx, worker.NewRun{ID: "run-1", Spec: []byte(`{}`)})

w, _ := worker.New(worker.Config{
    QueueStore:        store,
    Handler:           worker.HandlerFunc(handle),
    PollInterval:      20 * time.Millisecond,
    HeartbeatInterval: 40 * time.Millisecond,
    StaleAfter:        200 * time.Millisecond,
    ReaperInterval:    50 * time.Millisecond,
})
go w.Run(ctx)
w.Notify()
```

`Snapshot()` returns a read-only copy of all runs for assertions, and
`SetClock(func)` lets you synthesize stale heartbeats to exercise
reaper paths without waiting.
