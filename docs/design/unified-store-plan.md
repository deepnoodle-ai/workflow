# Unified Store & Dumb Worker Architecture

## Overview

Replace the separate ExecutionStore + WorkQueue with a unified ExecutionStore that handles both state persistence and work distribution. Workers become "dumb" HTTP clients that execute containerized tasks without needing workflow SDK code.

## Goals

1. **Simplify** - Single store interface, single table, no coordination between store and queue
2. **Dumb workers** - Workers speak HTTP, run containers, report results. No workflow SDK needed.
3. **Two modes** - In-process engine and HTTP orchestrator service, both natural
4. **Postgres + Memory** - Production and testing backends only

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         Orchestrator                            │
│  ┌──────────────────┐  ┌──────────────────┐  ┌───────────────┐  │
│  │  ExecutionStore  │  │  WorkflowEngine  │  │   HTTP API    │  │
│  │                  │  │                  │  │               │  │
│  │  - Create        │◀─│  - Submit        │◀─│ POST /submit  │  │
│  │  - Claim         │  │  - RunStep       │  │ POST /claim   │  │
│  │  - Complete      │  │  - Advance       │  │ POST /complete│  │
│  │  - Heartbeat     │  │                  │  │ POST /heartbeat│ │
│  │  - Release       │  │                  │  │ GET /status   │  │
│  └──────────────────┘  └──────────────────┘  └───────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                                   ▲
                                   │ HTTP
                    ┌──────────────┴──────────────┐
                    ▼                             ▼
              ┌──────────┐                  ┌──────────┐
              │  Worker  │                  │  Worker  │
              │  (dumb)  │                  │  (dumb)  │
              └──────────┘                  └──────────┘
```

## Components

### 1. ExecutionStore Interface

Unified interface for state + work distribution:

```go
type ExecutionStore interface {
    // Lifecycle
    Create(ctx context.Context, record *ExecutionRecord) error
    Claim(ctx context.Context, workerID string) (*TaskRecord, error)
    Complete(ctx context.Context, taskID string, result *TaskResult) error
    Release(ctx context.Context, taskID string, retryAfter time.Duration) error
    Heartbeat(ctx context.Context, taskID string, workerID string) error

    // Queries
    GetExecution(ctx context.Context, id string) (*ExecutionRecord, error)
    GetTask(ctx context.Context, id string) (*TaskRecord, error)
    ListExecutions(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error)

    // Recovery
    ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*TaskRecord, error)
    ResetTask(ctx context.Context, taskID string) error
}
```

### 2. Data Model

**ExecutionRecord** - Workflow execution state:
```go
type ExecutionRecord struct {
    ID            string
    WorkflowName  string
    Status        ExecutionStatus  // pending/running/completed/failed
    Inputs        map[string]any
    Outputs       map[string]any
    CurrentStep   string
    StepStates    map[string]*StepState
    CreatedAt     time.Time
    CompletedAt   time.Time
    LastError     string
}
```

**TaskRecord** - Unit of work for workers:
```go
type TaskRecord struct {
    ID            string  // {execution_id}_{step}_{attempt}
    ExecutionID   string
    StepName      string
    Attempt       int
    Status        TaskStatus  // pending/running/completed/failed

    // What to run (for dumb workers)
    Spec          *TaskSpec

    // Claiming
    WorkerID      string
    VisibleAt     time.Time
    LastHeartbeat time.Time

    // Result
    Result        *TaskResult

    CreatedAt     time.Time
    StartedAt     time.Time
    CompletedAt   time.Time
}

type TaskSpec struct {
    Image     string            `json:"image"`
    Command   []string          `json:"command,omitempty"`
    Env       map[string]string `json:"env,omitempty"`
    Timeout   time.Duration     `json:"timeout,omitempty"`

    // For non-container execution
    Type      string            `json:"type,omitempty"`  // "container", "http", "script"
    URL       string            `json:"url,omitempty"`   // for http type
    Script    string            `json:"script,omitempty"` // for script type
}

type TaskResult struct {
    Success   bool              `json:"success"`
    Output    string            `json:"output,omitempty"`
    Error     string            `json:"error,omitempty"`
    ExitCode  int               `json:"exit_code,omitempty"`
    Data      map[string]any    `json:"data,omitempty"`
}
```

### 3. Database Schema (Postgres)

```sql
-- Workflow executions
CREATE TABLE workflow_executions (
    id             TEXT PRIMARY KEY,
    workflow_name  TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    inputs         JSONB NOT NULL,
    outputs        JSONB,
    current_step   TEXT,
    step_states    JSONB,
    last_error     TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ
);

-- Tasks (work items for workers)
CREATE TABLE workflow_tasks (
    id              TEXT PRIMARY KEY,
    execution_id    TEXT NOT NULL REFERENCES workflow_executions(id),
    step_name       TEXT NOT NULL,
    attempt         INTEGER NOT NULL DEFAULT 1,
    status          TEXT NOT NULL DEFAULT 'pending',

    spec            JSONB NOT NULL,

    worker_id       TEXT,
    visible_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_heartbeat  TIMESTAMPTZ,

    result          JSONB,

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    UNIQUE(execution_id, step_name, attempt)
);

CREATE INDEX idx_tasks_claimable ON workflow_tasks(visible_at)
    WHERE status = 'pending';
CREATE INDEX idx_tasks_stale ON workflow_tasks(last_heartbeat)
    WHERE status = 'running';
