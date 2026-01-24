# Architecture Refactoring Plan

## Summary

Reorganize the codebase into a layered architecture following Domain-Driven Design principles with clear separation between domain, service, and infrastructure layers.

## Current State

Everything is in the root `workflow` package:
- Domain types (Workflow, Step, Activity, Execution, Path)
- Infrastructure (PostgresStore, MemoryStore, Engine)
- Execution logic
- Transport (none yet, planned HTTP)

**Problems:**
1. No clear boundaries between layers
2. Domain types have infrastructure dependencies (yaml, json tags)
3. "Store" mixes repository pattern with application logic
4. Difficult to test in isolation
5. Hard to understand what depends on what

## Proposed Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Public API                                      │
│  workflow/                                                                   │
│  └── Domain types only: Workflow, Step, Activity, Execution, etc.           │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Service Layer                                      │
│  internal/services/                                                          │
│  └── Application use cases: ExecutionService, TaskService                    │
│  └── Orchestrates domain objects, uses repositories                          │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         Repository Interfaces                                │
│  internal/repository/                                                        │
│  └── Interfaces: ExecutionRepository, TaskRepository                         │
│  └── Defined by service layer needs, implemented by infrastructure           │
└─────────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Infrastructure                                     │
│  internal/postgres/    → PostgreSQL implementations                          │
│  internal/memory/      → In-memory implementations (testing)                 │
│  internal/http/        → HTTP transport layer                                │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Layer Definitions

### 1. Domain Layer (`workflow/`)

**Purpose:** Core business concepts, pure Go, no external dependencies.

**Contents:**
- `Workflow`, `Step`, `Edge`, `Input`, `Output` - Workflow definition
- `Activity` - Activity interface
- `Execution`, `Path` - Execution model
- `Task`, `TaskSpec`, `TaskResult` - Task model
- `ExecutionRecord` - Persistent execution state
- Domain errors

**Rules:**
- NO infrastructure imports (database, HTTP, etc.)
- NO `internal/` imports
- CAN be imported by any other layer
- Types use struct tags only for serialization (`json`, `yaml`)

**Public API:** This is what external consumers import.

```go
package workflow

// Core domain types - dependency free
type Workflow struct { ... }
type Step struct { ... }
type Activity interface { ... }
type Execution struct { ... }
type Task struct { ... }
type ExecutionRecord struct { ... }

// Domain errors
type WorkflowError struct { ... }
```

### 2. Repository Interfaces (`internal/repository/`)

**Purpose:** Define storage contracts needed by services.

**Contents:**
- `ExecutionRepository` - CRUD for executions
- `TaskRepository` - Task lifecycle (claim, complete, etc.)
- `EventRepository` - Event logging

**Rules:**
- Interfaces only (no implementations)
- Uses domain types from `workflow/`
- Defined by what services need, not by storage capabilities

```go
package repository

import "github.com/deepnoodle-ai/workflow"

// ExecutionRepository handles execution persistence.
type ExecutionRepository interface {
    Create(ctx context.Context, record *workflow.ExecutionRecord) error
    Get(ctx context.Context, id string) (*workflow.ExecutionRecord, error)
    Update(ctx context.Context, record *workflow.ExecutionRecord) error
    List(ctx context.Context, filter workflow.ExecutionFilter) ([]*workflow.ExecutionRecord, error)
}

// TaskRepository handles task lifecycle.
type TaskRepository interface {
    Create(ctx context.Context, task *workflow.TaskRecord) error
    Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error)
    Complete(ctx context.Context, taskID, workerID string, result *workflow.TaskResult) error
    Heartbeat(ctx context.Context, taskID, workerID string) error
    Release(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error
    ListStale(ctx context.Context, cutoff time.Time) ([]*workflow.TaskRecord, error)
    Reset(ctx context.Context, taskID string) error
}
```

### 3. Service Layer (`internal/services/`)

**Purpose:** Application use cases, business logic orchestration.

**Contents:**
- `ExecutionService` - Submit, cancel, get executions
- `TaskService` - Task orchestration (for workers)
- `OrchestratorService` - Engine-level coordination

**Rules:**
- Uses domain types from `workflow/`
- Depends on repository interfaces, not implementations
- Contains application-level business logic
- Handles transactions, coordination

```go
package services

import (
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/internal/repository"
)

type ExecutionService struct {
    executions repository.ExecutionRepository
    tasks      repository.TaskRepository
    events     repository.EventRepository
}

func (s *ExecutionService) Submit(ctx context.Context, wf *workflow.Workflow, inputs map[string]any) (*workflow.ExecutionHandle, error) {
    // Create execution record
    // Create first task
    // Return handle
}

func (s *ExecutionService) Get(ctx context.Context, id string) (*workflow.ExecutionRecord, error) {
    return s.executions.Get(ctx, id)
}

type TaskService struct {
    tasks repository.TaskRepository
}

func (s *TaskService) Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
    return s.tasks.Claim(ctx, workerID)
}

func (s *TaskService) Complete(ctx context.Context, taskID, workerID string, result *workflow.TaskResult) error {
    return s.tasks.Complete(ctx, taskID, workerID, result)
}
```

