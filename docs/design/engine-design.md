# Workflow Engine Design

## Overview

This document proposes an Engine layer for the DeepNoodle workflow library. The current `Execution` struct manages a single workflow run with path-local state, step-level retries, and checkpointing after activities. What it lacks is a supervisor layer to manage multiple executions: bounded concurrency, durable submission, crash recovery, and optional distributed execution.

The Engine is an optional layer that composes with existing primitives. Users can continue calling `NewExecution` directly if they prefer.

## Goals

- Durable submission (inputs persisted before acknowledging submit)
- Bounded concurrency across executions
- Crash recovery for executions marked as running/pending
- Guaranteed final persistence (completion persisted before ack)
- Reliable manager↔worker communication with lease semantics
- Optional distributed worker support
- Minimal API surface

## Non-Goals

- Distributed coordination (leader election, sharding)
- Activity-level global scheduling or work stealing
- Event-sourced history (checkpoint-based recovery remains default)
- Built-in metrics (callbacks only)

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                           ENGINE                                │
│  ┌────────────────┐  ┌───────────────┐  ┌─────────────────────┐ │
│  │ ExecutionStore │  │   WorkQueue   │  │ ExecutionEnvironment│ │
│  │   (State)      │  │   (Flow)      │  │     (Compute)       │ │
│  └───────┬────────┘  └──────┬────────┘  └────────┬────────────┘ │
└──────────┼──────────────────┼────────────────────┼──────────────┘
           │                  │                    │
           ▼                  ▼                    ▼
    ┌─────────────┐    ┌─────────────┐    ┌──────────────────┐
    │  Postgres   │    │  Postgres   │    │  Local / Sprites │
    │  (records)  │    │  (queue)    │    │    (workers)     │
    └─────────────┘    └─────────────┘    └──────────────────┘
```

The architecture separates three concerns:

1. **ExecutionStore** - Durable storage of execution records (source of truth)
2. **WorkQueue** - Reliable delivery of execution tasks with lease semantics
3. **ExecutionEnvironment** - Where workflows actually run

---

## Core API

### Engine

```go
type Engine struct {
    store        ExecutionStore
    queue        WorkQueue
    env          ExecutionEnvironment
    checkpointer Checkpointer
    callbacks    EngineCallbacks
    logger       *slog.Logger

    workerID        string
    maxConcurrent   int
    shutdownTimeout time.Duration
    recoveryMode    RecoveryMode

    activeWg sync.WaitGroup
    stopping atomic.Bool
}

type EngineOptions struct {
    Store             ExecutionStore
    Queue             WorkQueue
    Environment       ExecutionEnvironment
    Checkpointer      Checkpointer
    Callbacks         EngineCallbacks
    Logger            *slog.Logger
    WorkerID          string        // unique identifier for this engine instance
    MaxConcurrent     int           // 0 = unlimited
    ShutdownTimeout   time.Duration // default 30s
    RecoveryMode      RecoveryMode  // resume or fail
}

func NewEngine(opts EngineOptions) (*Engine, error)

func (e *Engine) Start(ctx context.Context) error
func (e *Engine) Submit(ctx context.Context, req SubmitRequest) (*ExecutionHandle, error)
func (e *Engine) Get(ctx context.Context, id string) (*ExecutionRecord, error)
func (e *Engine) List(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error)
func (e *Engine) Cancel(ctx context.Context, id string) error
func (e *Engine) Shutdown(ctx context.Context) error
```

### Submission

```go
type SubmitRequest struct {
    Workflow    *Workflow
    Inputs      map[string]any
    ExecutionID string // optional override
}

type ExecutionHandle struct {
    ID     string
    Status EngineExecutionStatus
}
```

`Submit` synchronously persists the execution record before returning. The execution is then enqueued for background processing. If enqueue fails, the record remains in `pending` status and will be recovered on the next engine restart (see Recovery section).

**Note**: Submit does not use an outbox/transactional enqueue pattern. This means if enqueue fails after record creation, the execution stays pending until startup recovery. This tradeoff avoids outbox complexity while ensuring no execution is lost.

### Execution Records

```go
type ExecutionRecord struct {
    ID            string
    WorkflowName  string
    Status        EngineExecutionStatus
    Inputs        map[string]any
    Outputs       map[string]any
    Attempt       int        // fencing token for distributed execution
    WorkerID      string     // which worker owns this execution
    LastHeartbeat time.Time  // liveness signal from worker
    DispatchedAt  time.Time  // when dispatch mode handed off to worker
    CreatedAt     time.Time
    StartedAt     time.Time
    CompletedAt   time.Time
    LastError     string
    CheckpointID  string
}

// EngineExecutionStatus represents the engine-level execution state.
// This is distinct from the library's ExecutionStatus which tracks
// internal workflow state (paths, steps). The engine maps between them:
//   - Pending/Running map to the engine dispatching work
//   - Completed/Failed/Cancelled map from workflow completion
type EngineExecutionStatus string

const (
    EngineStatusPending   EngineExecutionStatus = "pending"
    EngineStatusRunning   EngineExecutionStatus = "running"
    EngineStatusCompleted EngineExecutionStatus = "completed"
    EngineStatusFailed    EngineExecutionStatus = "failed"
    EngineStatusCancelled EngineExecutionStatus = "cancelled"
)
```

---

## Persistence Layer

### ExecutionStore

The ExecutionStore is the source of truth for execution ownership and state.

```go
type ExecutionStore interface {
    Create(ctx context.Context, record *ExecutionRecord) error
    Get(ctx context.Context, id string) (*ExecutionRecord, error)
    List(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error)

    // ClaimExecution atomically updates status from pending to running if the
    // current attempt matches. Returns false if status is not pending or attempt
    // doesn't match. This provides distributed fencing.
    ClaimExecution(ctx context.Context, id string, workerID string, expectedAttempt int) (bool, error)

    // CompleteExecution atomically updates to completed/failed status if the
    // attempt matches. Returns false if attempt doesn't match (stale worker).
    CompleteExecution(ctx context.Context, id string, expectedAttempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) (bool, error)

    // MarkDispatched sets dispatched_at timestamp for dispatch mode tracking.
    MarkDispatched(ctx context.Context, id string, attempt int) error

    // Heartbeat updates the last_heartbeat timestamp for liveness tracking.
    Heartbeat(ctx context.Context, id string, workerID string) error

    // ListStaleRunning returns executions in running state with heartbeat older than cutoff.
    ListStaleRunning(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error)

    // ListStalePending returns executions in pending state with dispatched_at older than cutoff
    // (for dispatch mode where worker never claimed).
    ListStalePending(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error)
}

type ListFilter struct {
    WorkflowName string
    Statuses     []EngineExecutionStatus
    Limit        int
    Offset       int
}
```

### PostgresStore Implementation

```go
type PostgresStore struct {
    db *sql.DB
}

/*
CREATE TABLE workflow_executions (
    id             TEXT PRIMARY KEY,
    workflow_name  TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    inputs         JSONB NOT NULL,
    outputs        JSONB,
    attempt        INTEGER NOT NULL DEFAULT 1,
    worker_id      TEXT,
    last_heartbeat TIMESTAMPTZ,
    dispatched_at  TIMESTAMPTZ,
    last_error     TEXT,
    checkpoint_id  TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ
);

CREATE INDEX idx_executions_status ON workflow_executions(status);
CREATE INDEX idx_executions_workflow ON workflow_executions(workflow_name);
CREATE INDEX idx_executions_stale_running ON workflow_executions(last_heartbeat)
    WHERE status = 'running';
CREATE INDEX idx_executions_stale_pending ON workflow_executions(dispatched_at)
    WHERE status = 'pending' AND dispatched_at IS NOT NULL;
*/

