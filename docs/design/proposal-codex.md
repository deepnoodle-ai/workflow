# Workflow Engine Proposal (Codex)

## Overview

The library already provides a strong, resumable `Execution` for a single
workflow, with path-local state, retries at the step level, and checkpointing
after activities. What it lacks is a thin, production-facing supervisor for
multiple executions: bounded concurrency, durable submission, and restart
recovery. This proposal introduces a small `Engine` (aka `Manager`) layer that
adds those guarantees without overengineering or changing the core execution
model.

This design intentionally keeps the existing `Workflow`, `Execution`, `Path`,
and activity APIs stable. The new layer is optional and lives alongside them.

## Goals

- Durable submission (inputs persisted before acknowledging submit).
- Bounded concurrency across executions.
- Crash recovery for executions marked as running/pending.
- Explicit persistence guarantees for completion and outputs.
- Minimal API surface and minimal new abstractions.

## Non-Goals

- Full distributed scheduling (leader election, sharding, or multi-tenant fairness).
- Activity-level global scheduling or work stealing.
- Event-sourced history (checkpoint-based recovery remains the default).
- Built-in metrics or tracing frameworks (callbacks only).

## Current Guarantees and Constraints

- `Execution` checkpoints after each activity via `Checkpointer`.
- Path-local state avoids shared mutable state between concurrent paths.
- Step-level retries exist and are the preferred retry mechanism.
- There is no registry of *all* executions, so recovery from process restart
  cannot list or resume previous runs.

## Proposed API

### Engine Core

```go
type EngineOptions struct {
    Store                 ExecutionStore
    Queue                 WorkQueue
    Checkpointer          Checkpointer
    ActivityLogger        ActivityLogger
    Logger                *slog.Logger
    MaxConcurrent         int
    Callbacks             ExecutionCallbacks
    ShutdownTimeout       time.Duration
    RecoveryMode          RecoveryMode
}

type RecoveryMode string

const (
    RecoveryResume RecoveryMode = "resume"
    RecoveryFail   RecoveryMode = "fail"
)

type Engine struct {
    // internal fields
}

func NewEngine(opts EngineOptions) (*Engine, error)

func (e *Engine) Start(ctx context.Context) error
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*ExecutionHandle, error)
func (e *Engine) Get(ctx context.Context, id string) (*ExecutionRecord, error)
func (e *Engine) List(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error)
func (e *Engine) Shutdown(ctx context.Context) error
```

### Submission

```go
type SubmitRequest struct {
    Workflow   *Workflow
    Inputs     map[string]any
    ExecutionID string // optional override
}

type ExecutionHandle struct {
    ID     string
    Status ExecutionStatus
}
```

`Submit` synchronously persists the request and returns an ID. The execution is
then queued for background processing.

### Persistence Contract

```go
type ExecutionRecord struct {
    ID           string
    WorkflowName string
    Status       ExecutionStatus
    Inputs       map[string]any
    Outputs      map[string]any
    Attempt      int
    CreatedAt    time.Time
    StartedAt    time.Time
    CompletedAt  time.Time
    LastError    string
    CheckpointID string
}

type ExecutionStore interface {
    Create(ctx context.Context, record *ExecutionRecord) error
    Update(ctx context.Context, record *ExecutionRecord) error
    Get(ctx context.Context, id string) (*ExecutionRecord, error)
    List(ctx context.Context, filter ExecutionFilter) ([]*ExecutionRecord, error)
}
```

`ExecutionStore` is intentionally simple. It does not attempt to solve
distributed coordination. For multi-node execution, add a leasing extension.

```go
type ExecutionLeaser interface {
    Acquire(ctx context.Context, executionID, workerID string, ttl time.Duration) (bool, error)
    Heartbeat(ctx context.Context, executionID, workerID string, ttl time.Duration) error
    Release(ctx context.Context, executionID, workerID string) error
}
```

### Queue and Concurrency

- The engine owns a `WorkQueue` implementation for execution IDs.
- A fixed-size semaphore controls the number of active executions per worker.
- `Submit` enqueues IDs; `Start` also enqueues any recoverable executions.

This design caps active execution goroutines without trying to limit path-level
goroutines inside each execution.

