# Workflow Library Design Review and Enhancement Proposal

## Executive Summary

This document reviews the DeepNoodle workflow library's current architecture and proposes targeted enhancements. The library is already production-grade with sophisticated features including path-based concurrent execution, comprehensive checkpointing, and multi-path synchronization. The proposals focus on filling specific gaps without over-engineering.

## Current Architecture

### Core Execution Model

```
┌─────────────────────────────────────────────────────────────┐
│                    Workflow Definition                      │
│  (YAML/Code) -> Workflow, Steps, Edges, Joins, Retries      │
└────────────────────────┬────────────────────────────────────┘
                         │
                    NewExecution()
                         │
┌────────────────────────▼────────────────────────────────────┐
│                  Execution Orchestrator                     │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ ExecutionState (mutex-protected)                     │   │
│  │  ├─ Status, Inputs, Outputs                          │   │
│  │  ├─ PathStates: {pathID -> PathState}                │   │
│  │  └─ JoinStates: {stepName -> JoinState}              │   │
│  └──────────────────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────────────────┐   │
│  │ Event Loop: for snapshot := <-pathSnapshots          │   │
│  └──────────────────────────────────────────────────────┘   │
└──────────┬──────────────────────────────────────────────────┘
           │
    ┌──────┴──────┬──────────┐
    ▼             ▼          ▼
  ┌──────┐    ┌──────┐  ┌──────┐
  │Path1 │    │Path2 │  │Path3 │  (each in goroutine)
  │Run() │    │Run() │  │Run() │
  └──┬───┘    └──┬───┘  └──┬───┘
     │           │         │
     └─────PathSnapshots───┘ (channel-based coordination)
```

### Strengths

| Feature | Implementation |
|---------|----------------|
| **Fault tolerance** | Full checkpointing after every activity; resume from any point |
| **Concurrency** | Path-per-goroutine model with channel-based coordination |
| **State isolation** | Per-path `PathLocalState` requires no synchronization |
| **Branching** | Conditional edges with `all`/`first` matching strategies |
| **Multi-path sync** | Join mechanism with flexible path state merging |
| **Error handling** | Step-level retry with exponential backoff + catch handlers |
| **Extensibility** | Clean interfaces: `Activity`, `Checkpointer`, `ExecutionCallbacks` |

### Thread Safety Model

The library uses a **producer-consumer pattern** rather than shared memory:

1. **Paths** run independently in goroutines with unsynchronized `PathLocalState`
2. **ExecutionState** protects all shared state with a single `RWMutex`
3. **PathSnapshots** channel (buffered, size 100) enables atomic state transfer
4. **Immutable dependencies** injected at construction eliminate runtime mutation

This design is sound and well-suited for the problem domain.

---

## Gap Analysis

### Gaps Identified

| Gap | Current State | Impact |
|-----|---------------|--------|
| **No execution-level bounded concurrency** | Unlimited paths can spawn | Resource exhaustion possible |
| **No graceful shutdown** | Context cancellation only | In-flight work may be lost |
| **Checkpoint writes are fire-and-forget** | Errors logged but not surfaced | Silent data loss possible |
| **No execution history/listing** | Single checkpoint per execution | Cannot query past executions |
| **Channel backpressure unclear** | 100-buffer pathSnapshots channel | Could block on burst branching |

### What's Already Handled

The referenced proposal mentions several concerns that this library already addresses:

- **Retry mechanism**: Step-level `RetryConfig` with exponential backoff ✓
- **Input/result persistence**: Checkpoints include inputs, outputs, and all path variables ✓
- **Context propagation**: All activities receive proper `Context` for cancellation ✓
- **Structured logging**: `ActivityLogger` interface + `ExecutionCallbacks` ✓

---

## Proposed Enhancements

### Phase 1: Operational Safety

#### 1.1 Bounded Path Concurrency

**Problem**: A workflow with aggressive branching could spawn unlimited paths.

**Solution**: Add optional semaphore-based concurrency limit.

```go
type ExecutionOptions struct {
    // ... existing options
    MaxConcurrentPaths int // 0 = unlimited (default for backward compat)
}

type Execution struct {
    // ... existing fields
    pathSemaphore chan struct{} // nil if unlimited
}

func (e *Execution) runPath(ctx context.Context, path *Path) {
    if e.pathSemaphore != nil {
        select {
        case e.pathSemaphore <- struct{}{}:
            defer func() { <-e.pathSemaphore }()
        case <-ctx.Done():
            return
        }
    }
    path.Run(ctx)
}
```

**Guarantee**: At most N paths execute concurrently.

**Trade-off**: May increase wall-clock time for heavily parallel workflows. Users opt-in explicitly.

#### 1.2 Graceful Shutdown

**Problem**: When context is cancelled, in-flight activities may not complete cleanly.

**Solution**: Two-phase shutdown with draining period.