func (s *PostgresStore) ClaimExecution(ctx context.Context, id, workerID string, expectedAttempt int) (bool, error) {
    // Fencing: only claim if status=pending AND attempt matches
    // This prevents:
    // 1. Double-claiming an already-running execution
    // 2. Stale workers claiming after attempt was incremented
    result, err := s.db.ExecContext(ctx, `
        UPDATE workflow_executions
        SET status = 'running',
            worker_id = $2,
            started_at = NOW(),
            last_heartbeat = NOW()
        WHERE id = $1 AND attempt = $3 AND status = 'pending'
    `, id, workerID, expectedAttempt)
    if err != nil {
        return false, err
    }
    rows, _ := result.RowsAffected()
    return rows > 0, nil
}

func (s *PostgresStore) CompleteExecution(ctx context.Context, id string, expectedAttempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) (bool, error) {
    // Fencing: only complete if attempt matches
    // This prevents stale workers from overwriting newer attempts
    outputsJSON, _ := json.Marshal(outputs)
    result, err := s.db.ExecContext(ctx, `
        UPDATE workflow_executions
        SET status = $2,
            outputs = $3,
            last_error = $4,
            completed_at = NOW()
        WHERE id = $1 AND attempt = $5 AND status = 'running'
    `, id, status, outputsJSON, lastError, expectedAttempt)
    if err != nil {
        return false, err
    }
    rows, _ := result.RowsAffected()
    return rows > 0, nil
}

func (s *PostgresStore) MarkDispatched(ctx context.Context, id string, attempt int) error {
    _, err := s.db.ExecContext(ctx, `
        UPDATE workflow_executions
        SET dispatched_at = NOW()
        WHERE id = $1 AND attempt = $2
    `, id, attempt)
    return err
}

func (s *PostgresStore) ListStalePending(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error) {
    // Find pending executions that were dispatched but never claimed
    rows, err := s.db.QueryContext(ctx, `
        SELECT id, workflow_name, status, inputs, outputs, attempt, worker_id,
               last_heartbeat, dispatched_at, last_error, checkpoint_id,
               created_at, started_at, completed_at
        FROM workflow_executions
        WHERE status = 'pending'
          AND dispatched_at IS NOT NULL
          AND dispatched_at < $1
    `, cutoff)
    // ... scan rows
}
```

### Persistence Guarantees

| Operation  | Guarantee                                                          |
| ---------- | ------------------------------------------------------------------ |
| Submission | Record persisted before `Submit` returns                           |
| Claim      | Fenced: only succeeds if status=pending AND attempt matches        |
| Completion | Fenced: only succeeds if attempt matches; persisted before Ack     |
| Heartbeat  | Liveness signal enables stale detection                            |
| Progress   | Delegated to existing `Checkpointer` (best-effort)                 |

**Critical invariant**: Completion is persisted synchronously before queue Ack. If persistence fails, the queue item is Nack'd for retry.

---

## Work Queue

### Interface

The WorkQueue provides at-least-once delivery with explicit lease management.

```go
type WorkItem struct {
    ExecutionID string
}

type Lease struct {
    Item      WorkItem
    Token     string    // opaque lease identifier
    ExpiresAt time.Time
}

type WorkQueue interface {
    // Enqueue adds an item to the queue.
    Enqueue(ctx context.Context, item WorkItem) error

    // Dequeue claims the next available item. Blocks until available or ctx cancelled.
    // The returned lease must be Ack'd, Nack'd, or will expire.
    Dequeue(ctx context.Context) (Lease, error)

    // Ack acknowledges successful processing. Removes the item from the queue.
    Ack(ctx context.Context, token string) error

    // Nack returns the item to the queue for retry after the specified delay.
    Nack(ctx context.Context, token string, delay time.Duration) error

    // Extend extends the lease TTL for long-running work.
    Extend(ctx context.Context, token string, ttl time.Duration) error

    // Close releases resources.
    Close() error
}
```

### Delivery Semantics

The queue provides **at-least-once delivery**. Combined with the ExecutionStore's fenced claiming and completion, this ensures:

1. Items are never lost (persisted before Enqueue returns)
2. Items may be delivered multiple times (on worker crash, lease expiry)
3. Only one worker successfully claims each execution (via fenced ClaimExecution)
4. Only the correct attempt can complete (via fenced CompleteExecution)

### PostgresQueue Implementation

```go
type PostgresQueue struct {
    db           *sql.DB
    workerID     string
    pollInterval time.Duration
    leaseTTL     time.Duration
}

/*
CREATE TABLE workflow_queue (
    id            SERIAL PRIMARY KEY,
    execution_id  TEXT NOT NULL UNIQUE,
    status        TEXT NOT NULL DEFAULT 'pending',
    visible_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    locked_by     TEXT,
    locked_until  TIMESTAMPTZ,
    attempt       INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_queue_pending ON workflow_queue(visible_at)
    WHERE status = 'pending';
CREATE INDEX idx_queue_stale ON workflow_queue(locked_until)
    WHERE status = 'processing';
*/

func (q *PostgresQueue) Dequeue(ctx context.Context) (Lease, error) {
    for {
        // First, reap any stale leases
        q.reapStaleLeases(ctx)

        var lease Lease
        err := q.db.QueryRowContext(ctx, `
            UPDATE workflow_queue
            SET status = 'processing',
                locked_by = $1,
                locked_until = NOW() + $2::interval,
                attempt = attempt + 1
            WHERE id = (
                SELECT id FROM workflow_queue
                WHERE status = 'pending' AND visible_at <= NOW()
                ORDER BY created_at
                FOR UPDATE SKIP LOCKED
                LIMIT 1
            )
            RETURNING id, execution_id, locked_until
        `, q.workerID, q.leaseTTL).Scan(&lease.Token, &lease.Item.ExecutionID, &lease.ExpiresAt)

        if err == sql.ErrNoRows {
            // No work available, poll after interval
            select {
            case <-time.After(q.pollInterval):
                continue
            case <-ctx.Done():
                return Lease{}, ctx.Err()
            }
        }
        if err != nil {
            return Lease{}, err
        }
        return lease, nil
    }
}

func (q *PostgresQueue) Ack(ctx context.Context, token string) error {
    _, err := q.db.ExecContext(ctx, `
        DELETE FROM workflow_queue WHERE id = $1 AND locked_by = $2
    `, token, q.workerID)
    return err
}

func (q *PostgresQueue) Nack(ctx context.Context, token string, delay time.Duration) error {
    _, err := q.db.ExecContext(ctx, `
        UPDATE workflow_queue
        SET status = 'pending',
            locked_by = NULL,
            locked_until = NULL,
            visible_at = NOW() + $2::interval
        WHERE id = $1 AND locked_by = $3
    `, token, delay, q.workerID)
    return err
}

func (q *PostgresQueue) Extend(ctx context.Context, token string, ttl time.Duration) error {
    _, err := q.db.ExecContext(ctx, `
        UPDATE workflow_queue
        SET locked_until = NOW() + $2::interval
        WHERE id = $1 AND locked_by = $3 AND status = 'processing'
    `, token, ttl, q.workerID)
    return err
}

