# Checkpointing and Resume

Checkpointing captures the full state of an execution so it can survive
process restarts. When a workflow suspends (signal wait, sleep, pause) or
completes, the engine saves a checkpoint. A fresh process can later load
that checkpoint and resume exactly where it left off.

## The Checkpointer interface

```go
type Checkpointer interface {
    SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error
    LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error)
    DeleteCheckpoint(ctx context.Context, executionID string) error
}
```

The engine calls `SaveCheckpoint` after each step completes and when
branches suspend. `LoadCheckpoint` is called when resuming with
`ResumeFrom`. `DeleteCheckpoint` is available for cleanup but the engine
does not call it automatically.

## Built-in implementations

### NullCheckpointer (default)

Does nothing. No state is persisted. Resume is not possible.

```go
checkpointer := workflow.NewNullCheckpointer()
```

This is the default when no checkpointer is configured — fine for
one-shot scripts and simple tests.

### FileCheckpointer

Persists checkpoints as JSON files in a directory. Good for development,
CLI tools, and single-process deployments.

```go
checkpointer, err := workflow.NewFileCheckpointer("executions")
if err != nil {
    log.Fatal(err)
}
```

Each execution gets a file named `<execution-id>.json` in the given
directory.

### MemoryCheckpointer (testing)

An in-memory implementation from the `workflowtest` package. Useful for
tests that need to inspect checkpoint state.

```go
import "github.com/deepnoodle-ai/workflow/workflowtest"

cp := workflowtest.NewMemoryCheckpointer()

// After execution, inspect stored checkpoints
for id, checkpoint := range cp.Checkpoints() {
    fmt.Printf("Execution %s: status=%s\n", id, checkpoint.Status)
}
```

## Configuring a checkpointer

Pass the checkpointer when creating an execution:

```go
exec, err := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(checkpointer),
)
```

## Resuming an execution

To resume from a prior execution's checkpoint, create a new `Execution`
with the same workflow and registry, then pass the prior execution ID:

```go
// Create a fresh Execution against the same workflow
exec, err := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(cp),
    workflow.WithExecutionID(priorExecID), // use the same ID
)
if err != nil {
    log.Fatal(err)
}

// Resume from the checkpoint
result, err := exec.Execute(ctx, workflow.ResumeFrom(priorExecID))
```

Or with the Runner (recommended for production):

```go
runner := workflow.NewRunner()
result, err := runner.Run(ctx, exec, workflow.WithResumeFrom(priorExecID))
```

`ResumeFrom` loads the checkpoint and restores branch positions, state
variables, join state, and wait state. If no checkpoint exists for the
given ID, the execution starts fresh — this makes resume-or-run a single
code path.

## What's in a checkpoint

The `Checkpoint` struct captures everything needed to restore an execution:

| Field | Description |
|-------|-------------|
| `SchemaVersion` | Format version for forward compatibility |
| `ExecutionID` | Unique execution identifier |
| `WorkflowName` | Name of the workflow being executed |
| `Status` | Current execution status |
| `Inputs` | Original workflow inputs |
| `Outputs` | Computed outputs (populated on completion) |
| `BranchStates` | Per-branch state: variables, current step, wait state, activity history |
| `JoinStates` | Which branches have arrived at each join point |
| `StartedAt` / `FinishedAt` | Timing metadata |

Checkpoints are serialized as JSON. The `SchemaVersion` field
(currently `1`) ensures old checkpoints can be detected and handled.

## Fenced checkpointing

In distributed systems, multiple workers might try to run the same
execution. Fencing prevents a stale worker (one that lost its lease)
from overwriting a checkpoint that a newer worker is managing.

```go
checkpointer := workflow.WithFencing(inner, func(ctx context.Context) error {
    if !leaseManager.StillHoldsLease(ctx, workerID) {
        return fmt.Errorf("worker %s lost lease", workerID)
    }
    return nil
})
```

`WithFencing` wraps any `Checkpointer` with a pre-save check. If the
check function returns an error, `SaveCheckpoint` returns
`ErrFenceViolation`. Fence violations are special:

- They bypass retry handlers (non-retryable)
- They bypass catch handlers (non-catchable)
- The execution fails immediately

This ensures a stale worker stops promptly rather than continuing to
process work that another worker has taken over.

## Writing a custom checkpointer

For production, you'll typically implement `Checkpointer` backed by a
database. The checkpoint is a JSON-serializable struct, so the
implementation is straightforward:

```go
type PostgresCheckpointer struct {
    db *sql.DB
}

func (p *PostgresCheckpointer) SaveCheckpoint(ctx context.Context, cp *workflow.Checkpoint) error {
    data, err := json.Marshal(cp)
    if err != nil {
        return err
    }
    _, err = p.db.ExecContext(ctx,
        `INSERT INTO checkpoints (execution_id, data) VALUES ($1, $2)
         ON CONFLICT (execution_id) DO UPDATE SET data = $2`,
        cp.ExecutionID, data,
    )
    return err
}

func (p *PostgresCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*workflow.Checkpoint, error) {
    var data []byte
    err := p.db.QueryRowContext(ctx,
        `SELECT data FROM checkpoints WHERE execution_id = $1`, executionID,
    ).Scan(&data)
    if errors.Is(err, sql.ErrNoRows) {
        return nil, nil // convention: nil, nil = not found
    }
    if err != nil {
        return nil, err
    }
    var cp workflow.Checkpoint
    return &cp, json.Unmarshal(data, &cp)
}

func (p *PostgresCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
    _, err := p.db.ExecContext(ctx,
        `DELETE FROM checkpoints WHERE execution_id = $1`, executionID,
    )
    return err
}
```

Key conventions:
- `LoadCheckpoint` returns `(nil, nil)` when no checkpoint exists — not an error.
- `SaveCheckpoint` should upsert (create or replace).
- Use row-level locking or optimistic concurrency if multiple processes
  may write concurrently to the same execution's checkpoint.

## Checkpoint lifecycle

A typical production flow looks like this:

1. **Start**: `NewExecution` + `Execute` — engine saves a checkpoint after
   each step completes.
2. **Suspend**: workflow hits a `Sleep`, `WaitSignal`, or `Pause` step — the
   engine saves a final checkpoint with the suspension state and returns.
3. **External trigger**: a signal arrives, a timer fires, or an operator
   unpauses — this happens outside the library.
4. **Resume**: a new `Execution` is created and `Execute` is called with
   `ResumeFrom(execID)` — the engine loads the checkpoint and continues
   from where each branch left off.
5. **Complete**: the execution finishes — the consumer may delete or archive
   the checkpoint.

## Activity logging

Separate from checkpointing, `ActivityLogger` records an audit trail of
activity executions:

```go
type ActivityLogger interface {
    LogActivity(ctx context.Context, entry *ActivityLogEntry) error
    GetActivityHistory(ctx context.Context, executionID string) ([]*ActivityLogEntry, error)
}
```

Built-in implementations:

```go
// File-based logger
logger := workflow.NewFileActivityLogger("logs")

// No-op logger (default)
logger := workflow.NewNullActivityLogger()
```

Configure it on the execution:

```go
exec, err := workflow.NewExecution(wf, reg,
    workflow.WithActivityLogger(logger),
)
```

Each activity execution produces an `ActivityLogEntry` with the execution ID,
step name, activity name, parameters, result, error, and timing. This is
useful for debugging, compliance, and auditing — but it is not required for
the engine to function.