```go
type Execution struct {
    // ... existing fields
    shutdownOnce sync.Once
    draining     atomic.Bool
}

// Shutdown initiates graceful shutdown. Blocks until complete or ctx expires.
func (e *Execution) Shutdown(ctx context.Context) error {
    e.shutdownOnce.Do(func() {
        e.draining.Store(true)
        // Signal paths to stop accepting new work
        // But allow current activities to complete
    })

    // Wait for paths to drain
    done := make(chan struct{})
    go func() {
        e.doneWg.Wait()
        close(done)
    }()

    select {
    case <-done:
        return e.saveCheckpoint(ctx)
    case <-ctx.Done():
        return fmt.Errorf("shutdown timeout: %w", ctx.Err())
    }
}

// Path checks this before starting new steps
func (e *Execution) IsDraining() bool {
    return e.draining.Load()
}
```

**Integration with Path**:

```go
func (p *Path) Run(ctx context.Context) {
    for p.currentStep != nil {
        if p.execution.IsDraining() {
            // Complete current step, then stop
            p.status = ExecutionStatusPaused
            return
        }
        // ... execute step
    }
}
```

**Guarantee**: Graceful shutdown attempts to complete in-progress activities before stopping.

**Trade-off**: Adds ~20 lines of code and one atomic operation per step iteration.

### Phase 2: Persistence Guarantees

#### 2.1 Synchronous Checkpoint on Critical Transitions

**Problem**: Checkpoint errors are logged but execution continues. If the process crashes immediately after, the last state is lost.

**Current behavior**:
```go
func (e *Execution) saveCheckpoint(ctx context.Context) error {
    // ... create checkpoint
    if err := e.checkpointer.SaveCheckpoint(ctx, checkpoint); err != nil {
        e.activityLogger.LogActivity(...) // logged only
        return err // returned but often ignored
    }
    return nil
}
```

**Solution**: Make checkpoint failures non-silent at critical transitions.

```go
type CheckpointPolicy int

const (
    CheckpointPolicyBestEffort  CheckpointPolicy = iota // Current behavior
    CheckpointPolicyStrict                              // Fail execution on checkpoint error
)

type ExecutionOptions struct {
    CheckpointPolicy CheckpointPolicy
}

func (e *Execution) saveCheckpoint(ctx context.Context) error {
    err := e.checkpointer.SaveCheckpoint(ctx, checkpoint)
    if err != nil && e.opts.CheckpointPolicy == CheckpointPolicyStrict {
        // Mark execution as failed
        e.state.SetStatus(ExecutionStatusFailed)
        e.state.SetError(fmt.Sprintf("checkpoint failed: %v", err))
        return err
    }
    return err
}
```

**Critical transitions** where strict policy applies:
- Execution completion (success or failure)
- Path completion
- Join completion

Progress checkpoints during activity execution can remain best-effort to avoid blocking.

**Guarantee**: Under strict policy, final execution state is durable before `Run()` returns.

**Trade-off**: Strict policy adds latency and can fail execution on transient storage issues. Best-effort (default) preserves current behavior.

#### 2.2 Checkpoint Integrity

**Problem**: Partial writes or corruption could leave unreadable checkpoints.

**Solution**: Atomic write with temporary file rename.

```go
func (c *FileCheckpointer) SaveCheckpoint(ctx context.Context, cp *Checkpoint) error {
    data, err := json.MarshalIndent(cp, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal checkpoint: %w", err)
    }

    dir := c.checkpointDir(cp.ExecutionID)
    tmpFile := filepath.Join(dir, fmt.Sprintf(".checkpoint-%s.tmp", cp.ID))
    finalFile := filepath.Join(dir, fmt.Sprintf("checkpoint-%s.json", cp.ID))

    // Write to temp file
    if err := os.WriteFile(tmpFile, data, 0644); err != nil {
        return fmt.Errorf("write temp checkpoint: %w", err)
    }

    // Atomic rename
    if err := os.Rename(tmpFile, finalFile); err != nil {
        os.Remove(tmpFile)
        return fmt.Errorf("rename checkpoint: %w", err)
    }

    // Update latest symlink atomically
    return c.updateLatestSymlink(dir, finalFile)
}
```

**Guarantee**: Checkpoint files are never partially written.

**Trade-off**: One additional syscall per checkpoint.

### Phase 3: Execution Management

#### 3.1 Execution Listing and History

**Problem**: No way to list past executions or query their status.

**Solution**: Extend `Checkpointer` interface with listing capability.

```go
type ExecutionSummary struct {
    ExecutionID  string
    WorkflowName string
    Status       ExecutionStatus
    StartTime    time.Time
    EndTime      time.Time
    Error        string
}

type CheckpointerWithListing interface {
    Checkpointer
    ListExecutions(ctx context.Context, opts ListExecutionsOptions) ([]ExecutionSummary, error)
}

type ListExecutionsOptions struct {
    WorkflowName string           // Filter by workflow
    Statuses     []ExecutionStatus // Filter by status
    Limit        int
    Offset       int
}
```