func (q *PostgresQueue) reapStaleLeases(ctx context.Context) {
    // Move expired leases back to pending
    q.db.ExecContext(ctx, `
        UPDATE workflow_queue
        SET status = 'pending',
            locked_by = NULL,
            locked_until = NULL
        WHERE status = 'processing' AND locked_until < NOW()
    `)
}
```

### MemoryQueue Implementation

For testing and single-process deployments.

```go
type MemoryQueue struct {
    mu       sync.Mutex
    items    map[string]*memoryItem
    pending  chan string
    leaseTTL time.Duration
}

type memoryItem struct {
    executionID string
    lockedBy    string
    lockedUntil time.Time
}

func NewMemoryQueue(bufferSize int, leaseTTL time.Duration) *MemoryQueue {
    return &MemoryQueue{
        items:    make(map[string]*memoryItem),
        pending:  make(chan string, bufferSize),
        leaseTTL: leaseTTL,
    }
}
```

---

## Execution Environment

### Interface

The ExecutionEnvironment determines where and how workflows run. Two modes are supported:

1. **Blocking (Local)**: `Run` blocks until the workflow completes
2. **Dispatch (Remote)**: `Dispatch` hands off to a remote worker and returns immediately

```go
type ExecutionEnvironment interface {
    // Mode returns whether this environment blocks or dispatches.
    Mode() EnvironmentMode
}

type EnvironmentMode int

const (
    EnvironmentModeBlocking EnvironmentMode = iota
    EnvironmentModeDispatch
)

// BlockingEnvironment runs workflows in-process.
type BlockingEnvironment interface {
    ExecutionEnvironment
    // Run executes the workflow. Blocks until completion.
    Run(ctx context.Context, exec *Execution) error
}

// DispatchEnvironment hands off to remote workers.
type DispatchEnvironment interface {
    ExecutionEnvironment
    // Dispatch triggers remote execution. Returns once handoff succeeds.
    // The remote worker is responsible for claiming, running, and completing.
    Dispatch(ctx context.Context, executionID string, attempt int) error
}
```

### LocalEnvironment (Blocking)

Runs workflows in-process. The Engine manages the full lifecycle.

```go
type LocalEnvironment struct {
    checkpointer Checkpointer
    callbacks    ExecutionCallbacks
    logger       *slog.Logger
}

func (e *LocalEnvironment) Mode() EnvironmentMode {
    return EnvironmentModeBlocking
}

func (e *LocalEnvironment) Run(ctx context.Context, exec *Execution) error {
    return exec.Run(ctx)
}
```

### SpritesEnvironment (Dispatch)

For on-demand compute using [Sprites](https://sprites.dev/). The Engine dispatches work; the remote worker handles the rest.

```go
type SpritesEnvironment struct {
    client       *sprites.Client
    workerImage  string
    storeConnStr string // DB connection for worker
}

func (e *SpritesEnvironment) Mode() EnvironmentMode {
    return EnvironmentModeDispatch
}

func (e *SpritesEnvironment) Dispatch(ctx context.Context, executionID string, attempt int) error {
    sprite, err := e.client.Create(ctx, &sprites.CreateOptions{
        Image:  e.workerImage,
        CPU:    2,
        Memory: "4GB",
    })
    if err != nil {
        return fmt.Errorf("create sprite: %w", err)
    }

    // Start worker process. Worker is responsible for:
    // 1. Claiming the execution (with fencing via attempt)
    // 2. Running the workflow with heartbeating
    // 3. Completing/failing in the store (with fencing)
    _, err = e.client.Exec(ctx, sprite.ID, sprites.ExecOptions{
        Command: []string{
            "/app/worker",
            "--execution-id", executionID,
            "--attempt", strconv.Itoa(attempt),
            "--store-dsn", e.storeConnStr,
        },
        Detach: true, // Don't wait for completion
    })
    if err != nil {
        // Clean up the sprite on failure
        e.client.Delete(ctx, sprite.ID)
        return fmt.Errorf("exec worker: %w", err)
    }

    return nil
}
```

---

## Engine Lifecycle

### Startup and Recovery

```go
func (e *Engine) Start(ctx context.Context) error {
    // 1. Start the stale execution reaper
    go e.reaperLoop(ctx)

    // 2. Recover orphaned executions (pending without queue item, or stale running)
    if err := e.recoverOrphaned(ctx); err != nil {
        return fmt.Errorf("recovery failed: %w", err)
    }

    // 3. Start processing loop
    go e.processLoop(ctx)

    return nil
}

func (e *Engine) recoverOrphaned(ctx context.Context) error {
    // Recover both pending (never started or dispatch failed) and running (crashed)
    orphaned, err := e.store.List(ctx, ListFilter{
        Statuses: []EngineExecutionStatus{
            EngineStatusPending,
            EngineStatusRunning,
        },
    })
    if err != nil {
        return err
    }

    for _, record := range orphaned {
        switch e.recoveryMode {
        case RecoveryResume:
            // Increment attempt for fencing, reset to pending, re-enqueue
            record.Attempt++
            record.Status = EngineStatusPending
            record.WorkerID = ""
            record.LastHeartbeat = time.Time{}
            record.DispatchedAt = time.Time{}

            if err := e.store.Update(ctx, record); err != nil {
                e.logger.Warn("failed to update attempt", "id", record.ID, "error", err)
                continue
            }
            if err := e.queue.Enqueue(ctx, WorkItem{ExecutionID: record.ID}); err != nil {
                e.logger.Warn("failed to re-enqueue", "id", record.ID, "error", err)
            }
        case RecoveryFail:
            record.Status = EngineStatusFailed
            record.LastError = "execution orphaned: process terminated unexpectedly"
            record.CompletedAt = time.Now()
            if err := e.store.Update(ctx, record); err != nil {
                e.logger.Warn("failed to mark orphaned", "id", record.ID, "error", err)
            }
        }
    }
    return nil
}

// reaperLoop periodically checks for stale executions
func (e *Engine) reaperLoop(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            e.reapStaleExecutions(ctx)
        }
    }
}

func (e *Engine) reapStaleExecutions(ctx context.Context) {
    heartbeatCutoff := time.Now().Add(-2 * time.Minute)
    dispatchCutoff := time.Now().Add(-5 * time.Minute) // grace period for worker to claim

    // Reap stale running executions (missed heartbeats)
    staleRunning, err := e.store.ListStaleRunning(ctx, heartbeatCutoff)
    if err != nil {
        e.logger.Warn("failed to list stale running executions", "error", err)
    } else {
        for _, record := range staleRunning {
            e.reapExecution(ctx, record, "missed heartbeat")
        }
    }

    // Reap stale pending executions (dispatched but never claimed)
    stalePending, err := e.store.ListStalePending(ctx, dispatchCutoff)
    if err != nil {
        e.logger.Warn("failed to list stale pending executions", "error", err)
    } else {
        for _, record := range stalePending {
            e.reapExecution(ctx, record, "dispatch not claimed")
        }
    }
}

