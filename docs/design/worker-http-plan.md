# Worker HTTP/WebSocket Communication Plan

## Summary

Add an HTTP transport layer that wraps the service layer, enabling workers to communicate with the server over HTTP instead of direct database access. The worker uses an HTTP client that implements `TaskRepository`, making the core logic transport-agnostic.

## Architecture

### Current: Direct Database Access

```
┌──────────────┐         ┌──────────────┐
│   Worker     │────────▶│   Postgres   │
│ (PostgresStore)        │              │
└──────────────┘         └──────────────┘
```

### Proposed: Layered Architecture with HTTP Transport

```
                                              ┌───────────────────────┐
                                              │  workflow/ (domain)   │
                                              │  Workflow, Task, etc. │
                                              └───────────┬───────────┘
                                                          │
                    ┌─────────────────────────────────────┼─────────────────────────────────────┐
                    │                                     │                                     │
                    ▼                                     ▼                                     ▼
         ┌─────────────────────┐             ┌─────────────────────┐             ┌─────────────────────┐
         │ internal/repository │             │ internal/services   │             │ internal/http       │
         │                     │             │                     │             │                     │
         │ TaskRepository      │◀────────────│ TaskService         │◀────────────│ Server (transport)  │
         │ ExecutionRepository │             │ ExecutionService    │             │                     │
         └─────────┬───────────┘             └─────────────────────┘             └─────────┬───────────┘
                   │                                                                       │
       ┌───────────┼───────────┐                                                           │
       │           │           │                                                           │
       ▼           ▼           ▼                                                           ▼
┌───────────┐ ┌───────────┐ ┌───────────┐                                         ┌───────────────┐
│  memory/  │ │ postgres/ │ │  http/    │◀────────────────────────────────────────│    Worker     │
│ (testing) │ │ (server)  │ │  Client   │         HTTP/WebSocket                  │ (http client) │
└───────────┘ └───────────┘ └───────────┘                                         └───────────────┘
```

**Key insight:** The HTTP layer is just a transport. The server wraps `TaskService`, and the client implements `TaskRepository`. Workers use the same interface regardless of transport.

---

## Repository Interface (Worker Subset)

Workers only need task lifecycle methods. The `TaskRepository` interface from `internal/repository/` defines this:

```go
// internal/repository/task.go
package repository

import (
    "context"
    "time"

    "github.com/deepnoodle-ai/workflow"
)

// TaskRepository handles task lifecycle operations.
type TaskRepository interface {
    Create(ctx context.Context, task *workflow.TaskRecord) error
    Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error)
    Complete(ctx context.Context, taskID, workerID string, result *workflow.TaskOutput) error
    Release(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error
    Heartbeat(ctx context.Context, taskID, workerID string) error
    Get(ctx context.Context, id string) (*workflow.TaskRecord, error)
    ListStale(ctx context.Context, cutoff time.Time) ([]*workflow.TaskRecord, error)
    Reset(ctx context.Context, taskID string) error
}
```

Workers need only: `Claim`, `Complete`, `Release`, `Heartbeat`.

---

## Service Layer

The service layer in `internal/services/` provides application-level coordination:

```go
// internal/services/task.go
package services

import (
    "context"
    "time"

    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/internal/repository"
)

// TaskService provides task lifecycle operations.
type TaskService struct {
    tasks  repository.TaskRepository
    events repository.EventRepository
}

func NewTaskService(tasks repository.TaskRepository, events repository.EventRepository) *TaskService {
    return &TaskService{tasks: tasks, events: events}
}

func (s *TaskService) Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
    task, err := s.tasks.Claim(ctx, workerID)
    if err != nil {
        return nil, err
    }
    if task != nil {
        // Log event
        s.events.Append(ctx, workflow.Event{
            Type: workflow.EventTaskClaimed,
            Data: map[string]any{"task_id": task.ID, "worker_id": workerID},
        })
    }
    return task, nil
}

func (s *TaskService) Complete(ctx context.Context, taskID, workerID string, result *workflow.TaskOutput) error {
    if err := s.tasks.Complete(ctx, taskID, workerID, result); err != nil {
        return err
    }
    s.events.Append(ctx, workflow.Event{
        Type: workflow.EventTaskCompleted,
        Data: map[string]any{"task_id": taskID, "worker_id": workerID, "success": result.Success},
    })
    return nil
}

func (s *TaskService) Heartbeat(ctx context.Context, taskID, workerID string) error {
    return s.tasks.Heartbeat(ctx, taskID, workerID)
}

func (s *TaskService) Release(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error {
    return s.tasks.Release(ctx, taskID, workerID, retryAfter)
}
```

