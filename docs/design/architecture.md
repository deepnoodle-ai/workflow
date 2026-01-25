# Workflow Engine Architecture

## Overview

The workflow engine is a Go library for building durable, recoverable workflows with support for distributed execution. It consists of a core workflow library and an optional engine layer that adds durability, bounded concurrency, crash recovery, and distributed task execution.

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        CLIENT APPLICATION                                    │
│                                                                              │
│   workflow.New()       workflow.NewEngine()      workflow.Submit()          │
│   Define workflows     Create engine             Submit executions          │
└────────────┬───────────────────┬──────────────────────┬─────────────────────┘
             │                   │                      │
             ▼                   ▼                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         PUBLIC API LAYER                                     │
│                   (workflow package - thin facades)                          │
│                                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────┐  ┌────────────────┐ │
│  │ Workflow     │  │ Engine       │  │ ExecutionStore │  │ Type Aliases   │ │
│  │ Step         │  │ (facade)     │  │ (interface)    │  │ Runner, Task   │ │
│  └──────────────┘  └──────┬───────┘  └───────┬───────┘  └────────────────┘ │
└───────────────────────────┼──────────────────┼──────────────────────────────┘
                            │                  │
                            ▼                  ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                       INTERNAL IMPLEMENTATION                                │
│                                                                              │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        internal/engine                               │   │
│  │                                                                      │   │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │   │
│  │  │ Engine           │  │ SubmitRequest    │  │ ExecutionRecord  │  │   │
│  │  │ - Start()        │  │ - Workflow       │  │ - ID, Status     │  │   │
│  │  │ - Submit()       │  │ - Inputs         │  │ - Inputs/Outputs │  │   │
│  │  │ - Get()          │  │ - ExecutionID    │  │ - Timestamps     │  │   │
│  │  │ - Shutdown()     │  └──────────────────┘  └──────────────────┘  │   │
│  │  └──────────────────┘                                               │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
│  ┌──────────────────────┐  ┌──────────────────┐  ┌──────────────────────┐ │
│  │ internal/task        │  │ internal/memory  │  │ internal/postgres    │ │
│  │                      │  │                  │  │                      │ │
│  │ - Record, Spec       │  │ - Store (testing)│  │ - Store (production) │ │
│  │ - Result, Claimed    │  │ - In-memory      │  │ - PostgreSQL-backed  │ │
│  │ - Runner interface   │  │                  │  │                      │ │
│  └──────────────────────┘  └──────────────────┘  └──────────────────────┘ │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │                        internal/services                              │  │
│  │                                                                       │  │
│  │  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐         │  │
│  │  │ TaskService    │  │ ExecutionService│  │ ReaperService  │         │  │
│  │  │ - Claim        │  │ - Create       │  │ - ReapStale    │         │  │
│  │  │ - Complete     │  │ - Update       │  │                │         │  │
│  │  │ - Heartbeat    │  │ - List         │  │                │         │  │
│  │  └────────────────┘  └────────────────┘  └────────────────┘         │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │                        internal/http                                  │  │
│  │                                                                       │  │
│  │  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐         │  │
│  │  │ Server         │  │ Handler        │  │ TaskClient     │         │  │
│  │  │ (server) │  │ (endpoints)    │  │ (workers)      │         │  │
│  │  └────────────────┘  └────────────────┘  └────────────────┘         │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         DATA LAYER                                           │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │                        PostgreSQL                                     │  │
│  │                                                                       │  │
│  │   executions          tasks                events                     │  │
│  │   ┌──────────┐       ┌──────────┐        ┌──────────┐               │  │
│  │   │ id       │       │ id       │        │ id       │               │  │
│  │   │ status   │◀──────│ exec_id  │        │ exec_id  │               │  │
│  │   │ inputs   │       │ step     │        │ type     │               │  │
│  │   │ outputs  │       │ status   │        │ data     │               │  │
│  │   │ ...      │       │ spec     │        │ ...      │               │  │
│  │   └──────────┘       │ worker_id│        └──────────┘               │  │
│  │                      │ heartbeat│                                    │  │
│  │                      └──────────┘                                    │  │
│  └──────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Deployment Patterns

### 1. Local Execution (Single Process)

```
┌─────────────────────────────────────────┐
│              Application                 │
│                                          │
│  ┌──────────────────────────────────┐  │
│  │  workflow.Engine                  │  │
│  │  Mode: EngineModeLocal           │  │
│  │                                   │  │
│  │  - Submits workflows             │  │
│  │  - Claims tasks locally          │  │
│  │  - Executes tasks in-process     │  │
│  │  - Runs reaper for stale tasks   │  │
│  └──────────────────────────────────┘  │
│               │                          │
│               ▼                          │
│  ┌──────────────────────────────────┐  │
│  │  PostgreSQL / Memory Store        │  │
│  └──────────────────────────────────┘  │
└─────────────────────────────────────────┘
```