func (e *Engine) reapExecution(ctx context.Context, record *ExecutionRecord, reason string) {
    e.logger.Info("reaping stale execution", "id", record.ID, "reason", reason)

    // Increment attempt for fencing
    record.Attempt++
    record.Status = EngineStatusPending
    record.WorkerID = ""
    record.LastHeartbeat = time.Time{}
    record.DispatchedAt = time.Time{}

    if err := e.store.Update(ctx, record); err != nil {
        e.logger.Warn("failed to reset stale execution", "id", record.ID, "error", err)
        return
    }

    if err := e.queue.Enqueue(ctx, WorkItem{ExecutionID: record.ID}); err != nil {
        e.logger.Warn("failed to re-enqueue stale execution", "id", record.ID, "error", err)
    }
}
```

### Processing Loop

```go
func (e *Engine) processLoop(ctx context.Context) {
    sem := make(chan struct{}, e.maxConcurrent)
    if e.maxConcurrent == 0 {
        sem = nil // unlimited
    }

    for {
        if e.stopping.Load() {
            return
        }

        // Acquire semaphore FIRST, then dequeue
        // This ensures we have capacity before claiming a lease,
        // preventing lease expiry while waiting for capacity
        if sem != nil {
            select {
            case sem <- struct{}{}:
            case <-ctx.Done():
                return
            }
        }

        // Dequeue with lease
        lease, err := e.queue.Dequeue(ctx)
        if err != nil {
            if sem != nil {
                <-sem // release slot on error
            }
            if ctx.Err() != nil {
                return
            }
            e.logger.Warn("dequeue error", "error", err)
            continue
        }

        e.activeWg.Add(1)
        go func(lease Lease) {
            defer e.activeWg.Done()
            defer func() {
                if sem != nil {
                    <-sem
                }
            }()

            e.processExecution(ctx, lease)
        }(lease)
    }
}

func (e *Engine) processExecution(ctx context.Context, lease Lease) {
    id := lease.Item.ExecutionID

    record, err := e.store.Get(ctx, id)
    if err != nil {
        e.logger.Error("failed to load execution", "id", id, "error", err)
        e.queue.Nack(ctx, lease.Token, time.Minute)
        return
    }

    // Handle based on environment mode
    switch env := e.env.(type) {
    case BlockingEnvironment:
        e.processBlocking(ctx, lease, record, env)
    case DispatchEnvironment:
        e.processDispatch(ctx, lease, record, env)
    }
}

func (e *Engine) processBlocking(ctx context.Context, lease Lease, record *ExecutionRecord, env BlockingEnvironment) {
    id := record.ID
    attempt := record.Attempt

    // Claim with fencing (status must be pending, attempt must match)
    claimed, err := e.store.ClaimExecution(ctx, id, e.workerID, attempt)
    if err != nil {
        e.logger.Error("failed to claim execution", "id", id, "error", err)
        e.queue.Nack(ctx, lease.Token, time.Minute)
        return
    }
    if !claimed {
        // Either not pending or attempt changed - ack and move on
        e.logger.Debug("execution not claimable", "id", id, "attempt", attempt)
        e.queue.Ack(ctx, lease.Token)
        return
    }

    // Invoke OnExecutionStarted callback
    if e.callbacks != nil {
        e.callbacks.OnExecutionStarted(id)
    }

    // Start heartbeat goroutine
    heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
    defer cancelHeartbeat()
    go e.heartbeatLoop(heartbeatCtx, id, lease.Token)

    // Load or create execution
    exec, err := e.loadExecution(ctx, record)
    if err != nil {
        e.completeExecution(ctx, lease, record, attempt, EngineStatusFailed, nil, err.Error())
        return
    }

    // Run workflow
    if err := env.Run(ctx, exec); err != nil {
        e.completeExecution(ctx, lease, record, attempt, EngineStatusFailed, nil, err.Error())
        return
    }

    // Complete successfully
    e.completeExecution(ctx, lease, record, attempt, EngineStatusCompleted, exec.Outputs(), "")
}

func (e *Engine) completeExecution(ctx context.Context, lease Lease, record *ExecutionRecord, attempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) {
    // Persist completion with fencing - MUST succeed before Ack
    completed, err := e.store.CompleteExecution(ctx, record.ID, attempt, status, outputs, lastError)
    if err != nil {
        e.logger.Error("failed to persist completion", "id", record.ID, "error", err)
        // Nack for retry - DO NOT ack without persistence
        e.queue.Nack(ctx, lease.Token, time.Minute)
        return
    }
    if !completed {
        // Attempt changed - we're stale, just ack and move on
        e.logger.Warn("completion fenced out", "id", record.ID, "attempt", attempt)
        e.queue.Ack(ctx, lease.Token)
        return
    }

    // Persistence succeeded - now safe to ack
    e.queue.Ack(ctx, lease.Token)

    if e.callbacks != nil {
        var execErr error
        if lastError != "" {
            execErr = errors.New(lastError)
        }
        e.callbacks.OnExecutionCompleted(record.ID, time.Since(record.StartedAt), execErr)
    }
}

func (e *Engine) processDispatch(ctx context.Context, lease Lease, record *ExecutionRecord, env DispatchEnvironment) {
    id := record.ID
    attempt := record.Attempt

    // Mark dispatched for stale detection (worker must claim within grace period)
    if err := e.store.MarkDispatched(ctx, id, attempt); err != nil {
        e.logger.Error("failed to mark dispatched", "id", id, "error", err)
        e.queue.Nack(ctx, lease.Token, time.Minute)
        return
    }

    // Dispatch to remote worker
    if err := env.Dispatch(ctx, id, attempt); err != nil {
        e.logger.Error("failed to dispatch execution", "id", id, "error", err)
        e.queue.Nack(ctx, lease.Token, time.Minute)
        return
    }

    // Dispatch succeeded - ack the queue item
    // The execution record tracks state. If the worker fails to claim,
    // the reaper will detect stale dispatched_at and re-enqueue.
    e.queue.Ack(ctx, lease.Token)
}

func (e *Engine) heartbeatLoop(ctx context.Context, id, leaseToken string) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if err := e.store.Heartbeat(ctx, id, e.workerID); err != nil {
                e.logger.Warn("heartbeat failed", "id", id, "error", err)
            }
            // Also extend the queue lease
            if err := e.queue.Extend(ctx, leaseToken, 5*time.Minute); err != nil {
                e.logger.Warn("lease extend failed", "id", id, "error", err)
            }
        }
    }
}
```

### Graceful Shutdown

```go
func (e *Engine) Shutdown(ctx context.Context) error {
    // Signal processing loop to stop
    e.stopping.Store(true)

    // Wait for in-flight executions
    done := make(chan struct{})
    go func() {
        e.activeWg.Wait()
        close(done)
    }()

    select {
    case <-done:
        e.logger.Info("all executions completed")
        return e.queue.Close()
    case <-ctx.Done():
        e.logger.Warn("shutdown timeout, executions may be interrupted")
        return ctx.Err()
    }
}
```

---

## Worker Binary (Remote Execution)

For `DispatchEnvironment` (e.g., Sprites), a separate worker binary runs in the remote environment. The worker is responsible for the full execution lifecycle.

### Worker Responsibilities

1. **Claim** - Acquire the execution with fencing (prevents zombie workers)
2. **Heartbeat** - Signal liveness so the reaper doesn't reclaim
3. **Run** - Execute the workflow with checkpointing
4. **Complete** - Update final status with fencing (prevents stale writes)

### Worker Implementation

```go
func RunWorker(ctx context.Context, cfg WorkerConfig) error {
    store := postgres.NewExecutionStore(cfg.StoreDSN)
    checkpointer := file.NewCheckpointer(cfg.CheckpointDir)

    // 1. Load execution record
    record, err := store.Get(ctx, cfg.ExecutionID)
    if err != nil {
        return fmt.Errorf("load execution: %w", err)
    }

    // 2. Claim with fencing - only succeeds if status=pending AND attempt matches
    claimed, err := store.ClaimExecution(ctx, cfg.ExecutionID, cfg.WorkerID, cfg.Attempt)
    if err != nil {
        return fmt.Errorf("claim execution: %w", err)
    }
    if !claimed {
        // Fenced out - either not pending or newer attempt exists
        slog.Info("execution not claimable, exiting", "id", cfg.ExecutionID, "attempt", cfg.Attempt)
        return nil
    }

    // 3. Start heartbeat goroutine
    heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
    defer cancelHeartbeat()
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-heartbeatCtx.Done():
                return
            case <-ticker.C:
                store.Heartbeat(heartbeatCtx, cfg.ExecutionID, cfg.WorkerID)
            }
        }
    }()

    // 4. Load or resume execution
    exec, err := loadOrResumeExecution(ctx, record, checkpointer)
    if err != nil {
        return completeWithFencing(ctx, store, cfg.ExecutionID, cfg.Attempt, EngineStatusFailed, nil, err.Error())
    }

    // 5. Run workflow
    if err := exec.Run(ctx); err != nil {
        return completeWithFencing(ctx, store, cfg.ExecutionID, cfg.Attempt, EngineStatusFailed, nil, err.Error())
    }

    // 6. Complete successfully with fencing
    return completeWithFencing(ctx, store, cfg.ExecutionID, cfg.Attempt, EngineStatusCompleted, exec.Outputs(), "")
}