**Implementation for FileCheckpointer**:

```go
func (c *FileCheckpointer) ListExecutions(ctx context.Context, opts ListExecutionsOptions) ([]ExecutionSummary, error) {
    entries, err := os.ReadDir(c.baseDir)
    if err != nil {
        return nil, err
    }

    var summaries []ExecutionSummary
    for _, entry := range entries {
        if !entry.IsDir() {
            continue
        }
        checkpoint, err := c.LoadCheckpoint(ctx, entry.Name())
        if err != nil {
            continue // Skip unreadable
        }
        if opts.WorkflowName != "" && checkpoint.WorkflowName != opts.WorkflowName {
            continue
        }
        summaries = append(summaries, ExecutionSummary{
            ExecutionID:  checkpoint.ExecutionID,
            WorkflowName: checkpoint.WorkflowName,
            Status:       ExecutionStatus(checkpoint.Status),
            StartTime:    checkpoint.StartTime,
            EndTime:      checkpoint.EndTime,
            Error:        checkpoint.Error,
        })
    }
    return summaries, nil
}
```

**Note**: This is a simple implementation. For high-volume use cases, consider a SQLite index or separate metadata file.

#### 3.2 Orphaned Execution Recovery

**Problem**: If process crashes with running executions, they remain in "running" state forever.

**Solution**: Recovery function to mark stale executions.

```go
func RecoverOrphanedExecutions(ctx context.Context, checkpointer CheckpointerWithListing) error {
    executions, err := checkpointer.ListExecutions(ctx, ListExecutionsOptions{
        Statuses: []ExecutionStatus{ExecutionStatusRunning, ExecutionStatusPending},
    })
    if err != nil {
        return err
    }

    for _, exec := range executions {
        checkpoint, err := checkpointer.LoadCheckpoint(ctx, exec.ExecutionID)
        if err != nil {
            continue
        }

        checkpoint.Status = string(ExecutionStatusFailed)
        checkpoint.Error = "execution orphaned: process terminated unexpectedly"
        checkpoint.EndTime = time.Now()

        if err := checkpointer.SaveCheckpoint(ctx, checkpoint); err != nil {
            slog.Warn("failed to mark orphaned execution", "execution_id", exec.ExecutionID, "error", err)
        }
    }
    return nil
}
```

**Usage**: Call on startup before accepting new executions.

**Guarantee**: No executions remain in "running" state indefinitely after crash.

---

## What We're NOT Adding

To avoid over-engineering, these are explicitly out of scope:

| Feature | Rationale |
|---------|-----------|
| **Prometheus metrics** | Callbacks interface already supports custom instrumentation |
| **Distributed locking** | Single-process assumption; would add significant complexity |
| **Automatic retry on checkpoint failure** | Would complicate the execution model; strict policy is sufficient |
| **Job queue / scheduler** | Library is execution engine, not scheduler; compose with external queue |
| **Database-backed checkpointer** | FileCheckpointer sufficient; users can implement `Checkpointer` interface |

---

## Thread Safety Analysis

### Current Implementation: Sound

The existing concurrency model is well-designed:

```
┌─────────────────────────────────────────────────────────────┐
│ SHARED STATE (ExecutionState)                               │
│ - Protected by single RWMutex                               │
│ - All reads return copies                                   │
│ - Updates use closure pattern for atomicity                 │
└─────────────────────────────────────────────────────────────┘
        ▲
        │ (atomic snapshots via channel)
        │
┌───────┴─────────────────────────────────────────────────────┐
│ PER-PATH STATE (PathLocalState)                             │
│ - Owned by single goroutine                                 │
│ - No synchronization needed                                 │
│ - Never shared between paths                                │
└─────────────────────────────────────────────────────────────┘
```

### Potential Concern: Channel Backpressure

The `pathSnapshots` channel has buffer size 100. With aggressive branching:

```yaml
steps:
  - name: "fan-out"
    next:
      - step: "a"
      - step: "b"
      # ... 150 branches
```

If the main loop processes slowly, paths could block sending snapshots.

**Assessment**: Unlikely in practice. The main loop is fast (no I/O). If it becomes an issue, increase buffer or use dynamic buffer pattern.

**Recommendation**: No change needed. Document that extremely high branching factors (>100 simultaneous) may require configuration.

### Lock Ordering

Document the implicit lock ordering to prevent future deadlocks:

```go
// Lock ordering (acquire in this order to prevent deadlock):
// 1. ExecutionState.mutex
// 2. (future) Individual path locks if added
//
// Rules:
// - Never hold a lock while sending to pathSnapshots channel
// - Never hold a lock while calling Checkpointer
// - Never hold a lock while executing Activities
```

---

## Persistence Guarantees Summary

### Current

| Operation | Guarantee |
|-----------|-----------|
| Activity completion | Checkpoint saved (best effort) |
| Path completion | Checkpoint saved (best effort) |
| Execution completion | Checkpoint saved (best effort) |

### Proposed (with strict policy)