---

## HTTP Server (Transport Layer)

The HTTP server in `internal/http/` wraps services and exposes them over HTTP:

```go
// internal/http/server.go
package http

import (
    "net/http"

    "github.com/deepnoodle-ai/workflow/internal/services"
)

// Server wraps services and exposes HTTP API.
type Server struct {
    tasks      *services.TaskService
    executions *services.ExecutionService
    auth       Authenticator
    addr       string
}

type ServerOptions struct {
    TaskService      *services.TaskService
    ExecutionService *services.ExecutionService
    Auth             Authenticator
    Addr             string
}

func NewServer(opts ServerOptions) *Server {
    return &Server{
        tasks:      opts.TaskService,
        executions: opts.ExecutionService,
        auth:       opts.Auth,
        addr:       opts.Addr,
    }
}

func (s *Server) Handler() http.Handler {
    mux := http.NewServeMux()

    // Task endpoints (for workers)
    mux.HandleFunc("POST /tasks/claim", s.authMiddleware(s.handleClaimTask))
    mux.HandleFunc("POST /tasks/{id}/complete", s.authMiddleware(s.handleCompleteTask))
    mux.HandleFunc("POST /tasks/{id}/heartbeat", s.authMiddleware(s.handleHeartbeatTask))
    mux.HandleFunc("POST /tasks/{id}/release", s.authMiddleware(s.handleReleaseTask))

    // Execution endpoints (for clients submitting workflows)
    mux.HandleFunc("POST /executions", s.authMiddleware(s.handleSubmitExecution))
    mux.HandleFunc("GET /executions/{id}", s.authMiddleware(s.handleGetExecution))
    mux.HandleFunc("GET /executions", s.authMiddleware(s.handleListExecutions))
    mux.HandleFunc("POST /executions/{id}/cancel", s.authMiddleware(s.handleCancelExecution))

    // Health
    mux.HandleFunc("GET /health", s.handleHealth)

    return mux
}

func (s *Server) Start(ctx context.Context) error {
    server := &http.Server{Addr: s.addr, Handler: s.Handler()}
    return server.ListenAndServe()
}
```

### HTTP Handlers (Thin Wrappers)

Handlers are thin wrappers that delegate to services:

```go
// internal/http/handlers.go
package http

import (
    "encoding/json"
    "net/http"
    "strings"
    "time"

    "github.com/deepnoodle-ai/workflow"
)

func (s *Server) handleClaimTask(w http.ResponseWriter, r *http.Request) {
    workerID := r.Header.Get("X-Worker-ID")
    if workerID == "" {
        http.Error(w, "X-Worker-ID required", http.StatusBadRequest)
        return
    }

    task, err := s.tasks.Claim(r.Context(), workerID)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    if task == nil {
        w.WriteHeader(http.StatusNoContent)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(task)
}

func (s *Server) handleCompleteTask(w http.ResponseWriter, r *http.Request) {
    taskID := r.PathValue("id")
    workerID := r.Header.Get("X-Worker-ID")

    var result workflow.TaskOutput
    if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    if err := s.tasks.Complete(r.Context(), taskID, workerID, &result); err != nil {
        if strings.Contains(err.Error(), "not owned") {
            http.Error(w, err.Error(), http.StatusConflict)
            return
        }
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHeartbeatTask(w http.ResponseWriter, r *http.Request) {
    taskID := r.PathValue("id")
    workerID := r.Header.Get("X-Worker-ID")

    if err := s.tasks.Heartbeat(r.Context(), taskID, workerID); err != nil {
        if strings.Contains(err.Error(), "not owned") {
            http.Error(w, err.Error(), http.StatusConflict)
            return
        }
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusOK)
}

func (s *Server) handleReleaseTask(w http.ResponseWriter, r *http.Request) {
    taskID := r.PathValue("id")
    workerID := r.Header.Get("X-Worker-ID")

    var req struct {
        RetryAfter time.Duration `json:"retry_after"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    if err := s.tasks.Release(r.Context(), taskID, workerID, req.RetryAfter); err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.WriteHeader(http.StatusOK)
}
```

---

## HTTP Client (TaskRepository Implementation)

The HTTP client in `internal/http/` implements `TaskRepository` for workers:

```go
// internal/http/client.go
package http

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "time"

    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/internal/repository"
)