### 4. Infrastructure Layer

#### PostgreSQL (`internal/postgres/`)

**Purpose:** PostgreSQL repository implementations.

```go
package postgres

import (
    "database/sql"
    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/internal/repository"
)

type ExecutionRepository struct {
    db *sql.DB
}

func NewExecutionRepository(db *sql.DB) *ExecutionRepository {
    return &ExecutionRepository{db: db}
}

func (r *ExecutionRepository) Create(ctx context.Context, record *workflow.ExecutionRecord) error {
    // SQL INSERT
}

// Verify interface compliance
var _ repository.ExecutionRepository = (*ExecutionRepository)(nil)
```

#### Memory (`internal/memory/`)

**Purpose:** In-memory implementations for testing.

```go
package memory

type ExecutionRepository struct {
    mu         sync.RWMutex
    executions map[string]*workflow.ExecutionRecord
}

var _ repository.ExecutionRepository = (*ExecutionRepository)(nil)
```

#### HTTP (`internal/http/`)

**Purpose:** HTTP transport layer (server and client).

```go
package http

// Server wraps services and exposes HTTP API
type Server struct {
    executions *services.ExecutionService
    tasks      *services.TaskService
}

func (s *Server) HandleClaimTask(w http.ResponseWriter, r *http.Request) {
    workerID := r.Header.Get("X-Worker-ID")
    task, err := s.tasks.Claim(r.Context(), workerID)
    // ...
}

// Client implements repository interfaces over HTTP
type TaskClient struct {
    baseURL string
    http    *http.Client
}

func (c *TaskClient) Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
    // HTTP POST to /tasks/claim
}

var _ repository.TaskRepository = (*TaskClient)(nil)
```

---

## Directory Structure

```
workflow/
├── workflow.go              # Workflow, Step, Edge, Input, Output
├── activity.go              # Activity interface
├── execution.go             # Execution, Path (runtime)
├── task.go                  # Task, TaskSpec, TaskResult, ClaimedTask
├── records.go               # ExecutionRecord, TaskRecord (persistence)
├── errors.go                # Domain errors
├── context.go               # workflow.Context
├── clock.go                 # Clock interface
│
├── internal/
│   ├── repository/
│   │   ├── execution.go     # ExecutionRepository interface
│   │   ├── task.go          # TaskRepository interface
│   │   └── event.go         # EventRepository interface
│   │
│   ├── services/
│   │   ├── execution.go     # ExecutionService
│   │   ├── task.go          # TaskService
│   │   ├── orchestrator.go  # OrchestratorService (engine logic)
│   │   └── reaper.go        # ReaperService (stale task recovery)
│   │
│   ├── postgres/
│   │   ├── execution.go     # PostgreSQL ExecutionRepository
│   │   ├── task.go          # PostgreSQL TaskRepository
│   │   ├── event.go         # PostgreSQL EventRepository
│   │   ├── schema.go        # Schema creation
│   │   └── postgres_test.go # Integration tests
│   │
│   ├── memory/
│   │   ├── execution.go     # In-memory ExecutionRepository
│   │   ├── task.go          # In-memory TaskRepository
│   │   └── event.go         # In-memory EventRepository
│   │
│   └── http/
│       ├── server.go        # HTTP server
│       ├── handlers.go      # HTTP handlers
│       ├── client.go        # HTTP client (TaskRepository over HTTP)
│       └── auth.go          # Authentication middleware
│
├── cmd/
│   ├── orchestrator/
│   │   └── main.go          # Orchestrator binary
│   └── worker/
│       └── main.go          # Worker binary
│
├── activities/              # Built-in activities (unchanged)
│   ├── http_activity.go
│   ├── shell_activity.go
│   └── ...
│
├── ai/                      # AI extensions (unchanged)
│   └── ...
│
└── script/                  # Scripting support (unchanged)
    └── ...
```

---

## Migration Strategy

### Phase 1: Extract Domain Types

1. Create clean domain types in root package
2. Remove infrastructure dependencies
3. Keep backwards compatibility via type aliases if needed

**Files to create/modify:**
- `workflow.go` - Already clean, keep as-is
- `step.go` - Already clean, keep as-is
- `activity.go` - Already clean, keep as-is
- `task.go` - Move to root, remove store-specific concerns
- `records.go` - New file for ExecutionRecord, TaskRecord
- `errors.go` - Consolidate domain errors

### Phase 2: Create Repository Interfaces

1. Create `internal/repository/` package
2. Define interfaces based on current store methods
3. Split by aggregate root (Execution, Task, Event)

**Files to create:**
- `internal/repository/execution.go`
- `internal/repository/task.go`
- `internal/repository/event.go`

### Phase 3: Move Infrastructure