| Operation | Guarantee (Best Effort) | Guarantee (Strict) |
|-----------|-------------------------|-------------------|
| Activity completion | Saved, errors logged | Saved, errors logged |
| Path completion | Saved, errors logged | **Durable before path marked complete** |
| Execution completion | Saved, errors logged | **Durable before Run() returns** |

---

## API Design

### New Public Types

```go
// ExecutionOptions additions
type ExecutionOptions struct {
    // ... existing
    MaxConcurrentPaths int              // 0 = unlimited
    CheckpointPolicy   CheckpointPolicy // BestEffort or Strict
}

// New interfaces
type CheckpointerWithListing interface {
    Checkpointer
    ListExecutions(ctx context.Context, opts ListExecutionsOptions) ([]ExecutionSummary, error)
}

// New functions
func RecoverOrphanedExecutions(ctx context.Context, checkpointer CheckpointerWithListing) error
func (e *Execution) Shutdown(ctx context.Context) error
func (e *Execution) IsDraining() bool
```

### Backward Compatibility

All changes are additive:
- `MaxConcurrentPaths` defaults to 0 (unlimited)
- `CheckpointPolicy` defaults to `BestEffort`
- `CheckpointerWithListing` extends `Checkpointer`; existing implementations continue to work
- `Shutdown()` is a new method; existing code unaffected

---

## Implementation Roadmap

### Phase 1: Operational Safety
- [ ] Add `MaxConcurrentPaths` option with semaphore
- [ ] Implement `Shutdown()` with draining support
- [ ] Add `IsDraining()` check in path loop

### Phase 2: Persistence Guarantees
- [ ] Add `CheckpointPolicy` option
- [ ] Implement atomic checkpoint writes in `FileCheckpointer`
- [ ] Synchronous checkpoint on completion under strict policy

### Phase 3: Execution Management
- [ ] Define `CheckpointerWithListing` interface
- [ ] Implement `ListExecutions` for `FileCheckpointer`
- [ ] Add `RecoverOrphanedExecutions` function

---

## Phase 4: Distributed Worker Architecture

This phase introduces the ability to separate activity execution (workers) from workflow orchestration. This enables:

- **Horizontal scaling**: Multiple workers process activities in parallel
- **Resource isolation**: Workers run in sandboxed environments
- **On-demand compute**: Spin up workers only when needed (via Sprites)
- **Cost efficiency**: Pay-per-use compute for bursty workloads

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────┐
│                         ORCHESTRATOR                                │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │ Execution (state machine, no longer runs activities)        │    │
│  │  ├─ ExecutionState (paths checkpoint before dispatching)    │    │
│  │  ├─ Dispatches ActivityTask to queue                        │    │
│  │  └─ Receives ActivityResult, advances path                  │    │
│  └─────────────────────────────────────────────────────────────┘    │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        TASK QUEUE                                   │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │ TaskQueue interface                                         │    │
│  │  ├─ Enqueue(ctx, ActivityTask) error                        │    │
│  │  ├─ Dequeue(ctx) (ActivityTask, error)                      │    │
│  │  └─ Complete(ctx, taskID, ActivityResult) error             │    │
│  └─────────────────────────────────────────────────────────────┘    │
│  Implementations: PostgresQueue, CloudTasksQueue, MemoryQueue       │
└───────────────────────────┬─────────────────────────────────────────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
┌──────────────────┐ ┌──────────────────┐ ┌──────────────────┐
│     WORKER 1     │ │     WORKER 2     │ │     WORKER 3     │
│  ┌────────────┐  │ │  ┌────────────┐  │ │  ┌────────────┐  │
│  │  Dequeue   │  │ │  │  Dequeue   │  │ │  │  Dequeue   │  │
│  │  Execute   │  │ │  │  Execute   │  │ │  │  Execute   │  │
│  │  Complete  │  │ │  │  Complete  │  │ │  │  Complete  │  │
│  └────────────┘  │ │  └────────────┘  │ │  └────────────┘  │
└──────────────────┘ └──────────────────┘ └──────────────────┘
   (local process)     (Sprites VM)         (Sprites VM)
```

### Core Abstractions

#### 4.1 Task Queue Interface

```go
// ActivityTask represents a unit of work to be executed by a worker.
type ActivityTask struct {
    TaskID      string         `json:"task_id"`
    ExecutionID string         `json:"execution_id"`
    PathID      string         `json:"path_id"`
    StepName    string         `json:"step_name"`
    Activity    string         `json:"activity"`
    Parameters  map[string]any `json:"parameters"`
    Variables   map[string]any `json:"variables"`   // Path variables for expression evaluation
    Inputs      map[string]any `json:"inputs"`      // Workflow inputs (read-only)
    EnqueuedAt  time.Time      `json:"enqueued_at"`
    Timeout     time.Duration  `json:"timeout"`
    Attempt     int            `json:"attempt"`     // For retry tracking
}

