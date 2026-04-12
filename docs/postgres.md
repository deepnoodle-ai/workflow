# Postgres package

The `github.com/deepnoodle-ai/workflow/experimental/store/postgres`
package is a Postgres-backed `Store` that satisfies every persistence
interface the workflow engine and its worker need:

- [`worker.QueueStore`](./worker.md) — the run queue, claim loop,
  heartbeat, reaper, and terminal state.
- `workflow.Checkpointer` — lease-fenced checkpoint persistence for
  durable resume.
- `workflow.StepProgressStore` — per-step observability for UIs and
  dashboards.
- `workflow.ActivityLogger` — append-only activity history.

One pool, one schema, one place to look when something goes wrong.

The package depends only on `jackc/pgx/v5` so the root `workflow`
module can stay stdlib-only. It lives in its own Go module so you can
depend on it (or not) without pulling pgx into consumers that use a
different database.

## Installation

```
go get github.com/deepnoodle-ai/workflow/experimental/store/postgres
```

Inside this repository the top-level `go.work` already wires things
up, so you don't need to do anything extra to build the package
locally.

## Quick start

```go
package main

import (
    "context"
    "log"

    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/deepnoodle-ai/workflow/experimental/store/postgres"
)

func main() {
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, "postgres://user:pass@localhost:5432/workflow")
    if err != nil {
        log.Fatal(err)
    }
    defer pool.Close()

    store := postgres.New(pool)
    if err := store.Migrate(ctx); err != nil {
        log.Fatal(err)
    }

    // store now satisfies worker.QueueStore, workflow.StepProgressStore,
    // workflow.ActivityLogger, and exposes NewCheckpointer(lease).
}
```

- **You own the pool.** `postgres.New` takes an already-constructed
  `*pgxpool.Pool` and does not close it for you.
- **`Migrate` is idempotent.** It runs the embedded `schema.sql`
  (`CREATE TABLE IF NOT EXISTS`) on every call. Safe to call on every
  startup, skip it in production if you prefer managing migrations
  externally.
- **`WithLogger(slog.Logger)`** attaches a structured logger;
  otherwise the store logs to `io.Discard`.

## Schema

`Migrate` creates three tables.

### `workflow_runs`

The durable queue and state table. One row per run, including the
checkpoint blob.

| Column          | Type          | Notes                                                            |
| --------------- | ------------- | ---------------------------------------------------------------- |
| `id`            | `TEXT` PK     | The stable run identifier, also used as the workflow `ExecutionID`. |
| `spec`          | `BYTEA`       | Opaque payload from `Enqueue`. The worker never inspects it.     |
| `status`        | `TEXT`        | One of the `worker.Status` values.                               |
| `attempt`       | `INTEGER`     | 0 for queued, increments on each claim.                          |
| `claimed_by`    | `TEXT`        | `WorkerID` of the current leaseholder, or `''`.                 |
| `heartbeat_at`  | `TIMESTAMPTZ` | Refreshed by the worker's heartbeat goroutine.                   |
| `checkpoint`    | `BYTEA`       | JSON-encoded `workflow.Checkpoint`.                              |
| `result`        | `BYTEA`       | Opaque terminal/dormant payload from `Outcome.Result`.           |
| `error_message` | `TEXT`        | Failure reason for `StatusFailed`.                               |
| `created_at`    | `TIMESTAMPTZ` | `NOW()` at enqueue time.                                         |
| `started_at`    | `TIMESTAMPTZ` | First claim timestamp.                                           |
| `completed_at`  | `TIMESTAMPTZ` | Set when the run reaches a terminal status.                      |

Indexes:

- `(status, created_at)` — claim loop ordering.
- `(status, heartbeat_at)` — reaper scans.

### `workflow_step_progress`

One row per `(execution_id, step_name, branch_id)`; the latest
status update wins via `ON CONFLICT ... DO UPDATE`. A step that runs
on two branches produces two rows.

Use it to power a UI that watches workflow progress — the row stores
`status`, `activity`, `attempt`, `started_at`, `finished_at`,
`error`, and a JSONB `detail` blob for whatever extra context the
engine emits.

### `workflow_activity_log`

Append-only log of every activity invocation with its parameters,
result, error, start time, and duration. Keyed by a stable
`entry.ID`. Good for audit trails, replay analysis, and
post-mortem debugging.

Indexed by `(execution_id, start_time)` so pulling the full history
for one run is a single range scan.

## Using the store

A single `*postgres.Store` is usually handed to four places in a
running system: the worker, each workflow execution's checkpointer,
its progress store, and its activity logger.

### As a `QueueStore`

Pass the store straight to `worker.New`:

```go
w, _ := worker.New(worker.Config{
    QueueStore: store,
    Handler:    myHandler,
})
```