```

### 4. Activity Runner Interface

Activities define how to convert parameters to TaskSpec:

```go
type ActivityRunner interface {
    // ToSpec converts activity parameters to a TaskSpec for workers
    ToSpec(ctx context.Context, params map[string]any) (*TaskSpec, error)

    // ParseResult interprets worker output as activity result
    ParseResult(result *TaskResult) (map[string]any, error)
}

// ContainerRunner runs activities as containers
type ContainerRunner struct {
    Image   string
    Command []string
}

// HTTPRunner calls an HTTP endpoint
type HTTPRunner struct {
    URL    string
    Method string
}

// InlineRunner for in-process execution (testing, simple activities)
type InlineRunner struct {
    Func func(ctx context.Context, params map[string]any) (map[string]any, error)
}
```

### 5. Orchestrator HTTP API

```
POST   /v1/executions              Create new execution
GET    /v1/executions/{id}         Get execution status
GET    /v1/executions              List executions

POST   /v1/tasks/claim             Claim next available task
POST   /v1/tasks/{id}/heartbeat    Worker heartbeat
POST   /v1/tasks/{id}/complete     Report task completion
POST   /v1/tasks/{id}/release      Release task for retry
GET    /v1/tasks/{id}              Get task details
```

### 6. Worker Package

Standalone package that workers import:

```go
package worker

type Worker struct {
    orchestratorURL string
    workerID        string
    executor        Executor
    heartbeatInterval time.Duration
}

type Executor interface {
    Execute(ctx context.Context, spec *TaskSpec) (*TaskResult, error)
}

// ContainerExecutor runs Docker containers
type ContainerExecutor struct { ... }

// ProcessExecutor runs local processes
type ProcessExecutor struct { ... }

func (w *Worker) Run(ctx context.Context) error {
    for {
        task, err := w.claim(ctx)
        if err != nil { ... }
        if task == nil {
            time.Sleep(pollInterval)
            continue
        }

        // Start heartbeat
        hbCtx, cancel := context.WithCancel(ctx)
        go w.heartbeatLoop(hbCtx, task.ID)

        // Execute
        result := w.executor.Execute(ctx, task.Spec)

        // Report
        cancel()
        w.complete(ctx, task.ID, result)
    }
}
```

### 7. In-Process Engine Mode

For simpler deployments, engine can run tasks directly:

```go
engine := workflow.NewEngine(EngineOptions{
    Store:    store,
    Mode:     EngineModeLocal,  // vs EngineModeOrchestrator
    Executor: &ProcessExecutor{},
})

// Engine internally claims and executes tasks
engine.Start(ctx)
engine.Submit(ctx, req)
```

---

## Implementation Plan

### Phase 1: Core Types & Store Interface
- [ ] Define new types: TaskRecord, TaskSpec, TaskResult, TaskStatus
- [ ] Define unified ExecutionStore interface
- [ ] Define ActivityRunner interface
- [ ] Update ExecutionRecord for new model

### Phase 2: Memory Store
- [ ] Implement MemoryStore with new interface
- [ ] Claim uses mutex, returns nil if no work
- [ ] Support visibility delay for retries
- [ ] Unit tests

### Phase 3: Postgres Store
- [ ] Create new schema (workflow_executions, workflow_tasks)
- [ ] Implement PostgresStore with FOR UPDATE SKIP LOCKED
- [ ] Heartbeat and stale detection
- [ ] Integration tests

### Phase 4: Activity Runners
- [ ] ContainerRunner (Docker)
- [ ] HTTPRunner (webhook-style)
- [ ] InlineRunner (for testing / simple cases)
- [ ] ProcessRunner (subprocess)

### Phase 5: Workflow Engine Updates
- [ ] Engine creates tasks when advancing workflow
- [ ] Engine processes task results and advances state
- [ ] Support both local and orchestrator modes
- [ ] Reaper for stale tasks

### Phase 6: HTTP Orchestrator
- [ ] HTTP handlers for all endpoints
- [ ] Request/response types
- [ ] Authentication hooks
- [ ] OpenAPI spec

### Phase 7: Worker Package
- [ ] Worker struct with claim loop
- [ ] Heartbeat goroutine
- [ ] ContainerExecutor (Docker SDK)
- [ ] ProcessExecutor (os/exec)
- [ ] CLI binary

### Phase 8: Cleanup
- [ ] Remove old queue.go, queue_*.go
- [ ] Remove old store interface methods
- [ ] Update all tests
- [ ] Update documentation

---

## Files to Create/Modify

### New Files
```
workflow/
├── task.go                    # TaskRecord, TaskSpec, TaskResult types
├── runner.go                  # ActivityRunner interface + implementations
├── store_memory.go            # Updated MemoryStore
├── store_postgres.go          # Updated PostgresStore
├── orchestrator/
│   ├── server.go              # HTTP server
│   ├── handlers.go            # HTTP handlers
│   └── types.go               # API request/response types
└── worker/
    ├── worker.go              # Worker struct and loop
    ├── executor.go            # Executor interface
    ├── container_executor.go  # Docker execution
    ├── process_executor.go    # Subprocess execution
    └── cmd/
        └── worker/
            └── main.go        # Worker CLI
```

### Files to Remove
```
queue.go
queue_memory.go
queue_postgres.go
```

### Files to Modify
```
engine.go           # Use new store interface
engine_process.go   # Task-based processing
store.go            # New interface
```

---

## Migration Notes

This is a breaking change. The new design:
- Removes WorkQueue interface entirely
- Changes ExecutionStore interface significantly
- Adds tasks table for work distribution
- Changes how activities are defined (need runners)

No backwards compatibility - clean break.