// ActivityResult represents the outcome of executing an activity.
type ActivityResult struct {
    TaskID      string         `json:"task_id"`
    ExecutionID string         `json:"execution_id"`
    PathID      string         `json:"path_id"`
    StepName    string         `json:"step_name"`
    Output      any            `json:"output,omitempty"`
    Error       *WorkflowError `json:"error,omitempty"`
    Duration    time.Duration  `json:"duration"`
    CompletedAt time.Time      `json:"completed_at"`
}

// TaskQueue is the interface for activity task distribution.
type TaskQueue interface {
    // Enqueue adds a task to the queue. Returns immediately.
    Enqueue(ctx context.Context, task *ActivityTask) error

    // Dequeue retrieves the next available task. Blocks until available or ctx cancelled.
    // The task is leased to this consumer until Complete is called or lease expires.
    Dequeue(ctx context.Context) (*ActivityTask, error)

    // Complete marks a task as finished and publishes the result.
    // The orchestrator receives results via a separate channel/subscription.
    Complete(ctx context.Context, result *ActivityResult) error

    // Results returns a channel of completed activity results for the orchestrator.
    Results(ctx context.Context) (<-chan *ActivityResult, error)

    // Close releases resources.
    Close() error
}
```

#### 4.2 Queue Implementations

**Postgres Queue** (for single-region, transactional guarantees):

```go
type PostgresQueue struct {
    db              *sql.DB
    pollInterval    time.Duration
    visibilityTimeout time.Duration
}

// Schema
/*
CREATE TABLE activity_tasks (
    task_id         TEXT PRIMARY KEY,
    execution_id    TEXT NOT NULL,
    path_id         TEXT NOT NULL,
    step_name       TEXT NOT NULL,
    activity        TEXT NOT NULL,
    parameters      JSONB NOT NULL,
    variables       JSONB NOT NULL,
    inputs          JSONB NOT NULL,
    timeout         INTERVAL NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 1,
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending, processing, completed, failed
    enqueued_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    visible_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until    TIMESTAMPTZ,
    leased_by       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE activity_results (
    task_id         TEXT PRIMARY KEY REFERENCES activity_tasks(task_id),
    execution_id    TEXT NOT NULL,
    path_id         TEXT NOT NULL,
    step_name       TEXT NOT NULL,
    output          JSONB,
    error           JSONB,
    duration        INTERVAL NOT NULL,
    completed_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tasks_pending ON activity_tasks(visible_at) WHERE status = 'pending';
CREATE INDEX idx_results_unprocessed ON activity_results(execution_id) WHERE processed = false;
*/

func (q *PostgresQueue) Dequeue(ctx context.Context) (*ActivityTask, error) {
    // Atomic claim with SELECT FOR UPDATE SKIP LOCKED
    query := `
        UPDATE activity_tasks
        SET status = 'processing',
            leased_until = NOW() + $1,
            leased_by = $2
        WHERE task_id = (
            SELECT task_id FROM activity_tasks
            WHERE status = 'pending' AND visible_at <= NOW()
            ORDER BY enqueued_at
            FOR UPDATE SKIP LOCKED
            LIMIT 1
        )
        RETURNING task_id, execution_id, path_id, step_name, activity,
                  parameters, variables, inputs, timeout, attempt
    `
    // ... scan and return
}
```

**Google Cloud Tasks Queue** (for managed, scalable distribution):

```go
type CloudTasksQueue struct {
    client       *cloudtasks.Client
    projectID    string
    location     string
    queueName    string
    workerURL    string  // HTTP endpoint for task delivery
    pubsubClient *pubsub.Client
    resultsTopic string
}

func (q *CloudTasksQueue) Enqueue(ctx context.Context, task *ActivityTask) error {
    payload, _ := json.Marshal(task)

    req := &taskspb.CreateTaskRequest{
        Parent: fmt.Sprintf("projects/%s/locations/%s/queues/%s",
            q.projectID, q.location, q.queueName),
        Task: &taskspb.Task{
            MessageType: &taskspb.Task_HttpRequest{
                HttpRequest: &taskspb.HttpRequest{
                    Url:        q.workerURL,
                    HttpMethod: taskspb.HttpMethod_POST,
                    Body:       payload,
                },
            },
            ScheduleTime: timestamppb.Now(),
        },
    }
    _, err := q.client.CreateTask(ctx, req)
    return err
}

func (q *CloudTasksQueue) Results(ctx context.Context) (<-chan *ActivityResult, error) {
    // Subscribe to Pub/Sub topic for results
    sub := q.pubsubClient.Subscription(q.resultsTopic + "-sub")
    ch := make(chan *ActivityResult, 100)

    go func() {
        defer close(ch)
        sub.Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
            var result ActivityResult
            if err := json.Unmarshal(msg.Data, &result); err != nil {
                msg.Nack()
                return
            }
            ch <- &result
            msg.Ack()
        })
    }()

    return ch, nil
}
```

**In-Memory Queue** (for testing and local development):

```go
type MemoryQueue struct {
    tasks   chan *ActivityTask
    results chan *ActivityResult
    mu      sync.RWMutex
    pending map[string]*ActivityTask
}