Use when:
- Development and testing
- Single-machine deployments
- All activities can run in-process

### 2. Distributed Execution (Server + Workers)

```
┌─────────────────────────────────────────────────────────────────────┐
│                                                                      │
│  ┌──────────────────────┐      ┌──────────────────────────────────┐│
│  │      Server    │      │           Workers                ││
│  │                      │      │                                  ││
│  │  ┌────────────────┐  │      │  ┌────────────┐ ┌────────────┐  ││
│  │  │  HTTP Server   │◀─┼──────┼──│  Worker 1  │ │  Worker 2  │  ││
│  │  │                │  │      │  │            │ │            │  ││
│  │  │  /tasks/claim  │  │      │  │  Claims    │ │  Claims    │  ││
│  │  │  /tasks/...    │  │      │  │  Executes  │ │  Executes  │  ││
│  │  │  /executions   │  │      │  │  Completes │ │  Completes │  ││
│  │  └────────────────┘  │      │  └────────────┘ └────────────┘  ││
│  │                      │      │                                  ││
│  │  ┌────────────────┐  │      │  ┌────────────┐                 ││
│  │  │  Reaper Loop   │  │      │  │  Worker N  │                 ││
│  │  │                │  │      │  │            │                 ││
│  │  │  Detects stale │  │      │  │  ...       │                 ││
│  │  │  Resets tasks  │  │      │  └────────────┘                 ││
│  │  └────────────────┘  │      │                                  ││
│  └──────────┬───────────┘      └──────────────────────────────────┘│
│             │                                                       │
│             ▼                                                       │
│  ┌──────────────────────────────────────────────────────────────┐ │
│  │                        PostgreSQL                             │ │
│  └──────────────────────────────────────────────────────────────┘ │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

Use when:
- Production deployments
- Activities need isolation (containers, processes)
- Horizontal scaling of workers
- Heterogeneous workers (some with GPU, some without, etc.)

## Event-Driven Multi-Step Execution

The engine uses an event-driven model where **task completion triggers state transitions**. Unlike traditional workflow engines that maintain goroutines per execution, all state is persisted between tasks.

### How It Works

```
Submit Workflow
      │
      ▼
┌─────────────────────────────────────────────────────────────────────┐
│  Create ExecutionRecord + First Task                                │
│  StateData = { PathStates: {"main": {status: "pending"}} }         │
└─────────────────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────────────────┐
│  Worker Claims Task                                                  │
│  Executes step activity                                             │
│  Calls CompleteTask with result                                     │
└─────────────────────────────────────────────────────────────────────┘
      │
      ▼
┌─────────────────────────────────────────────────────────────────────┐
│  HandleTaskCompletion (event handler)                               │
│                                                                     │
│  1. Load EngineExecutionState from ExecutionRecord.StateData        │
│  2. Store step output in path's StepOutputs                         │
│  3. Check retry config if task failed                               │
│  4. Evaluate edges to find next steps                               │
│  5. Handle branching: create tasks for multiple matching edges      │
│  6. Handle joins: check if all paths arrived, merge state           │
│  7. Create tasks for all next steps                                 │
│  8. Save updated state back to ExecutionRecord                      │
│  9. Mark execution complete if all paths done                       │
└─────────────────────────────────────────────────────────────────────┘
      │
      ▼
   (repeat until all paths complete)
```

### Execution State (Serialized to JSON)

```go
type EngineExecutionState struct {
    PathStates  map[string]*PathState  // Per-path tracking
    JoinStates  map[string]*JoinState  // Join coordination
    PathCounter int                     // For generating path IDs
}