func completeWithFencing(ctx context.Context, store ExecutionStore, id string, attempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) error {
    completed, err := store.CompleteExecution(ctx, id, attempt, status, outputs, lastError)
    if err != nil {
        return fmt.Errorf("complete execution: %w", err)
    }
    if !completed {
        // Fenced out - a newer attempt took over
        slog.Warn("completion fenced out", "id", id, "attempt", attempt)
    }
    return nil
}
```

### Worker CLI

```bash
worker --execution-id abc123 --attempt 1 --store-dsn "postgres://..." --checkpoint-dir /tmp/checkpoints
```

---

## Manager↔Worker Contract

### For LocalEnvironment (Blocking)

```
┌──────────────────────────────────────────────────────────────┐
│                         ENGINE                               │
│  1. Acquire semaphore slot                                   │
│  2. Dequeue(lease)                                           │
│  3. ClaimExecution(id, workerID, attempt) → fenced           │
│  4. OnExecutionStarted callback                              │
│  5. Start heartbeat goroutine                                │
│  6. env.Run(exec) → blocks until complete                    │
│  7. CompleteExecution (fenced) → MUST succeed before Ack     │
│  8. Ack(lease.Token)                                         │
└──────────────────────────────────────────────────────────────┘
```

### For SpritesEnvironment (Dispatch)

```
┌─────────────────────────────────┐     ┌─────────────────────────────────┐
│            ENGINE               │     │            WORKER               │
│                                 │     │                                 │
│ 1. Acquire semaphore slot       │     │                                 │
│ 2. Dequeue(lease)               │     │                                 │
│ 3. MarkDispatched(id, attempt)  │     │                                 │
│ 4. env.Dispatch(id, attempt) ───┼────▶│ 1. ClaimExecution (fenced)      │
│ 5. Ack(lease.Token)             │     │ 2. Start heartbeat              │
│                                 │     │ 3. Run workflow                 │
│ (reaper monitors heartbeats     │     │ 4. CompleteExecution (fenced)   │
│  AND stale dispatched_at)       │     │ 5. Exit                         │
└─────────────────────────────────┘     └─────────────────────────────────┘

On worker crash or dispatch not claimed:
┌─────────────────────────────────┐
│          ENGINE REAPER          │
│ 1. ListStaleRunning(cutoff)     │
│    OR ListStalePending(cutoff)  │
│ 2. Increment attempt (fencing)  │
│ 3. Reset status to pending      │
│ 4. Re-enqueue                   │
└─────────────────────────────────┘
```

### Failure Scenarios

| Scenario                         | Detection                          | Recovery                            |
| -------------------------------- | ---------------------------------- | ----------------------------------- |
| Worker crash during execution    | Heartbeat timeout (reaper)         | Re-enqueue with incremented attempt |
| Worker crash after completion    | N/A (already completed)            | None needed                         |
| Dispatch worker never claims     | dispatched_at timeout (reaper)     | Re-enqueue with incremented attempt |
| Engine crash before dispatch     | Queue lease expires                | Another engine dequeues             |
| Engine crash after dispatch      | Worker continues                   | Worker completes normally           |
| Network partition                | Heartbeat timeout                  | New attempt fences old worker       |
| Stale worker tries to complete   | CompleteExecution returns false    | Stale write prevented               |
| Persistence fails on completion  | Error returned                     | Nack, retry later                   |

### Idempotency Requirements

Since delivery is at-least-once, workflows may be re-executed from a checkpoint. Activities should be idempotent at checkpoint boundaries:

- File writes: use atomic rename
- Database updates: use upserts or conditional updates
- External API calls: use idempotency keys where available

---

## Callbacks

```go
type EngineCallbacks interface {
    OnExecutionSubmitted(id string, workflowName string)
    OnExecutionStarted(id string)
    OnExecutionCompleted(id string, duration time.Duration, err error)
}
```

This allows users to integrate metrics (Prometheus, OpenTelemetry) without adding hard dependencies to the library.

---

## Configuration

```go
type RecoveryMode string

const (
    RecoveryResume RecoveryMode = "resume" // Resume from checkpoint
    RecoveryFail   RecoveryMode = "fail"   // Mark as failed
)
```

Environment variables:

```bash
WORKFLOW_WORKER_ID=worker-1     # Unique worker identifier
WORKFLOW_MAX_CONCURRENT=10      # Max concurrent executions
WORKFLOW_SHUTDOWN_TIMEOUT=30s   # Time to wait on shutdown
WORKFLOW_RECOVERY_MODE=resume   # resume or fail
WORKFLOW_HEARTBEAT_INTERVAL=30s # How often workers heartbeat
WORKFLOW_HEARTBEAT_TIMEOUT=2m   # When to consider running worker dead
WORKFLOW_DISPATCH_TIMEOUT=5m    # When to consider dispatched-but-not-claimed dead
WORKFLOW_QUEUE_LEASE_TTL=5m     # Queue item lease duration
```

---

## Usage Example

```go
// Create components
store := postgres.NewExecutionStore(db)
queue := postgres.NewQueue(db, postgres.QueueOptions{
    WorkerID:     "engine-1",
    PollInterval: 100 * time.Millisecond,
    LeaseTTL:     5 * time.Minute,
})
checkpointer := file.NewCheckpointer("./checkpoints")
env := &LocalEnvironment{
    checkpointer: checkpointer,
    logger:       logger,
}

// Create engine
engine, err := NewEngine(EngineOptions{
    Store:           store,
    Queue:           queue,
    Environment:     env,
    Checkpointer:    checkpointer,
    WorkerID:        "engine-1",
    MaxConcurrent:   10,
    ShutdownTimeout: 30 * time.Second,
    RecoveryMode:    RecoveryResume,
    Logger:          logger,
})

// Start engine (runs reaper, recovers orphaned, starts processing)
if err := engine.Start(ctx); err != nil {
    log.Fatal(err)
}

// Submit workflow
handle, err := engine.Submit(ctx, SubmitRequest{
    Workflow: myWorkflow,
    Inputs:   map[string]any{"url": "https://example.com"},
})

// Query status
record, _ := engine.Get(ctx, handle.ID)