func NewMemoryQueue(bufferSize int) *MemoryQueue {
    return &MemoryQueue{
        tasks:   make(chan *ActivityTask, bufferSize),
        results: make(chan *ActivityResult, bufferSize),
        pending: make(map[string]*ActivityTask),
    }
}
```

#### 4.3 Worker Interface

```go
// Worker executes activities from a queue.
type Worker interface {
    // Start begins processing tasks. Blocks until ctx is cancelled.
    Start(ctx context.Context) error

    // Stop gracefully shuts down, completing in-flight work.
    Stop(ctx context.Context) error
}

// WorkerConfig configures worker behavior.
type WorkerConfig struct {
    Queue           TaskQueue
    Activities      map[string]Activity
    Concurrency     int           // Max concurrent activities
    HeartbeatPeriod time.Duration // For lease extension
}

// LocalWorker runs activities in the current process.
type LocalWorker struct {
    config WorkerConfig
    wg     sync.WaitGroup
    stop   chan struct{}
}

func (w *LocalWorker) Start(ctx context.Context) error {
    sem := make(chan struct{}, w.config.Concurrency)

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-w.stop:
            w.wg.Wait()
            return nil
        case sem <- struct{}{}:
            task, err := w.config.Queue.Dequeue(ctx)
            if err != nil {
                <-sem
                continue
            }

            w.wg.Add(1)
            go func() {
                defer w.wg.Done()
                defer func() { <-sem }()

                result := w.executeTask(ctx, task)
                w.config.Queue.Complete(ctx, result)
            }()
        }
    }
}
```

#### 4.4 Sprites Worker

[Sprites](https://sprites.dev/) provides on-demand Firecracker VMs with:
- ~1s cold start
- ~300ms checkpointing
- HTTP endpoint per sprite
- Pay-per-use ($0.07/CPU-hour)

```go
// SpritesWorkerPool manages a pool of Sprites-based workers.
type SpritesWorkerPool struct {
    client         *sprites.Client
    queue          TaskQueue
    activities     map[string]Activity
    minWorkers     int
    maxWorkers     int
    idleTimeout    time.Duration
    spriteImage    string  // Base image with Go runtime + activities

    mu             sync.RWMutex
    activeSprites  map[string]*SpriteWorker
}

type SpriteWorker struct {
    spriteID    string
    spriteURL   string
    lastActive  time.Time
    processing  atomic.Int32
}

// SpritesWorkerPool scales workers based on queue depth.
func (p *SpritesWorkerPool) Start(ctx context.Context) error {
    // Monitor queue and scale workers
    go p.autoscaler(ctx)

    // Receive results from sprites via webhook or polling
    go p.resultCollector(ctx)

    <-ctx.Done()
    return p.drainAll(ctx)
}

func (p *SpritesWorkerPool) autoscaler(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            queueDepth := p.queue.Depth()
            activeCount := len(p.activeSprites)

            // Scale up if queue is backing up
            if queueDepth > activeCount*2 && activeCount < p.maxWorkers {
                p.spawnSprite(ctx)
            }

            // Scale down idle sprites
            p.reapIdleSprites(ctx)
        }
    }
}

func (p *SpritesWorkerPool) spawnSprite(ctx context.Context) error {
    // Create new sprite from checkpoint or image
    sprite, err := p.client.Create(ctx, &sprites.CreateOptions{
        Image: p.spriteImage,
        CPU:   2,
        Memory: "4GB",
    })
    if err != nil {
        return err
    }

    // Start worker process in sprite
    _, err = p.client.Exec(ctx, sprite.ID, sprites.ExecOptions{
        Command: []string{"/app/worker", "--queue-url", p.queue.URL()},
        Detach:  true,
    })
    if err != nil {
        p.client.Delete(ctx, sprite.ID)
        return err
    }

    p.mu.Lock()
    p.activeSprites[sprite.ID] = &SpriteWorker{
        spriteID:   sprite.ID,
        spriteURL:  sprite.URL,
        lastActive: time.Now(),
    }
    p.mu.Unlock()

    return nil
}
```

### Orchestrator Changes

The orchestrator shifts from executing activities to dispatching tasks and processing results.

#### 4.5 Execution Mode

```go
type ExecutionMode int

const (
    ExecutionModeLocal      ExecutionMode = iota // Current behavior: in-process activities
    ExecutionModeDistributed                     // Queue-based: dispatch to workers
)

type ExecutionOptions struct {
    // ... existing options
    Mode      ExecutionMode
    TaskQueue TaskQueue  // Required if Mode == ExecutionModeDistributed
}
```

#### 4.6 Path State Machine

In distributed mode, paths become state machines that checkpoint and yield when dispatching work:

```go
type PathStatus string