type PathState struct {
    ID          string
    Status      ExecutionStatus  // pending, running, waiting, completed, failed
    CurrentStep string
    StepOutputs map[string]any   // step name -> output
    Variables   map[string]any   // path-scoped variables
}
```

### Local vs Server Mode

```
┌─────────────────────────────────────────────────────────────────────┐
│                         LOCAL MODE                                   │
│                                                                      │
│   Engine.taskProcessLoop():                                         │
│     ┌──────────────────────────────────────────────────────────┐   │
│     │  for {                                                    │   │
│     │      task := store.ClaimTask()    // Poll for tasks      │   │
│     │      result := executeTask(task)  // Run activity        │   │
│     │      store.CompleteTask(result)   // Update task status  │   │
│     │      HandleTaskCompletion(task, result)  // Advance      │   │
│     │  }                                                        │   │
│     └──────────────────────────────────────────────────────────┘   │
│                                                                      │
│   Everything happens in one process.                                │
└─────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────┐
│                      ORCHESTRATOR MODE                               │
│                                                                      │
│   Server (HTTP Server):                                       │
│     ┌──────────────────────────────────────────────────────────┐   │
│     │  POST /tasks/claim     → taskService.Claim()             │   │
│     │  POST /tasks/{id}/complete → taskService.Complete()      │   │
│     │                             → OnTaskCompleted callback ──┼───┐│
│     └──────────────────────────────────────────────────────────┘   ││
│                                                                      ││
│   Remote Workers:                                                    ││
│     ┌──────────────────────────────────────────────────────────┐   ││
│     │  for {                                                    │   ││
│     │      task := POST /tasks/claim                            │   ││
│     │      result := executeTask(task)                          │   ││
│     │      POST /tasks/{id}/complete with result ───────────────┼───┘│
│     │  }                                                        │    │
│     └──────────────────────────────────────────────────────────┘    │
│                                                                      │
│   Callback wires to Engine.HandleTaskCompletion() to advance        │
│   workflow state when remote workers complete tasks.                │
└─────────────────────────────────────────────────────────────────────┘
```

### Wiring the Callback (Server Setup)

```go
// Create engine with workflow definitions
eng, _ := engine.New(engine.Options{
    Store:     store,
    Workflows: workflows,  // map[string]WorkflowDefinition
    WorkerID:  "server",
    Mode:      engine.ModeServer,
})

// Create HTTP server with callback to advance workflows
server := http.NewServer(http.ServerOptions{
    TaskService:      taskService,
    ExecutionService: executionService,
    OnTaskCompleted:  eng.HandleTaskCompletion,  // Wire it up
})
```

## Task Lifecycle

```
                    ┌─────────┐
                    │ pending │
                    └────┬────┘
                         │
        ┌────────────────┼────────────────┐
        │ Worker claims  │                │
        ▼                │                │
   ┌─────────┐           │                │
   │ running │───────────┤                │
   └────┬────┘           │                │
        │                │                │
   ┌────┴────┐      ┌────┴────┐     ┌────┴────┐
   │         │      │         │     │         │
   ▼         ▼      ▼         ▼     ▼         │
┌────────┐ ┌────────┐ ┌────────┐           │
│completed│ │ failed │ │released│───────────┘
└────────┘ └────────┘ └────────┘  (retry)
```

### State Transitions

1. **pending → running**: Worker calls `ClaimTask`, atomically assigns task
2. **running → completed**: Worker calls `CompleteTask` with success result
3. **running → failed**: Worker calls `CompleteTask` with failure result
4. **running → pending**: Worker calls `ReleaseTask` for retry, or reaper resets stale task
5. **failed → pending**: Automatic retry based on retry policy

## Heartbeat and Recovery

```
Timeline:
────────────────────────────────────────────────────────────────────►
   │         │         │         │         │         │
   ▼         ▼         ▼         ▼         ▼         ▼
┌──────┐ ┌──────┐ ┌──────┐ ┌──────┐                ┌──────┐
│Claim │ │ HB   │ │ HB   │ │ HB   │    Worker     │Reset │
│      │ │      │ │      │ │      │    Crash!     │ Task │
└──────┘ └──────┘ └──────┘ └──────┘                └──────┘
   │         │         │         │         │         │
   ├─────────┼─────────┼─────────┼─────────┼─────────┤
   │    30s       30s       30s       30s      30s   │
   │         │         │         │    Timeout!       │
   │         │         │         │         │         │
   └─────────┴─────────┴─────────┴─────────┴─────────┘
                                    │
                                    ▼
                            Task available for
                            another worker
```

- Workers send heartbeats every `HeartbeatInterval` (default 30s)
- Reaper checks for stale tasks every `ReaperInterval` (default 30s)
- Tasks with heartbeat older than `HeartbeatTimeout` (default 2m) are reset
- Reset increments attempt counter for retry tracking

## Package Dependencies

```
                    ┌────────────────┐
                    │    workflow    │  Public API
                    │   (facade)     │
                    └───────┬────────┘
                            │ delegates to
                            ▼
          ┌─────────────────────────────────────┐
          │           internal/                  │
          │                                      │
          │  ┌──────────┐        ┌──────────┐  │
          │  │  engine  │◀───────│ services │  │
          │  └─────┬────┘        └────┬─────┘  │
          │        │                  │         │
          │        ▼                  ▼         │
          │  ┌──────────┐        ┌──────────┐  │
          │  │   task   │        │   http   │  │
          │  └─────┬────┘        └────┬─────┘  │
          │        │                  │         │
          │        ▼                  │         │
          │  ┌──────────┐◀───────────┘         │
          │  │  memory  │                       │
          │  │ postgres │                       │
          │  └──────────┘                       │
          └─────────────────────────────────────┘
```

Note: The `workflow` package is a thin facade that re-exports types from
internal packages and delegates to `internal/engine.Engine`. This enables
clean package boundaries while maintaining a simple public API.