// TaskClient implements TaskRepository over HTTP.
type TaskClient struct {
    baseURL    string
    workerID   string
    httpClient *http.Client
    token      string
}

type TaskClientOptions struct {
    BaseURL  string
    WorkerID string
    Token    string
    Timeout  time.Duration
}

func NewTaskClient(opts TaskClientOptions) *TaskClient {
    timeout := opts.Timeout
    if timeout == 0 {
        timeout = 30 * time.Second
    }
    return &TaskClient{
        baseURL:    opts.BaseURL,
        workerID:   opts.WorkerID,
        token:      opts.Token,
        httpClient: &http.Client{Timeout: timeout},
    }
}

func (c *TaskClient) Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
    req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/tasks/claim", nil)
    if err != nil {
        return nil, err
    }
    c.setHeaders(req, workerID)

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusNoContent {
        return nil, nil // No tasks available
    }
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("claim failed: %d", resp.StatusCode)
    }

    var task workflow.ClaimedTask
    if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
        return nil, err
    }
    return &task, nil
}

func (c *TaskClient) Complete(ctx context.Context, taskID, workerID string, result *workflow.TaskOutput) error {
    body, err := json.Marshal(result)
    if err != nil {
        return err
    }

    req, err := http.NewRequestWithContext(ctx, "POST",
        c.baseURL+"/tasks/"+taskID+"/complete", bytes.NewReader(body))
    if err != nil {
        return err
    }
    c.setHeaders(req, workerID)
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusConflict {
        return errors.New("task not owned by this worker")
    }
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("complete failed: %d", resp.StatusCode)
    }
    return nil
}

func (c *TaskClient) Heartbeat(ctx context.Context, taskID, workerID string) error {
    req, err := http.NewRequestWithContext(ctx, "POST",
        c.baseURL+"/tasks/"+taskID+"/heartbeat", nil)
    if err != nil {
        return err
    }
    c.setHeaders(req, workerID)

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode == http.StatusConflict {
        return errors.New("task not owned by this worker")
    }
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("heartbeat failed: %d", resp.StatusCode)
    }
    return nil
}

func (c *TaskClient) Release(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error {
    body, _ := json.Marshal(map[string]any{"retry_after": retryAfter})
    req, err := http.NewRequestWithContext(ctx, "POST",
        c.baseURL+"/tasks/"+taskID+"/release", bytes.NewReader(body))
    if err != nil {
        return err
    }
    c.setHeaders(req, workerID)
    req.Header.Set("Content-Type", "application/json")

    resp, err := c.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("release failed: %d", resp.StatusCode)
    }
    return nil
}

// Methods not needed by workers - return errors
func (c *TaskClient) Create(ctx context.Context, task *workflow.TaskRecord) error {
    return errors.New("TaskClient: Create not supported (server only)")
}

func (c *TaskClient) Get(ctx context.Context, id string) (*workflow.TaskRecord, error) {
    return nil, errors.New("TaskClient: Get not supported (server only)")
}