const (
    PathStatusRunning      PathStatus = "running"
    PathStatusAwaitingTask PathStatus = "awaiting_task"  // New: waiting for activity result
    PathStatusCompleted    PathStatus = "completed"
    PathStatusFailed       PathStatus = "failed"
)

// PathState additions
type PathState struct {
    // ... existing fields
    PendingTaskID string `json:"pending_task_id,omitempty"` // Task we're waiting for
}
```

#### 4.7 Distributed Execution Flow

```go
func (e *Execution) runDistributed(ctx context.Context) error {
    // Start result listener
    results, err := e.taskQueue.Results(ctx)
    if err != nil {
        return err
    }

    // Main event loop
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()

        case snapshot := <-e.pathSnapshots:
            // Path wants to execute an activity
            if snapshot.ActivityRequest != nil {
                if err := e.dispatchActivity(ctx, snapshot); err != nil {
                    e.failPath(snapshot.PathID, err)
                }
            } else {
                // Normal path processing (branching, joins, completion)
                e.processPathSnapshot(ctx, snapshot)
            }

        case result := <-results:
            // Worker completed an activity
            e.handleActivityResult(ctx, result)
        }

        if e.isComplete() {
            break
        }
    }

    return e.saveCheckpoint(ctx)
}

func (e *Execution) dispatchActivity(ctx context.Context, snapshot PathSnapshot) error {
    req := snapshot.ActivityRequest

    task := &ActivityTask{
        TaskID:      uuid.New().String(),
        ExecutionID: e.state.ExecutionID(),
        PathID:      snapshot.PathID,
        StepName:    req.StepName,
        Activity:    req.Activity,
        Parameters:  req.Parameters,
        Variables:   snapshot.Variables,
        Inputs:      e.state.Inputs(),
        Timeout:     req.Timeout,
    }

    // Update path state: now awaiting this task
    e.state.UpdatePathState(snapshot.PathID, func(ps *PathState) {
        ps.Status = string(PathStatusAwaitingTask)
        ps.PendingTaskID = task.TaskID
        ps.Variables = snapshot.Variables  // Snapshot current state
    })

    // Checkpoint BEFORE dispatching (crash safety)
    if err := e.saveCheckpoint(ctx); err != nil {
        return fmt.Errorf("checkpoint before dispatch: %w", err)
    }

    // Dispatch to queue
    if err := e.taskQueue.Enqueue(ctx, task); err != nil {
        return fmt.Errorf("enqueue task: %w", err)
    }

    return nil
}

func (e *Execution) handleActivityResult(ctx context.Context, result *ActivityResult) {
    pathState := e.state.GetPathState(result.PathID)
    if pathState == nil || pathState.PendingTaskID != result.TaskID {
        // Stale result (maybe from a previous execution attempt)
        return
    }

    // Restore path and continue execution
    path := e.reconstitutePath(pathState)

    if result.Error != nil {
        // Handle error (retry logic, catch handlers)
        e.handleActivityError(ctx, path, result)
    } else {
        // Store output and advance
        path.SetStepOutput(result.StepName, result.Output)
        path.AdvanceToNextStep()

        // Resume path execution
        e.runPath(ctx, path)
    }
}
```

### Consistency Guarantees

#### At-Least-Once Delivery

With distributed workers, activities may execute more than once if:
- Worker crashes after completing but before acking
- Network partition during result delivery
- Orchestrator crashes after dispatching

**Mitigation**: Activities should be idempotent where possible. For non-idempotent activities:

```go
type ActivityTask struct {
    // ... existing fields
    IdempotencyKey string `json:"idempotency_key"` // Caller-provided dedup key
}