1. Move `store_postgres.go` → `internal/postgres/`
2. Move `store_memory.go` → `internal/memory/`
3. Split into separate repository implementations
4. Update to implement new interfaces

**Files to move/split:**
- `store_postgres.go` → `internal/postgres/execution.go`, `internal/postgres/task.go`
- `store_memory.go` → `internal/memory/execution.go`, `internal/memory/task.go`
- `event_log_postgres.go` → `internal/postgres/event.go`
- `event_log.go` (memory) → `internal/memory/event.go`

### Phase 4: Create Service Layer

1. Create `internal/services/` package
2. Extract engine logic into services
3. Services depend on repository interfaces

**Files to create:**
- `internal/services/execution.go` - Submit, Get, List, Cancel
- `internal/services/task.go` - Claim, Complete, Heartbeat
- `internal/services/orchestrator.go` - Engine coordination
- `internal/services/reaper.go` - Stale detection

### Phase 5: Add HTTP Layer

1. Create `internal/http/` package
2. Server wraps services
3. Client implements TaskRepository

**Files to create:**
- `internal/http/server.go`
- `internal/http/handlers.go`
- `internal/http/client.go`
- `internal/http/auth.go`

### Phase 6: Update Binaries

1. Update `cmd/orchestrator/main.go` to wire everything together
2. Update `cmd/worker/main.go` to use HTTP client

---

## Dependency Rules

```
workflow/ (domain)
    ↑
    │ imports
    │
internal/repository/ (interfaces)
    ↑
    │ imports & implements
    │
internal/services/ ←──────────────── internal/postgres/
    ↑                                internal/memory/
    │ imports                        internal/http/client
    │
internal/http/server ────────────────────────────────────┘
    ↑
    │
cmd/orchestrator/
cmd/worker/
```

**Key rules:**
1. `workflow/` imports nothing from `internal/`
2. `internal/repository/` only imports `workflow/`
3. `internal/services/` imports `workflow/` and `internal/repository/`
4. Infrastructure implements `internal/repository/` interfaces
5. `internal/http/server` imports `internal/services/`
6. `cmd/` wires everything together

---

## Public API Surface

After refactoring, external consumers import:

```go
import "github.com/deepnoodle-ai/workflow"

// Create workflow
wf, _ := workflow.New(workflow.Options{...})

// Domain types
var exec *workflow.Execution
var task *workflow.Task
var record *workflow.ExecutionRecord
```

For infrastructure (if needed externally):

```go
import "github.com/deepnoodle-ai/workflow/postgres"  // If we expose this

// Or consumers wire their own
import "github.com/deepnoodle-ai/workflow/internal/postgres"  // Not recommended
```

---

## Testing Strategy

1. **Domain tests** - Pure unit tests, no mocks needed
2. **Service tests** - Mock repository interfaces
3. **Repository tests** - In-memory for unit, Postgres for integration
4. **HTTP tests** - httptest with mock services
5. **E2E tests** - Full stack with testcontainers

```go
// Service test with mock repository
func TestExecutionService_Submit(t *testing.T) {
    mockRepo := &mockExecutionRepository{}
    mockTasks := &mockTaskRepository{}

    svc := services.NewExecutionService(mockRepo, mockTasks)

    handle, err := svc.Submit(ctx, wf, inputs)
    // ...
}
```

---

## Backwards Compatibility

To maintain backwards compatibility during migration:

```go
// workflow/compat.go (temporary)
package workflow

import (
    "github.com/deepnoodle-ai/workflow/internal/postgres"
    "github.com/deepnoodle-ai/workflow/internal/services"
)

// Deprecated: Use internal/postgres.NewExecutionRepository
func NewPostgresStore(opts PostgresStoreOptions) *PostgresStore {
    return &PostgresStore{
        executions: postgres.NewExecutionRepository(opts.DB),
        tasks:      postgres.NewTaskRepository(opts.DB),
    }
}

// Engine wraps the new services for backwards compatibility
type Engine struct {
    exec *services.ExecutionService
    // ...
}
```

---

## Open Questions

1. **Should activities move to `internal/`?** - Probably not, they're part of the public API
2. **What about `ai/` package?** - Keep as-is, it extends the domain
3. **Should we expose repository interfaces publicly?** - Probably not initially
4. **Transaction boundaries?** - Services should handle, repositories are single-operation
5. **Config types?** - Keep in domain or separate config package?

---

## Implementation Order

1. [ ] Create `internal/repository/` interfaces
2. [ ] Extract `ExecutionRecord`, `TaskRecord` to `records.go`
3. [ ] Create `internal/memory/` implementations
4. [ ] Create `internal/postgres/` implementations
5. [ ] Create `internal/services/` layer
6. [ ] Create `internal/http/` transport
7. [ ] Update `cmd/` binaries
8. [ ] Add backwards compatibility shims
9. [ ] Update tests
10. [ ] Remove deprecated code