// Graceful shutdown
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
engine.Shutdown(shutdownCtx)
```

---

## Thread Safety

The Engine uses several patterns to ensure thread safety:

1. **WaitGroup for shutdown** - Tracks active executions reliably
2. **Atomic stopping flag** - Coordinates shutdown across goroutines
3. **Fenced claiming** - Only status=pending with matching attempt can claim
4. **Fenced completion** - Only matching attempt can write final status
5. **Queue leases** - Prevents double-dequeue via tokens

Lock ordering (to prevent deadlocks):
1. ExecutionStore (via database transactions)
2. WorkQueue (via database transactions)

Rules:
- Never hold a lock while calling Checkpointer
- Never hold a lock while executing Activities
- Always use fenced claiming and completion for distributed execution

---

## Trade-offs

| Decision                                          | Rationale                                                        |
| ------------------------------------------------- | ---------------------------------------------------------------- |
| Execution-level concurrency (not activity-level)  | Simpler model; avoids rewriting `Path.Run` into a task scheduler |
| Checkpoints over event sourcing                   | Keeps persistence minimal; avoids complex schema design          |
| At-least-once delivery                            | Simpler than exactly-once; fencing prevents double-execution     |
| Heartbeat-based liveness                          | Simple, works across network partitions                          |
| ExecutionStore as source of truth                 | Queue is ephemeral; store survives restarts                      |
| Semaphore-first ordering                          | Prevents lease expiry while waiting for capacity                 |
| No transactional outbox for Submit                | Simpler; startup recovery handles orphaned pending records       |
| Fenced completion before Ack                      | Guarantees persistence; Nack on failure enables retry            |
| Event log for observability (not recovery)        | Audit trail benefits without event-sourcing complexity           |
| Timers via checkpointed deadlines                 | Durable delays without server-managed timer events               |
| Deterministic helpers (not enforced)              | Easy to do right, flexibility preserved for edge cases           |

---

## Operational Considerations

This section explains what users should expect and plan for given the design tradeoffs, how crashes are handled, and guidelines for building reliable workflows.

### At-Least-Once Execution Model

The engine provides **at-least-once** execution semantics. This means:

- Every submitted workflow will eventually complete (assuming the system recovers)
- A workflow may be executed more than once in failure scenarios
- Activities may be re-executed from the last checkpoint

**What users must plan for:**

1. **Activities should be idempotent** - The same activity with the same inputs should produce the same result, even if called multiple times
2. **Side effects need protection** - External calls (APIs, databases, file writes) need idempotency strategies
3. **Checkpoints are the replay boundary** - Work before the last checkpoint won't be re-executed; work after it will be

### Crash Scenarios and Recovery

#### Engine Crash (Manager)

| Scenario | What Happens | Recovery Time | User Impact |
|----------|--------------|---------------|-------------|
| Crash before Submit persists | Client receives error | Immediate | Retry submission |
| Crash after Submit, before Enqueue | Record stays pending | Next startup (~seconds) | Automatic recovery |
| Crash while processing (blocking mode) | Execution stays running | Heartbeat timeout (2min) + reaper cycle (30s) | Automatic retry with new attempt |
| Crash after dispatch (dispatch mode) | Worker continues normally | None | No impact if worker healthy |

**Key insight**: The engine is stateless between requests. All durable state lives in the ExecutionStore. Restarting the engine triggers recovery of any incomplete work.

#### Worker Crash (Remote Execution)

| Scenario | What Happens | Recovery Time | User Impact |
|----------|--------------|---------------|-------------|
| Crash before claim | Dispatch timeout detected | 5 minutes + reaper cycle | Automatic retry with new attempt |
| Crash during execution | Heartbeat timeout detected | 2 minutes + reaper cycle | Automatic retry from checkpoint |
| Crash after completion persisted | Nothing (already done) | None | No impact |
| Crash after completion, persistence failed | Treated as crash during execution | 2 minutes + reaper cycle | Retry from checkpoint |

**Key insight**: The `attempt` counter serves as a fencing token. When a worker crashes and work is retried, the new attempt number prevents the zombie worker (if it recovers) from overwriting the new attempt's results.

#### Database Unavailability

| Scenario | What Happens | Recovery |
|----------|--------------|----------|
| DB down during Submit | Client receives error | Retry when DB recovers |
| DB down during claim | Nack, retry after delay | Automatic |
| DB down during heartbeat | Logged warning, continues | Automatic (unless prolonged) |
| DB down during completion | Nack, retry after delay | Automatic |
| Prolonged DB outage during execution | Heartbeat timeout, reaper retries | May cause duplicate execution |

**Key insight**: Transient database failures are handled gracefully via Nack and retry. Prolonged outages (longer than heartbeat timeout) may cause executions to be retried from checkpoint.

### Designing Idempotent Activities

Since activities may be re-executed, they must be designed for idempotency:

#### Pattern 1: Idempotency Keys for External APIs

```go
func (a *PaymentActivity) Execute(ctx workflow.Context, params PaymentParams) (any, error) {
    // Use execution ID + step name as idempotency key
    idempotencyKey := fmt.Sprintf("%s-%s", ctx.ExecutionID(), ctx.StepName())

    resp, err := a.paymentClient.Charge(ctx, &ChargeRequest{
        Amount:         params.Amount,
        IdempotencyKey: idempotencyKey,
    })
    // Payment provider deduplicates based on key
    return resp, err
}
```

#### Pattern 2: Check-Then-Act for Database Operations

```go
func (a *CreateUserActivity) Execute(ctx workflow.Context, params CreateUserParams) (any, error) {
    // Use upsert instead of insert
    user, err := a.db.UpsertUser(ctx, &User{
        Email: params.Email,  // Natural key for deduplication
        Name:  params.Name,
    })
    return user, err
}
```

#### Pattern 3: Atomic File Operations

```go
func (a *WriteReportActivity) Execute(ctx workflow.Context, params ReportParams) (any, error) {
    // Write to temp file, then atomic rename
    tempPath := params.OutputPath + ".tmp." + ctx.ExecutionID()
    if err := os.WriteFile(tempPath, params.Data, 0644); err != nil {
        return nil, err
    }
    return nil, os.Rename(tempPath, params.OutputPath)
}
```

#### Pattern 4: Conditional Updates

```go
func (a *UpdateStatusActivity) Execute(ctx workflow.Context, params StatusParams) (any, error) {
    // Only update if in expected state (prevents double-updates)
    result, err := a.db.Exec(`
        UPDATE orders
        SET status = $1, updated_at = NOW()
        WHERE id = $2 AND status = $3
    `, params.NewStatus, params.OrderID, params.ExpectedStatus)

    if rows, _ := result.RowsAffected(); rows == 0 {
        // Already updated (idempotent success) or wrong state (error)
        current, _ := a.db.GetOrderStatus(ctx, params.OrderID)
        if current == params.NewStatus {
            return nil, nil // Idempotent success
        }
        return nil, fmt.Errorf("order in unexpected state: %s", current)
    }
    return nil, err
}
```

### What Gets Replayed on Recovery

Understanding checkpoint boundaries is crucial:

```
Activity A ──checkpoint──▶ Activity B ──checkpoint──▶ Activity C
                                                          │
                                                       CRASH
                                                          │
                                                       RECOVERY
                                                          │
                                          Activity C re-executes