// Activities can check for duplicate execution
func (a *PaymentActivity) Execute(ctx Context, params PaymentParams) (any, error) {
    // Check if already processed
    if exists, result := a.checkIdempotency(params.IdempotencyKey); exists {
        return result, nil
    }

    // Process payment...

    // Record completion
    a.recordIdempotency(params.IdempotencyKey, result)
    return result, nil
}
```

#### Exactly-Once State Transitions

The orchestrator ensures each task result is processed exactly once:

```go
func (e *Execution) handleActivityResult(ctx context.Context, result *ActivityResult) {
    e.state.UpdatePathState(result.PathID, func(ps *PathState) {
        // Idempotent check: only process if still waiting for this task
        if ps.PendingTaskID != result.TaskID {
            return // Already processed or different task
        }

        // Clear pending task (prevents reprocessing)
        ps.PendingTaskID = ""
        ps.Status = string(PathStatusRunning)
        // ... apply result
    })
}
```

### Recovery Scenarios

#### Orchestrator Crash

1. On restart, load checkpoint
2. For paths in `awaiting_task` state, check queue for results
3. If result found: process and continue
4. If no result and task expired: re-dispatch or fail

```go
func (e *Execution) recoverAwaitingPaths(ctx context.Context) error {
    for pathID, ps := range e.state.PathStates() {
        if ps.Status != string(PathStatusAwaitingTask) {
            continue
        }

        // Check if result is available
        result, err := e.taskQueue.GetResult(ctx, ps.PendingTaskID)
        if err == nil && result != nil {
            e.handleActivityResult(ctx, result)
            continue
        }

        // Check if task is still pending/processing
        task, err := e.taskQueue.GetTask(ctx, ps.PendingTaskID)
        if err == nil && task != nil {
            // Task still in flight, wait for result
            continue
        }

        // Task lost - re-dispatch or fail based on policy
        if e.opts.RetryLostTasks {
            e.redispatchTask(ctx, pathID, ps)
        } else {
            e.failPath(pathID, errors.New("task lost during recovery"))
        }
    }
    return nil
}
```

#### Worker Crash

1. Task lease expires
2. Queue makes task visible again
3. Another worker picks it up
4. Activity executes again (idempotency important)

### Configuration

```go
// Distributed execution configuration
type DistributedConfig struct {
    // Queue selection
    QueueType       string            // "postgres", "cloudtasks", "memory"
    QueueConfig     map[string]string // Queue-specific config

    // Worker configuration (if managing workers)
    WorkerType      string            // "local", "sprites"
    WorkerConfig    map[string]string

    // Behavior
    TaskTimeout     time.Duration     // Default activity timeout
    RetryLostTasks  bool              // Re-dispatch tasks lost during recovery
    ResultTTL       time.Duration     // How long to keep results
}

// Example: Postgres queue with Sprites workers
config := DistributedConfig{
    QueueType: "postgres",
    QueueConfig: map[string]string{
        "connection_string": "postgres://...",
        "poll_interval":     "100ms",
    },
    WorkerType: "sprites",
    WorkerConfig: map[string]string{
        "min_workers":  "1",
        "max_workers":  "10",
        "idle_timeout": "5m",
        "sprite_image": "ghcr.io/deepnoodle/workflow-worker:latest",
    },
}
```

### Trade-offs

| Aspect | Local Mode | Distributed Mode |
|--------|------------|------------------|
| **Latency** | Microseconds (function call) | Milliseconds (queue + network) |
| **Scalability** | Single process | Horizontal (multiple workers) |
| **Fault tolerance** | Process crash loses in-flight | Workers can fail independently |
| **Complexity** | Simple | Queue + worker management |
| **Cost** | Fixed (server always on) | Variable (pay per use with Sprites) |
| **Consistency** | Exactly-once (in-process) | At-least-once (need idempotency) |

### What This Enables

1. **Burst capacity**: Spin up Sprites workers for large fan-outs
2. **Cost optimization**: No idle compute when workflows are quiet
3. **Isolation**: Activities run in separate VMs (security, resource limits)
4. **Language flexibility**: Workers could be implemented in any language
5. **Geographic distribution**: Workers closer to data sources

---

## Implementation Roadmap (Updated)

### Phase 1: Operational Safety
- [ ] Add `MaxConcurrentPaths` option with semaphore
- [ ] Implement `Shutdown()` with draining support
- [ ] Add `IsDraining()` check in path loop

### Phase 2: Persistence Guarantees
- [ ] Add `CheckpointPolicy` option
- [ ] Implement atomic checkpoint writes in `FileCheckpointer`
- [ ] Synchronous checkpoint on completion under strict policy

### Phase 3: Execution Management
- [ ] Define `CheckpointerWithListing` interface
- [ ] Implement `ListExecutions` for `FileCheckpointer`
- [ ] Add `RecoverOrphanedExecutions` function

### Phase 4: Distributed Workers
- [ ] Define `TaskQueue` interface
- [ ] Implement `MemoryQueue` for testing
- [ ] Implement `PostgresQueue`
- [ ] Implement `CloudTasksQueue`
- [ ] Add `ExecutionMode` option
- [ ] Implement distributed execution loop
- [ ] Add path state machine changes (`awaiting_task`)
- [ ] Implement recovery for awaiting paths

### Phase 5: Sprites Integration
- [ ] Implement `SpritesWorkerPool`
- [ ] Build worker Docker image with activity registry
- [ ] Implement autoscaling logic
- [ ] Add idle sprite reaping

---

## Conclusion

The workflow library has a solid foundation with well-thought-out concurrency patterns and comprehensive checkpointing. The proposed enhancements address operational gaps and add distributed execution capability:

1. **Bounded concurrency** prevents resource exhaustion
2. **Graceful shutdown** preserves in-flight work
3. **Checkpoint integrity** ensures durable state
4. **Execution listing** enables operational tooling
5. **Orphan recovery** maintains consistent state across restarts
6. **Distributed workers** enable horizontal scaling and on-demand compute
7. **Queue abstraction** supports multiple backends (Postgres, Cloud Tasks)
8. **Sprites integration** provides pay-per-use worker execution

The distributed architecture introduces at-least-once semantics, requiring idempotent activities for correctness. The trade-off is increased complexity and latency in exchange for scalability and cost efficiency.