```go
type WorkItem struct {
    ExecutionID string
}

type WorkQueue interface {
    Enqueue(ctx context.Context, item WorkItem) error
    Dequeue(ctx context.Context) (WorkItem, error)
}
```

## Lifecycle and Guarantees

### Submission

Guarantee: `ExecutionRecord` with inputs is persisted before `Submit` returns.

Tradeoff: `Submit` may be slower but ensures replayability and auditability.

### Start/Recovery

On `Start`, the engine queries `ExecutionStore.List` for records in
`pending` or `running` states and either:

- `RecoveryResume`: resumes from checkpoint if present, otherwise starts fresh.
- `RecoveryFail`: marks as failed with a standardized error message.

Guarantee: no execution remains indefinitely in `running` after restart.

Tradeoff: resume assumes activities are idempotent at the checkpoint boundary.

## Distributed Workers

The engine can be split into two roles:

- **Orchestrator**: persists submissions and enqueues work items.
- **Worker**: dequeues work items, acquires a lease, runs/resumes executions,
  and heartbeats until completion.

This separation enables workers to be scaled independently, including
on-demand provisioning.

### Worker Loop

```go
func (w *Worker) Run(ctx context.Context) error {
    for {
        item, err := w.queue.Dequeue(ctx)
        if err != nil {
            return err
        }

        ok, err := w.leaser.Acquire(ctx, item.ExecutionID, w.id, w.leaseTTL)
        if err != nil || !ok {
            continue
        }

        w.runExecution(ctx, item.ExecutionID)
    }
}
```

### Queue Options

Two concrete queue options are expected initially:

1) **Postgres-backed queue**
   - `queue` table with `available_at`, `payload`, `attempt`, `locked_by`,
     `locked_until`.
   - `Dequeue` uses `FOR UPDATE SKIP LOCKED` to claim work.
   - Simple to run anywhere Postgres is available.

2) **Google Cloud queue**
   - Option A: **Pub/Sub** (at-least-once delivery; requires idempotent
     execution and lease checks in the store).
   - Option B: **Cloud Tasks** (push or pull, visibility timeout semantics).

Both options integrate with the same `WorkQueue` interface. Delivery semantics
are at-least-once; the lease prevents double execution.

### On-Demand Workers (sprites.dev)

If using `sprites.dev` to spin up workers on demand, the orchestrator can:

- enqueue a work item,
- request a worker from `sprites.dev`,
- the worker process starts, connects to the queue, and runs until idle,
  then terminates.

This requires no changes to the core engine beyond supporting a remote queue
and an external worker lifecycle controller.

### Completion

Guarantee: final status and outputs are persisted synchronously before the
engine marks the execution finished. If persistence fails, the execution is
considered failed even if the workflow completed in-memory.

Tradeoff: completion latency depends on store durability.

### Checkpointing

Checkpoints are delegated to the existing `Checkpointer`. The engine does not
alter current checkpoint frequency (after each activity). If a checkpoint fails,
the execution fails immediately (current behavior).

## Thread Safety Considerations

The engine must not read path-local state concurrently with path execution.
Snapshots should be passed as immutable copies from the path to the engine.
If this is not feasible, access to shared state must be guarded with locks.

The engine itself should use a single goroutine to manage its internal maps
and queues, or use a mutex with clear ownership boundaries.

## Tradeoffs and Rationale

- **No distributed execution**: simpler mental model, aligns with current
  single-process assumptions.
- **No activity-level pool**: avoids rewriting `Path.Run` into a task scheduler.
  Per-workflow concurrency remains unbounded; bounded workflow concurrency is
  the minimal safe improvement.
- **Checkpoints over event sourcing**: keeps persistence minimal and avoids
  large schema design work.

## Compatibility

- Existing users can keep calling `NewExecution` directly.
- The `Engine` is an optional layer that composes `ExecutionOptions`.
- No changes required to `Workflow`, `Step`, or `Activity` interfaces.

## Open Questions

- Should `ExecutionStore.Update` accept partial updates to reduce write volume?
- Do we need a per-execution retry policy (beyond step-level retries)?
- Should activity logs be integrated into `ExecutionStore` or remain separate?