```

**Rules:**
- Activities before the last checkpoint are NOT re-executed
- The activity that was running at crash time IS re-executed
- Path-local variables are restored from the checkpoint
- External side effects from the crashed activity may have partially completed

**Implication**: If Activity C calls an external API and crashes after the API succeeds but before checkpoint, the API call will be made again on recovery. This is why idempotency matters.

### Monitoring and Alerting Recommendations

Users should monitor:

| Metric | Normal | Warning | Critical |
|--------|--------|---------|----------|
| Executions in `running` state | Varies | Growing backlog | Stuck (no heartbeats) |
| Executions in `pending` state | Near zero | Growing (capacity issue) | Very old pending (>10min) |
| Reaper activity | Occasional | Frequent reaping | Constant reaping |
| Completion failures | Zero | Occasional | Persistent |
| Heartbeat failures | Rare | Frequent | All failing |

**Alerts to configure:**
- Execution stuck in `running` longer than expected workflow duration
- Execution in `pending` with `dispatched_at` older than dispatch timeout
- High rate of attempt increments (indicates repeated failures)
- Database connection errors

### Failure Modes and Expected Behavior

#### Normal Operation
- Executions flow: pending → running → completed/failed
- Attempts stay at 1
- Heartbeats succeed
- Completions persist on first try

#### Degraded Operation (Transient Failures)
- Occasional Nack and retry (normal)
- Attempt may increment once or twice
- Reaper occasionally recovers stuck work
- System self-heals

#### Abnormal Operation (Investigate)
- Attempts consistently > 2 (repeated failures)
- Same execution ID appearing in reaper logs repeatedly
- Growing pending queue despite available capacity
- Completions failing persistently

### Recovery Time Objectives

| Failure Type | Detection Time | Recovery Time | Total |
|--------------|----------------|---------------|-------|
| Engine crash (blocking) | Heartbeat timeout: 2min | Reaper cycle: 30s + restart | ~2.5-3 min |
| Engine crash (dispatch) | None (worker continues) | None | 0 |
| Worker crash | Heartbeat timeout: 2min | Reaper cycle: 30s | ~2.5-3 min |
| Dispatch not claimed | Dispatch timeout: 5min | Reaper cycle: 30s | ~5.5 min |
| Queue lease expiry | Lease TTL: 5min | Immediate re-dequeue | ~5 min |
| DB transient failure | Immediate | Nack delay: 1min | ~1 min |

**Tuning tradeoffs:**
- Shorter heartbeat timeout = faster recovery, but more false positives during GC pauses or network blips
- Longer heartbeat timeout = fewer false positives, but slower recovery
- Shorter reaper interval = faster recovery, but more DB load

### Capacity Planning

#### Queue Depth
- Queue depth should stay near zero in steady state
- Growing queue indicates: insufficient workers, slow activities, or stuck processing
- Size queue table for peak submission rate × max processing time

#### Concurrent Executions
- `MaxConcurrent` limits per-engine parallelism
- For N engines with MaxConcurrent=M, system handles up to N×M concurrent executions
- Memory usage scales with concurrent executions (each holds workflow state)

#### Database Load
- Heartbeats: 1 write per execution per 30 seconds
- Reaper: 2 queries per 30 seconds (stale running + stale pending)
- Queue polling: 1 query per poll interval when idle
- Plan for: (concurrent executions × 2/min) + (2/30s) + polling writes

### Graceful Degradation

When the system is under stress:

1. **Backpressure via semaphore**: New dequeues block when at capacity
2. **Nack with delay**: Failed operations retry after delay, preventing tight loops
3. **Lease expiry**: Abandoned work eventually returns to queue
4. **Reaper cleanup**: Stuck executions are recovered periodically

**What won't help automatically:**
- Persistent database failures (requires intervention)
- Bug in activity code causing repeated failures (requires fix + redeploy)
- Resource exhaustion (OOM, disk full) - requires capacity increase

---

## Timers (Durable Delays)

Workflows often need to wait—rate limiting, scheduling, retry backoff. Native `time.Sleep()` doesn't survive crashes. The engine provides a timer abstraction that checkpoints the deadline.

### Timer Step

```go
// Timer creates a durable delay that survives recovery
func (p *Path) Timer(name string, duration time.Duration) *Path {
    return p.AddStep(&TimerStep{
        name:     name,
        duration: duration,
    })
}

type TimerStep struct {
    name     string
    duration time.Duration
}

func (t *TimerStep) Execute(ctx workflow.Context) error {
    state := ctx.PathState()
    deadlineKey := "timer_deadline_" + t.name

    // On first execution: compute and checkpoint deadline
    // On recovery: load deadline from checkpoint
    deadline, ok := state[deadlineKey].(time.Time)
    if !ok {
        deadline = ctx.Now().Add(t.duration)
        state[deadlineKey] = deadline
    }

    remaining := time.Until(deadline)
    if remaining <= 0 {
        return nil // Already elapsed
    }

    select {
    case <-ctx.Clock().After(remaining):
        return nil
    case <-ctx.Done():
        return ctx.Err()
    }
}
```

### Usage

```go
workflow := NewWorkflow("rate-limited-fetch").
    Path("main").
        Activity("fetch", &FetchActivity{}).
        Timer("rate-limit", 1*time.Second).  // Durable 1s delay
        Activity("process", &ProcessActivity{}).
    Build()
```

### Recovery Behavior

| Scenario | Behavior |
|----------|----------|
| Crash before timer starts | Timer runs full duration after recovery |
| Crash during timer wait | Timer resumes with remaining time |
| Crash after deadline passed | Timer completes immediately |

### Clock Abstraction

Timers use an injectable clock for testing:

```go
type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
}

// RealClock uses system time (default)
type RealClock struct{}

// FakeClock for testing - time only advances when you call Advance()
type FakeClock struct {
    mu      sync.Mutex
    now     time.Time
    waiters []clockWaiter
}

func (c *FakeClock) Advance(d time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.now = c.now.Add(d)
    // Fire waiters whose deadline has passed
    for _, w := range c.waiters {
        if !w.deadline.After(c.now) {
            close(w.ch)
        }
    }
}
```

This allows testing hour-long workflows in milliseconds:

```go
func TestWorkflow_WithTimer(t *testing.T) {
    clock := NewFakeClock(time.Now())
    exec := NewExecution(workflow, WithClock(clock))

    go exec.Run(ctx)

    // Advance past the 1-hour timer instantly
    clock.Advance(1 * time.Hour)

    // Workflow completes without waiting
    result := <-exec.Done()
}
```

---

## Event Log (Observability)

While recovery uses checkpoints (not event replay), an optional event log provides audit trails and debugging visibility.

### Interface

```go
// EventLog captures workflow events for observability (not recovery)
type EventLog interface {
    Append(ctx context.Context, event Event) error
    List(ctx context.Context, executionID string, filter EventFilter) ([]Event, error)
}

type Event struct {
    ID          string         `json:"id"`
    ExecutionID string         `json:"execution_id"`
    Timestamp   time.Time      `json:"timestamp"`
    Type        EventType      `json:"type"`
    StepName    string         `json:"step_name,omitempty"`
    PathID      string         `json:"path_id,omitempty"`
    Attempt     int            `json:"attempt,omitempty"`
    Data        map[string]any `json:"data,omitempty"`
    Error       string         `json:"error,omitempty"`
}

type EventType string

const (
    EventWorkflowStarted   EventType = "workflow_started"
    EventWorkflowCompleted EventType = "workflow_completed"
    EventWorkflowFailed    EventType = "workflow_failed"
    EventStepStarted       EventType = "step_started"
    EventStepCompleted     EventType = "step_completed"
    EventStepFailed        EventType = "step_failed"
    EventStepRetrying      EventType = "step_retrying"
    EventCheckpointSaved   EventType = "checkpoint_saved"
    EventTimerStarted      EventType = "timer_started"
    EventTimerFired        EventType = "timer_fired"
    EventPathForked        EventType = "path_forked"
    EventPathJoined        EventType = "path_joined"
)