func (c *TaskClient) ListStale(ctx context.Context, cutoff time.Time) ([]*workflow.TaskRecord, error) {
    return nil, errors.New("TaskClient: ListStale not supported (server only)")
}

func (c *TaskClient) Reset(ctx context.Context, taskID string) error {
    return errors.New("TaskClient: Reset not supported (server only)")
}

func (c *TaskClient) setHeaders(req *http.Request, workerID string) {
    req.Header.Set("Authorization", "Bearer "+c.token)
    req.Header.Set("X-Worker-ID", workerID)
}

// Verify interface compliance
var _ repository.TaskRepository = (*TaskClient)(nil)
```

---

## Worker Changes

The worker code barely changes - it just uses `TaskClient` instead of a direct repository:

```go
// cmd/worker/main.go

import (
    internalhttp "github.com/deepnoodle-ai/workflow/internal/http"
)

func main() {
    // Before (direct DB):
    // db, _ := sql.Open("postgres", connStr)
    // taskRepo := postgres.NewTaskRepository(db)

    // After (HTTP):
    taskRepo := internalhttp.NewTaskClient(internalhttp.TaskClientOptions{
        BaseURL:  os.Getenv("SERVER_URL"),
        WorkerID: workerID,
        Token:    os.Getenv("WORKER_TOKEN"),
    })

    // Worker loop is identical - it just calls taskRepo.Claim(), etc.
    for {
        task, err := taskRepo.Claim(ctx, workerID)
        if err != nil {
            log.Warn("claim error", "error", err)
            time.Sleep(pollInterval)
            continue
        }
        if task == nil {
            time.Sleep(pollInterval)
            continue
        }

        // Execute and complete (unchanged)
        result := executeTask(ctx, task)
        taskRepo.Complete(ctx, task.ID, workerID, result)
    }
}
```

---

## File Structure (Aligned with Architecture Plan)

```
workflow/
├── workflow.go              # Domain: Workflow, Step, Edge
├── task.go                  # Domain: TaskRecord, TaskInput, TaskOutput, ClaimedTask
├── records.go               # Domain: ExecutionRecord
├── errors.go                # Domain errors
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
│   │   ├── server.go  # ServerService
│   │   └── reaper.go        # ReaperService
│   │
│   ├── postgres/
│   │   ├── execution.go     # PostgreSQL ExecutionRepository
│   │   ├── task.go          # PostgreSQL TaskRepository
│   │   └── event.go         # PostgreSQL EventRepository
│   │
│   ├── memory/
│   │   ├── execution.go     # In-memory ExecutionRepository
│   │   ├── task.go          # In-memory TaskRepository
│   │   └── event.go         # In-memory EventRepository
│   │
│   └── http/
│       ├── server.go        # HTTP server wrapping services
│       ├── handlers.go      # HTTP handlers (thin wrappers)
│       ├── client.go        # TaskClient (TaskRepository over HTTP)
│       └── auth.go          # Authentication middleware
│
├── cmd/
│   ├── server/
│   │   └── main.go          # Server binary (postgres + http server)
│   └── worker/
│       └── main.go          # Worker binary (http client)
```

---

## HTTP API

All endpoints delegate to services, which use repositories:

| Endpoint | Method | Service Method | Repository Method |
|----------|--------|----------------|-------------------|
| `/tasks/claim` | POST | `TaskService.Claim` | `TaskRepository.Claim` |
| `/tasks/{id}/complete` | POST | `TaskService.Complete` | `TaskRepository.Complete` |
| `/tasks/{id}/heartbeat` | POST | `TaskService.Heartbeat` | `TaskRepository.Heartbeat` |
| `/tasks/{id}/release` | POST | `TaskService.Release` | `TaskRepository.Release` |
| `/executions` | POST | `ExecutionService.Submit` | `ExecutionRepository.Create` |
| `/executions/{id}` | GET | `ExecutionService.Get` | `ExecutionRepository.Get` |
| `/executions` | GET | `ExecutionService.List` | `ExecutionRepository.List` |
| `/executions/{id}/cancel` | POST | `ExecutionService.Cancel` | `ExecutionRepository.Update` |

---

## Implementation Phases

### Phase 1: Repository Interfaces
- Create `internal/repository/task.go` with `TaskRepository` interface
- Create `internal/repository/execution.go` with `ExecutionRepository` interface
- Create `internal/repository/event.go` with `EventRepository` interface

### Phase 2: PostgreSQL Implementations
- Move PostgresStore logic to `internal/postgres/task.go`
- Split execution logic to `internal/postgres/execution.go`
- Move event log to `internal/postgres/event.go`

### Phase 3: Service Layer
- Create `internal/services/task.go` with `TaskService`
- Create `internal/services/execution.go` with `ExecutionService`
- Services depend on repository interfaces

### Phase 4: HTTP Transport
- Create `internal/http/server.go` wrapping services
- Create `internal/http/handlers.go` with thin handlers
- Create `internal/http/client.go` implementing `TaskRepository`
- Create `internal/http/auth.go` for authentication

### Phase 5: Wire Up Binaries
- Update `cmd/server/main.go` to use postgres repos + http server
- Update `cmd/worker/main.go` to use http client

### Phase 6: WebSocket (Optional)
- Add WebSocket support for real-time heartbeats
- WebSocket can coexist with HTTP endpoints

---

## Testing

Since `TaskClient` implements `TaskRepository`, we can:

1. **Test HTTP layer in isolation** using httptest with mock services
2. **Test services** with mock repositories
3. **Reuse repository tests** - same interface, different implementations

```go
func TestTaskClient(t *testing.T) {
    // Create mock service
    mockService := &mockTaskService{}

    // Create server wrapping it
    server := http.NewServer(http.ServerOptions{
        TaskService: mockService,
    })
    ts := httptest.NewServer(server.Handler())
    defer ts.Close()

    // Create client pointing at test server
    client := http.NewTaskClient(http.TaskClientOptions{
        BaseURL:  ts.URL,
        WorkerID: "test-worker",
    })

    // Now test the client
    task, err := client.Claim(ctx, "test-worker")
    // ...
}
```

---

## Dependency Flow

```
workflow/ (domain types)
    ↑
    │ imports
    │