`Enqueue`, `ClaimQueued`, `Heartbeat`, `Complete`, `ReclaimStale`,
and `DeadLetterStale` all live on the store and do what the
[`QueueStore` contract](./worker.md#the-queuestore-contract) requires.

The claim uses `SELECT ... FOR UPDATE SKIP LOCKED` inside a short
transaction, so multiple workers claiming concurrently never receive
the same row. No advisory locks, no polling backoff fights — the
oldest queued row wins.

### As a lease-fenced `Checkpointer`

`Store.NewCheckpointer(lease)` returns a `workflow.Checkpointer`
whose writes are fenced on `(claimed_by, attempt)`. Hand one to each
execution when you construct it:

```go
lease := worker.Lease{RunID: c.ID, WorkerID: w.ID(), Attempt: c.Attempt}
cp := store.NewCheckpointer(lease)

exec, err := workflow.NewExecution(wf, registry,
    workflow.WithExecutionID(c.ID),
    workflow.WithCheckpointer(cp),
)
```

- **`SaveCheckpoint`** writes under the lease. A worker that has
  lost its claim sees `worker.ErrLeaseLost` on the next save and
  the engine surfaces the error to the handler — which should treat
  it as "my lease is gone, stop" and return cleanly.
- **`LoadCheckpoint`** is intentionally **unfenced**. A fresh
  attempt must be able to read a prior attempt's snapshot,
  regardless of who wrote it.
- **`DeleteCheckpoint`** clears the `checkpoint` column without
  deleting the run row.

Checkpoints are stored as JSON-encoded `workflow.Checkpoint` blobs in
the `checkpoint` column — a single column update, no extra tables,
no version history. The checkpointer will refuse to load a snapshot
written by a newer library version (`SchemaVersion`
compatibility check) and will return `workflow.ErrNoCheckpoint` when
the row exists but has `NULL` data — the signal the engine needs to
start fresh.

### As a `StepProgressStore`

Pass the store to `workflow.WithStepProgressStore` and the engine
will emit progress updates as steps run. Upserts are keyed by
`(execution_id, step_name, branch_id)`; each update overwrites the
previous row for that key. The `detail` field is stored as JSONB so
you can query into it with standard SQL.

Step progress writes are fire-and-forget from the engine's side — a
dropped write will not block the workflow. Transient errors are
logged and the next update replaces the row anyway.

### As an `ActivityLogger`

Pass the store to `workflow.WithActivityLogger` for an append-only
audit trail:

```go
exec, _ := workflow.NewExecution(wf, registry,
    workflow.WithCheckpointer(store.NewCheckpointer(lease)),
    workflow.WithStepProgressStore(store),
    workflow.WithActivityLogger(store),
)
```

`LogActivity` inserts a row per invocation; `GetActivityHistory`
reads them back in start-time order for a given `execution_id`.
Parameters and results are stored as JSONB, so structured queries
(`WHERE parameters->>'url' LIKE ...`) are straightforward.

## Lease fencing in practice

Every state-changing write on an active run carries
`(claimed_by, attempt)` in its `WHERE` clause. The pattern looks
like:

```sql
UPDATE workflow_runs
SET ...
WHERE id         = $1
  AND claimed_by = $2
  AND attempt    = $3;
```

If the row has been reclaimed by the reaper (attempt bumped, or
claimed_by cleared) the update matches zero rows and the store
returns `worker.ErrLeaseLost`. The worker turns that into a
graceful stop for the run.

There is one notable exception: `Complete` does **not** filter by
`status = 'running'`. This is deliberate. A handler can legitimately
write a terminal status after the run context has been cancelled,
and we want that write to land even if the run ticked past
`running` in-memory. Fencing on `(claimed_by, attempt)` is enough to
keep two workers from racing.

## Running the tests

The postgres test suite is gated on a real database. Without
`WORKFLOW_PG_DSN` the tests skip silently.

```
WORKFLOW_PG_DSN="postgres://postgres@localhost:5432/workflow_test?sslmode=disable" \
    go test ./experimental/store/postgres/...
```

The tests run `Migrate` and then `TRUNCATE` all three tables between
runs, so point them at a throwaway database. They cover:

- Enqueue → claim → heartbeat → complete round-trip.
- Heartbeat rejection under a wrong `WorkerID` or wrong `Attempt`.
- Reaper reclaim and dead-letter thresholds.
- Checkpointer round-trip including lease-lost behavior.
- Step progress upsert and activity log append/read.

## Things this package does not do

- **No signal store.** The workflow engine supports `SignalStore` for
  signal-wait patterns, but this package does not ship a
  Postgres-backed implementation. Provide one yourself if you need
  it — it is additive and independent of the tables here.
- **No metrics hooks.** Observability belongs to the logger you
  attach via `WithLogger`; if you want Prometheus or OpenTelemetry,
  wrap the store.
- **No tenancy or authorization.** The store trusts whatever IDs and
  specs you hand it. Isolation between customers, projects, or
  environments is a concern for the layer above.
- **No separate archive table.** Completed, failed, and suspended
  runs remain in `workflow_runs`. Archival or pruning is the
  operator's responsibility.