type EventFilter struct {
    Types  []EventType
    After  time.Time
    Before time.Time
    Limit  int
}
```

### Implementation via Callbacks

The event log integrates through the existing callback mechanism:

```go
type EventLogCallbacks struct {
    log EventLog
}

func (c *EventLogCallbacks) OnStepStarted(execID, pathID, stepName string) {
    c.log.Append(ctx, Event{
        ID:          uuid.New().String(),
        ExecutionID: execID,
        Timestamp:   time.Now(),
        Type:        EventStepStarted,
        StepName:    stepName,
        PathID:      pathID,
    })
}

// ... similar for other callbacks
```

### PostgresEventLog

```go
type PostgresEventLog struct {
    db *sql.DB
}

/*
CREATE TABLE workflow_events (
    id            TEXT PRIMARY KEY,
    execution_id  TEXT NOT NULL,
    timestamp     TIMESTAMPTZ NOT NULL,
    type          TEXT NOT NULL,
    step_name     TEXT,
    path_id       TEXT,
    attempt       INTEGER,
    data          JSONB,
    error         TEXT
);

CREATE INDEX idx_events_execution ON workflow_events(execution_id, timestamp);
CREATE INDEX idx_events_type ON workflow_events(type, timestamp);
*/
```

### Usage

```go
eventLog := postgres.NewEventLog(db)

engine, _ := NewEngine(EngineOptions{
    // ... other options
    Callbacks: &EventLogCallbacks{log: eventLog},
})

// Query events for debugging
events, _ := eventLog.List(ctx, executionID, EventFilter{
    Types: []EventType{EventStepFailed, EventStepRetrying},
})
```

### Event Log vs. Checkpoints

| Aspect | Checkpoints | Event Log |
|--------|-------------|-----------|
| Purpose | Recovery | Observability |
| Required | Yes | No (optional) |
| Contains | State snapshot | Operation history |
| Size | Fixed per checkpoint | Grows with workflow |
| Query | By execution ID | By execution, type, time |

---

## Deterministic Helpers

To help users write recoverable workflows, the context provides deterministic alternatives to common non-deterministic operations. These are helpers, not enforced constraints.

### Context Extensions

```go
type Context interface {
    // Existing methods...

    // Now returns the current time. Uses injected clock for testability.
    // Prefer this over time.Now() in workflow code.
    Now() time.Time

    // Clock returns the clock instance for timer operations.
    Clock() Clock

    // DeterministicID generates a deterministic ID based on execution ID,
    // path ID, and step name. Safe to use across recovery.
    // Prefer this over uuid.New() in workflow code.
    DeterministicID(prefix string) string

    // Rand returns a deterministic random source seeded from the execution ID.
    // Prefer this over rand.* in workflow code.
    Rand() *rand.Rand
}
```

### Implementation

```go
func (ctx *executionContext) DeterministicID(prefix string) string {
    // Hash execution ID + path ID + step name + counter
    h := sha256.New()
    h.Write([]byte(ctx.executionID))
    h.Write([]byte(ctx.pathID))
    h.Write([]byte(ctx.stepName))

    counter := ctx.idCounter.Add(1)
    binary.Write(h, binary.BigEndian, counter)

    hash := h.Sum(nil)
    encoded := base32.StdEncoding.EncodeToString(hash[:10])
    return fmt.Sprintf("%s_%s", prefix, strings.ToLower(encoded))
}

func (ctx *executionContext) Rand() *rand.Rand {
    if ctx.rand == nil {
        // Seed from execution ID for deterministic sequence
        h := sha256.Sum256([]byte(ctx.executionID + ctx.pathID))
        seed := int64(binary.BigEndian.Uint64(h[:8]))
        ctx.rand = rand.New(rand.NewSource(seed))
    }
    return ctx.rand
}
```

### Usage Guidelines

```go
// AVOID: Non-deterministic operations
func (a *MyActivity) Execute(ctx workflow.Context, params Params) (any, error) {
    id := uuid.New().String()           // Different on each execution!
    delay := rand.Intn(10)              // Different on recovery!
    timestamp := time.Now()             // Changes on recovery!
    return result, nil
}

// PREFER: Deterministic alternatives
func (a *MyActivity) Execute(ctx workflow.Context, params Params) (any, error) {
    id := ctx.DeterministicID("order")  // Same across recovery
    delay := ctx.Rand().Intn(10)        // Same sequence on recovery
    timestamp := ctx.Now()              // Consistent with injected clock
    return result, nil
}
```

### Why Helpers, Not Enforcement

Unlike Temporal, we don't forbid non-deterministic operations because:

1. **Flexibility**: Sometimes you want real randomness or wall-clock time
2. **Simplicity**: No need for code analysis or runtime interception
3. **Checkpoints**: Our recovery model re-executes from checkpoint, not replay
4. **Pragmatism**: Document best practices, trust users to follow them

The helpers make it easy to do the right thing without making it impossible to do otherwise.

---

## Compatibility

- Existing users can keep calling `NewExecution` directly
- The Engine is an optional layer that composes `ExecutionOptions`
- No changes required to `Workflow`, `Step`, or `Activity` interfaces
- `EngineExecutionStatus` is distinct from the library's internal `ExecutionStatus`

---

## Implementation Phases

### Phase 1: Core Engine
- [ ] Define `ExecutionStore` interface with fenced ClaimExecution and CompleteExecution
- [ ] Define `WorkQueue` interface with lease semantics
- [ ] Implement `Engine` with Submit, Get, List
- [ ] Implement `LocalEnvironment`
- [ ] Add bounded concurrency with semaphore-first ordering

### Phase 2: Postgres Implementations
- [ ] Implement `PostgresStore` with fenced operations
- [ ] Implement `PostgresQueue` with Ack/Nack/Extend
- [ ] Add queue lease reaper
- [ ] Add execution heartbeat reaper
- [ ] Add stale dispatch reaper

### Phase 3: Operations
- [ ] Implement graceful shutdown with WaitGroup
- [ ] Add `EngineCallbacks` with all invocations
- [ ] Add Cancel support
- [ ] Add recovery on startup

### Phase 4: Timers and Time
- [ ] Define `Clock` interface with `Now()` and `After()`
- [ ] Implement `RealClock` (default) and `FakeClock` (testing)
- [ ] Implement `TimerStep` with checkpointed deadlines
- [ ] Add `WithClock` option to `ExecutionOptions`
- [ ] Add timer recovery tests (crash before/during/after)

### Phase 5: Observability
- [ ] Define `EventLog` interface
- [ ] Implement `PostgresEventLog`
- [ ] Implement `EventLogCallbacks` adapter
- [ ] Add event types for all workflow operations
- [ ] Add event query API with filtering

### Phase 6: Context Helpers
- [ ] Add `Now()` to context (uses injected clock)
- [ ] Add `Clock()` to context
- [ ] Add `DeterministicID(prefix)` to context
- [ ] Add `Rand()` to context with execution-seeded source
- [ ] Document best practices for deterministic workflows

### Phase 7: Distributed (Optional)
- [ ] Implement `SpritesEnvironment`
- [ ] Worker binary with fenced completion
- [ ] Integration tests for failure scenarios