internal/repository/ (interfaces)
    ↑
    │ imports & implements
    │
internal/services/ ←──────────────── internal/postgres/
    ↑                                internal/memory/
    │ imports
    │
internal/http/server ────────────────────────────────────┐
    ↑                                                    │
    │                                                    ▼
cmd/server/                              internal/http/client
                                                        ↑
                                                        │
                                                cmd/worker/
```

**Key rules:**
1. `workflow/` imports nothing from `internal/`
2. `internal/repository/` only imports `workflow/`
3. `internal/services/` imports `workflow/` and `internal/repository/`
4. `internal/postgres/` and `internal/memory/` implement repository interfaces
5. `internal/http/server` imports `internal/services/`
6. `internal/http/client` implements `internal/repository/` interfaces
7. `cmd/` packages wire everything together

---

## Benefits of This Approach

1. **Transport-agnostic core** - Services and repositories don't know about HTTP
2. **Minimal worker changes** - Just swap repository implementation
3. **Testable** - Mock at any layer (repository, service, HTTP)
4. **Consistent interfaces** - Same `TaskRepository` interface everywhere
5. **Clear separation** - HTTP server is pure transport, no business logic
6. **Composable** - Can wrap repositories with caching, metrics, etc.

---

## Open Questions

1. **Worker-only interface?** - Should we formalize a narrower `WorkerTaskRepository` interface?
2. **Streaming results?** - For long-running tasks, stream stdout via WebSocket?
3. **Batch operations?** - Claim multiple tasks in one request?
4. **gRPC alternative?** - Would gRPC be better for this use case?
5. **Authentication** - Token-based? mTLS? API keys?
