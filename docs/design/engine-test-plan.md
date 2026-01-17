# Workflow Engine Test Plan

This document outlines the testing strategy and instrumentation approach for the workflow engine implementation described in `engine-design.md`.

## Testing Goals

1. **Correctness**: Verify the engine behaves according to the design specification
2. **Reliability**: Confirm the system recovers gracefully from failures
3. **Concurrency safety**: Validate fencing, locking, and race condition handling
4. **Observability**: Ensure sufficient visibility for debugging and monitoring

---

## Unit Tests

Unit tests verify individual components in isolation using mocks or in-memory implementations.

### Engine Core (`engine_test.go`)

| Test Case | Description |
|-----------|-------------|
| `TestEngine_Submit_PersistsBeforeReturn` | Verify Submit() persists record to store before returning handle |
| `TestEngine_Submit_EnqueueFailure_RecordRemainsPending` | If enqueue fails after create, record stays pending for recovery |
| `TestEngine_Submit_CustomExecutionID` | Custom execution ID is used when provided |
| `TestEngine_Submit_GeneratesID` | Generates unique ID when not provided |
| `TestEngine_Get_ReturnsRecord` | Get() returns correct record by ID |
| `TestEngine_Get_NotFound` | Get() returns appropriate error for missing ID |
| `TestEngine_List_FiltersByStatus` | List() correctly filters by status |
| `TestEngine_List_FiltersByWorkflowName` | List() correctly filters by workflow name |
| `TestEngine_List_Pagination` | Limit and offset work correctly |
| `TestEngine_Cancel_SetsStatus` | Cancel() transitions running execution to cancelled |
| `TestEngine_Cancel_AlreadyCompleted` | Cancel() on completed execution returns error or no-op |

### Concurrency Control (`engine_concurrency_test.go`)

| Test Case | Description |
|-----------|-------------|
| `TestEngine_MaxConcurrent_Respected` | Never exceeds MaxConcurrent active executions |
| `TestEngine_MaxConcurrent_Zero_Unlimited` | MaxConcurrent=0 allows unlimited concurrency |
| `TestEngine_Semaphore_AcquiredBeforeDequeue` | Semaphore acquired before dequeue (prevents lease expiry) |
| `TestEngine_Semaphore_ReleasedOnCompletion` | Semaphore slot released after execution completes |
| `TestEngine_Semaphore_ReleasedOnError` | Semaphore slot released even on processing error |

### Recovery Logic (`engine_recovery_test.go`)

| Test Case | Description |
|-----------|-------------|
| `TestEngine_Recovery_PendingReenqueued` | Orphaned pending records are re-enqueued on startup |
| `TestEngine_Recovery_RunningReenqueued` | Orphaned running records are re-enqueued (resume mode) |
| `TestEngine_Recovery_RunningMarkedFailed` | Orphaned running records marked failed (fail mode) |
| `TestEngine_Recovery_AttemptIncremented` | Attempt counter incremented on recovery |
| `TestEngine_Recovery_WorkerIDCleared` | Worker ID cleared on recovery |
| `TestEngine_Reaper_StaleRunning` | Reaper detects executions with old heartbeat |
| `TestEngine_Reaper_StalePending` | Reaper detects dispatched-but-not-claimed executions |
| `TestEngine_Reaper_IncrementAttemptOnReap` | Reaper increments attempt before re-enqueue |

### Shutdown (`engine_shutdown_test.go`)

| Test Case | Description |
|-----------|-------------|
| `TestEngine_Shutdown_WaitsForActive` | Shutdown blocks until active executions complete |
| `TestEngine_Shutdown_Timeout` | Shutdown returns error after timeout with active work |
| `TestEngine_Shutdown_StopsProcessingLoop` | No new work dequeued after shutdown initiated |
| `TestEngine_Shutdown_ClosesQueue` | Queue is closed after shutdown completes |

### ExecutionStore (`store_test.go`)

Tests run against both MemoryStore and PostgresStore implementations.

| Test Case | Description |
|-----------|-------------|
| `TestStore_Create_Success` | Creates record with correct fields |
| `TestStore_Create_DuplicateID` | Returns error on duplicate ID |
| `TestStore_Get_Exists` | Returns record by ID |
| `TestStore_Get_NotFound` | Returns appropriate error for missing ID |
| `TestStore_ClaimExecution_Success` | Claims pending execution, sets running status |
| `TestStore_ClaimExecution_NotPending` | Returns false if status is not pending |
| `TestStore_ClaimExecution_WrongAttempt` | Returns false if attempt doesn't match (fencing) |
| `TestStore_ClaimExecution_SetsWorkerID` | Sets worker_id on successful claim |
| `TestStore_ClaimExecution_SetsHeartbeat` | Sets last_heartbeat on successful claim |
| `TestStore_CompleteExecution_Success` | Completes running execution with outputs |
| `TestStore_CompleteExecution_NotRunning` | Returns false if status is not running |
| `TestStore_CompleteExecution_WrongAttempt` | Returns false if attempt doesn't match (fencing) |
| `TestStore_CompleteExecution_SetsCompletedAt` | Sets completed_at timestamp |
| `TestStore_Heartbeat_Updates` | Updates last_heartbeat timestamp |
| `TestStore_ListStaleRunning` | Returns executions with heartbeat older than cutoff |
| `TestStore_ListStalePending` | Returns dispatched executions older than cutoff |

### WorkQueue (`queue_test.go`)

Tests run against both MemoryQueue and PostgresQueue implementations.

| Test Case | Description |
|-----------|-------------|
| `TestQueue_Enqueue_Success` | Item added to queue |
| `TestQueue_Enqueue_Duplicate` | Handles duplicate execution ID appropriately |
| `TestQueue_Dequeue_ReturnsItem` | Returns next available item |
| `TestQueue_Dequeue_Blocks` | Blocks when queue empty until item available |
| `TestQueue_Dequeue_RespectsContext` | Returns when context cancelled |
| `TestQueue_Dequeue_SetsLease` | Returns lease with token and expiry |
| `TestQueue_Ack_RemovesItem` | Acknowledged item removed from queue |
| `TestQueue_Ack_WrongWorker` | Ack fails if worker doesn't own lease |
| `TestQueue_Nack_ReturnsItem` | Nack'd item returns to queue after delay |
| `TestQueue_Nack_RespectsDelay` | Item not visible until delay passes |
| `TestQueue_Extend_ExtendsLease` | Extend updates locked_until |
| `TestQueue_LeaseExpiry_ItemReturns` | Expired lease makes item available again |
| `TestQueue_ReapStaleLeases` | Stale processing items returned to pending |

### ExecutionEnvironment (`environment_test.go`)

| Test Case | Description |
|-----------|-------------|
| `TestLocalEnvironment_Mode` | Returns EnvironmentModeBlocking |
| `TestLocalEnvironment_Run_Success` | Runs execution to completion |
| `TestLocalEnvironment_Run_Error` | Returns error from failed execution |
| `TestLocalEnvironment_Run_ContextCancelled` | Respects context cancellation |

---

## Integration Tests

Integration tests verify multiple components working together with real (or realistic) dependencies.

### Full Lifecycle (`integration/lifecycle_test.go`)

```go
// TestIntegration_SubmitToCompletion verifies the complete happy path:
// Submit → Enqueue → Dequeue → Claim → Run → Complete → Ack
func TestIntegration_SubmitToCompletion(t *testing.T)

// TestIntegration_SubmitMultiple_ConcurrencyLimit verifies that
// MaxConcurrent is enforced across multiple submissions
func TestIntegration_SubmitMultiple_ConcurrencyLimit(t *testing.T)

// TestIntegration_WorkflowWithCheckpoints verifies checkpoint/resume
// works correctly through the engine layer
func TestIntegration_WorkflowWithCheckpoints(t *testing.T)

// TestIntegration_WorkflowFailure_MarkedFailed verifies failed
// workflows result in EngineStatusFailed
func TestIntegration_WorkflowFailure_MarkedFailed(t *testing.T)
```

### Fencing Scenarios (`integration/fencing_test.go`)

```go
// TestIntegration_StaleWorkerCannotComplete verifies that a worker
// with an old attempt number cannot overwrite a newer attempt's results
func TestIntegration_StaleWorkerCannotComplete(t *testing.T) {
    // 1. Submit execution (attempt=1)
    // 2. Worker A claims (attempt=1)
    // 3. Simulate timeout, reaper increments to attempt=2
    // 4. Worker B claims (attempt=2)
    // 5. Worker A tries to complete with attempt=1 → should fail
    // 6. Worker B completes with attempt=2 → should succeed
}

// TestIntegration_DoubleClaimPrevented verifies only one worker
// can claim a pending execution
func TestIntegration_DoubleClaimPrevented(t *testing.T) {
    // 1. Submit execution
    // 2. Worker A and B both try to claim concurrently
    // 3. Exactly one succeeds, the other gets false
}

// TestIntegration_RecoveryFencing verifies recovered executions
// have incremented attempt that fences out old workers
func TestIntegration_RecoveryFencing(t *testing.T)
```

### Failure Recovery (`integration/recovery_test.go`)

```go
// TestIntegration_EngineRestart_RecoversPending verifies pending
// executions are recovered on engine restart
func TestIntegration_EngineRestart_RecoversPending(t *testing.T) {
    // 1. Submit execution
    // 2. Stop engine before processing
    // 3. Start new engine
    // 4. Verify execution completes
}

// TestIntegration_HeartbeatTimeout_Requeued verifies executions
// with stale heartbeats are re-enqueued
func TestIntegration_HeartbeatTimeout_Requeued(t *testing.T) {
    // 1. Submit and start execution
    // 2. Stop heartbeat (simulate worker hang)
    // 3. Wait for reaper cycle
    // 4. Verify execution re-enqueued with incremented attempt
}

// TestIntegration_DispatchTimeout_Requeued verifies dispatched
// but unclaimed executions are re-enqueued
func TestIntegration_DispatchTimeout_Requeued(t *testing.T)
```

### Database Behavior (`integration/postgres_test.go`)

```go
// TestPostgres_ConcurrentClaims verifies FOR UPDATE SKIP LOCKED
// works correctly under concurrent access
func TestPostgres_ConcurrentClaims(t *testing.T)

// TestPostgres_TransactionIsolation verifies updates don't
// interfere with concurrent reads
func TestPostgres_TransactionIsolation(t *testing.T)

// TestPostgres_ConnectionPoolExhaustion verifies graceful handling
// when connection pool is exhausted
func TestPostgres_ConnectionPoolExhaustion(t *testing.T)

// TestPostgres_TransientFailure_Retry verifies Nack and retry
// on transient database errors
func TestPostgres_TransientFailure_Retry(t *testing.T)
```

### Multi-Engine (`integration/multi_engine_test.go`)

```go
// TestMultiEngine_WorkDistribution verifies work is distributed
// across multiple engines sharing the same queue
func TestMultiEngine_WorkDistribution(t *testing.T) {
    // 1. Start 3 engines with same store/queue
    // 2. Submit 30 executions
    // 3. Verify each engine processes roughly 10
}

// TestMultiEngine_EngineFailure_OthersTakeOver verifies remaining
// engines take over work when one fails
func TestMultiEngine_EngineFailure_OthersTakeOver(t *testing.T)

// TestMultiEngine_NoDuplicateProcessing verifies same execution
// is never processed by multiple engines simultaneously
func TestMultiEngine_NoDuplicateProcessing(t *testing.T)
```

---

## End-to-End Tests

End-to-end tests verify the system in realistic deployment scenarios.

### Scenario Tests (`e2e/scenarios_test.go`)

```go
// TestE2E_RealWorkflow_DataPipeline runs a realistic multi-step
// data processing workflow through the engine
func TestE2E_RealWorkflow_DataPipeline(t *testing.T)

// TestE2E_RealWorkflow_WithBranching tests workflow with
// conditional paths and parallel branches
func TestE2E_RealWorkflow_WithBranching(t *testing.T)

// TestE2E_LongRunning_HeartbeatMaintained verifies heartbeats
// keep execution alive for long-running workflows
func TestE2E_LongRunning_HeartbeatMaintained(t *testing.T)
```

### Chaos Tests (`e2e/chaos_test.go`)

These tests require special infrastructure (Docker, process management).

```go
// TestChaos_EngineKill verifies recovery after SIGKILL
func TestChaos_EngineKill(t *testing.T) {
    // 1. Start engine in subprocess
    // 2. Submit long-running workflow
    // 3. SIGKILL engine mid-execution
    // 4. Start new engine
    // 5. Verify workflow completes
}

// TestChaos_DatabaseRestart verifies recovery after DB restart
func TestChaos_DatabaseRestart(t *testing.T)

// TestChaos_NetworkPartition simulates network issues between
// engine and database
func TestChaos_NetworkPartition(t *testing.T)

// TestChaos_RandomKills randomly kills processes during
// sustained load to verify overall reliability
func TestChaos_RandomKills(t *testing.T)
```

### Distributed Worker Tests (`e2e/worker_test.go`)

For SpritesEnvironment or similar dispatch modes.

```go
// TestWorker_ClaimAndComplete verifies worker binary
// successfully claims, runs, and completes
func TestWorker_ClaimAndComplete(t *testing.T)

// TestWorker_FencedOut verifies worker exits gracefully
// when it cannot claim (attempt mismatch)
func TestWorker_FencedOut(t *testing.T)

// TestWorker_CompletionFencedOut verifies worker handles
// completion rejection gracefully
func TestWorker_CompletionFencedOut(t *testing.T)

// TestWorker_Crash_ReapedAndRetried verifies crashed worker
// is detected and work is retried
func TestWorker_Crash_ReapedAndRetried(t *testing.T)
```

---

## Test Infrastructure

### In-Memory Implementations

Provide in-memory implementations for fast unit testing:

```go
// MemoryStore implements ExecutionStore for testing
type MemoryStore struct {
    mu      sync.RWMutex
    records map[string]*ExecutionRecord
}

// MemoryQueue implements WorkQueue for testing
type MemoryQueue struct {
    mu       sync.Mutex
    items    []*queueItem
    pending  chan struct{}
    leaseTTL time.Duration
}
```

### Test Fixtures

Provide reusable workflow fixtures:

```go
// SimpleWorkflow returns a workflow with a single activity
func SimpleWorkflow() *Workflow

// FailingWorkflow returns a workflow that always fails
func FailingWorkflow(err error) *Workflow

// LongRunningWorkflow returns a workflow that takes the specified duration
func LongRunningWorkflow(duration time.Duration) *Workflow

// CheckpointingWorkflow returns a workflow with multiple checkpoint boundaries
func CheckpointingWorkflow(steps int) *Workflow

// BranchingWorkflow returns a workflow with conditional branches
func BranchingWorkflow() *Workflow
```

### Test Database

For Postgres integration tests:

```go
// TestDB manages a test database instance
type TestDB struct {
    db       *sql.DB
    connStr  string
    cleanup  func()
}

// NewTestDB creates a fresh test database (via testcontainers or embedded)
func NewTestDB(t *testing.T) *TestDB

// WithMigrations applies schema migrations
func (tdb *TestDB) WithMigrations() *TestDB

// Truncate clears all tables between tests
func (tdb *TestDB) Truncate(t *testing.T)
```

### Timing Helpers

For tests that depend on time-based behavior:

```go
// FakeClock allows controlling time in tests
type FakeClock struct {
    now time.Time
}

func (c *FakeClock) Now() time.Time
func (c *FakeClock) Advance(d time.Duration)

// WithFakeClock injects a fake clock into the engine for testing
func WithFakeClock(clock *FakeClock) EngineOption
```

---

## CI/CD Pipeline Integration

### Test Stages

```yaml
# .github/workflows/test.yml
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: Unit tests
        run: go test -v -race ./... -short

  integration:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_PASSWORD: test
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
    steps:
      - name: Integration tests
        run: go test -v -race ./integration/...
        env:
          TEST_POSTGRES_DSN: postgres://postgres:test@localhost/test

  e2e:
    runs-on: ubuntu-latest
    steps:
      - name: E2E tests
        run: go test -v ./e2e/... -timeout 30m
```

### Test Tags

Use build tags to control test scope:

```go
//go:build integration
// +build integration

package integration

//go:build e2e
// +build e2e

package e2e

//go:build chaos
// +build chaos

package e2e
```

### Coverage Requirements

```yaml
- name: Check coverage
  run: |
    go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | grep total | awk '{print $3}'
    # Fail if coverage below threshold
    COVERAGE=$(go tool cover -func=coverage.out | grep total | awk '{print substr($3, 1, length($3)-1)}')
    if (( $(echo "$COVERAGE < 80" | bc -l) )); then
      echo "Coverage $COVERAGE% is below 80% threshold"
      exit 1
    fi
```

---

## Instrumentation

### For Users (Production Visibility)

#### Structured Logging

All engine operations emit structured logs:

```go
type EngineLogger struct {
    logger *slog.Logger
}

// Log levels:
// - Debug: dequeue attempts, heartbeats, lease extensions
// - Info: execution started, completed, recovered
// - Warn: transient failures, retries, fencing events
// - Error: persistent failures requiring investigation

func (e *Engine) processExecution(ctx context.Context, lease Lease) {
    e.logger.Info("processing execution",
        slog.String("execution_id", lease.Item.ExecutionID),
        slog.String("worker_id", e.workerID),
    )
    // ...
    e.logger.Info("execution completed",
        slog.String("execution_id", id),
        slog.String("status", string(status)),
        slog.Duration("duration", duration),
    )
}
```

#### Metrics via Callbacks

Implement metrics adapters using the callback interface:

```go
// PrometheusCallbacks implements EngineCallbacks for Prometheus
type PrometheusCallbacks struct {
    submittedTotal   *prometheus.CounterVec
    startedTotal     *prometheus.CounterVec
    completedTotal   *prometheus.CounterVec
    durationHist     *prometheus.HistogramVec
    activeGauge      prometheus.Gauge
    pendingGauge     prometheus.Gauge
}

func (p *PrometheusCallbacks) OnExecutionSubmitted(id, workflowName string) {
    p.submittedTotal.WithLabelValues(workflowName).Inc()
    p.pendingGauge.Inc()
}

func (p *PrometheusCallbacks) OnExecutionStarted(id string) {
    p.startedTotal.Inc()
    p.pendingGauge.Dec()
    p.activeGauge.Inc()
}

func (p *PrometheusCallbacks) OnExecutionCompleted(id string, duration time.Duration, err error) {
    status := "success"
    if err != nil {
        status = "failure"
    }
    p.completedTotal.WithLabelValues(status).Inc()
    p.durationHist.Observe(duration.Seconds())
    p.activeGauge.Dec()
}
```

#### Recommended Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `workflow_executions_submitted_total` | Counter | workflow_name | Total submissions |
| `workflow_executions_started_total` | Counter | | Total started |
| `workflow_executions_completed_total` | Counter | status | Total completed |
| `workflow_execution_duration_seconds` | Histogram | workflow_name | Execution duration |
| `workflow_executions_active` | Gauge | | Currently running |
| `workflow_executions_pending` | Gauge | | Waiting to start |
| `workflow_reaper_recoveries_total` | Counter | reason | Reaper recovery events |
| `workflow_heartbeat_failures_total` | Counter | | Failed heartbeats |
| `workflow_claim_failures_total` | Counter | reason | Failed claims |
| `workflow_attempts_total` | Counter | | Total attempt increments |

#### Health Endpoints

```go
// EngineHealth provides health check data
type EngineHealth struct {
    Healthy       bool      `json:"healthy"`
    WorkerID      string    `json:"worker_id"`
    ActiveCount   int       `json:"active_count"`
    MaxConcurrent int       `json:"max_concurrent"`
    QueueDepth    int       `json:"queue_depth"`
    LastDequeue   time.Time `json:"last_dequeue"`
    Stopping      bool      `json:"stopping"`
}

func (e *Engine) Health(ctx context.Context) (*EngineHealth, error)
```

### For AI Agents (Development/Testing)

#### Event Tracing

Capture detailed traces of all operations for debugging and AI analysis:

```go
// EventTrace captures engine operations for analysis
type EventTrace struct {
    mu     sync.Mutex
    events []TraceEvent
}

type TraceEvent struct {
    Timestamp   time.Time              `json:"timestamp"`
    Type        string                 `json:"type"`
    ExecutionID string                 `json:"execution_id,omitempty"`
    WorkerID    string                 `json:"worker_id,omitempty"`
    Attempt     int                    `json:"attempt,omitempty"`
    Details     map[string]any         `json:"details,omitempty"`
    Error       string                 `json:"error,omitempty"`
}

// Event types for analysis
const (
    EventSubmitted        = "submitted"
    EventEnqueued         = "enqueued"
    EventDequeued         = "dequeued"
    EventClaimAttempted   = "claim_attempted"
    EventClaimSucceeded   = "claim_succeeded"
    EventClaimFailed      = "claim_failed"
    EventHeartbeat        = "heartbeat"
    EventCompleteAttempted = "complete_attempted"
    EventCompleteSucceeded = "complete_succeeded"
    EventCompleteFailed   = "complete_failed"
    EventReaped           = "reaped"
    EventRecovered        = "recovered"
)

// TracingCallbacks implements EngineCallbacks with full tracing
type TracingCallbacks struct {
    trace *EventTrace
}
```

#### State Inspection

Enable querying internal state for debugging:

```go
// EngineSnapshot captures current engine state
type EngineSnapshot struct {
    Timestamp     time.Time                 `json:"timestamp"`
    WorkerID      string                    `json:"worker_id"`
    Active        []string                  `json:"active_execution_ids"`
    Stopping      bool                      `json:"stopping"`
    QueueSnapshot *QueueSnapshot            `json:"queue,omitempty"`
    StoreSnapshot *StoreSnapshot            `json:"store,omitempty"`
}

type QueueSnapshot struct {
    Pending    int `json:"pending"`
    Processing int `json:"processing"`
    Total      int `json:"total"`
}

type StoreSnapshot struct {
    ByStatus map[EngineExecutionStatus]int `json:"by_status"`
    Total    int                           `json:"total"`
}

// Snapshot returns current engine state (expensive, for debugging)
func (e *Engine) Snapshot(ctx context.Context) (*EngineSnapshot, error)
```

#### Simulation Mode

Enable deterministic testing and failure injection:

```go
// SimulationEngine wraps Engine with failure injection
type SimulationEngine struct {
    *Engine
    clock        *FakeClock
    failureRules []FailureRule
}

type FailureRule struct {
    Operation   string        // "claim", "complete", "heartbeat", "enqueue", "dequeue"
    ExecutionID string        // empty = all
    Probability float64       // 0.0-1.0
    Error       error         // error to return
    Delay       time.Duration // delay before operation
}

// InjectFailure adds a failure rule
func (s *SimulationEngine) InjectFailure(rule FailureRule)

// ClearFailures removes all failure rules
func (s *SimulationEngine) ClearFailures()

// AdvanceTime moves the fake clock forward, triggering time-based behavior
func (s *SimulationEngine) AdvanceTime(d time.Duration)
```

#### Test Output Format

For AI agent consumption, emit machine-readable test output:

```go
// TestResult provides structured test output
type TestResult struct {
    Name        string        `json:"name"`
    Passed      bool          `json:"passed"`
    Duration    time.Duration `json:"duration"`
    Events      []TraceEvent  `json:"events,omitempty"`
    Assertions  []Assertion   `json:"assertions"`
    Error       string        `json:"error,omitempty"`
    Snapshot    *EngineSnapshot `json:"final_state,omitempty"`
}

type Assertion struct {
    Description string `json:"description"`
    Passed      bool   `json:"passed"`
    Expected    any    `json:"expected,omitempty"`
    Actual      any    `json:"actual,omitempty"`
}
```

#### Scenario Replay

Record and replay execution sequences:

```go
// Scenario represents a reproducible test scenario
type Scenario struct {
    Name        string            `json:"name"`
    Description string            `json:"description"`
    Submissions []SubmitRequest   `json:"submissions"`
    Failures    []FailureRule     `json:"failures"`
    TimeSteps   []TimeStep        `json:"time_steps"`
    Assertions  []ScenarioAssert  `json:"assertions"`
}

type TimeStep struct {
    Advance      time.Duration `json:"advance"`
    Description  string        `json:"description"`
}

type ScenarioAssert struct {
    After       string                     `json:"after"` // time step name
    ExecutionID string                     `json:"execution_id"`
    Status      EngineExecutionStatus      `json:"status"`
    Attempt     int                        `json:"attempt,omitempty"`
}

// RunScenario executes a scenario and returns results
func RunScenario(ctx context.Context, scenario *Scenario) (*ScenarioResult, error)
```

---

## Test Data Management

### Fixtures Directory Structure

```
testdata/
├── workflows/
│   ├── simple.json
│   ├── branching.json
│   └── long_running.json
├── scenarios/
│   ├── happy_path.json
│   ├── worker_crash.json
│   ├── double_claim.json
│   └── network_partition.json
└── golden/
    ├── trace_happy_path.json
    └── trace_recovery.json
```

### Golden File Testing

For complex behaviors, use golden file comparison:

```go
func TestEngine_Trace_HappyPath(t *testing.T) {
    trace := runScenario(t, "happy_path")

    golden := filepath.Join("testdata", "golden", "trace_happy_path.json")
    if *update {
        writeGolden(t, golden, trace)
        return
    }

    expected := readGolden(t, golden)
    assertTracesEqual(t, expected, trace)
}
```

---

## Performance Testing

### Benchmarks

```go
func BenchmarkEngine_Submit(b *testing.B) {
    engine := setupBenchmarkEngine(b)
    defer engine.Shutdown(context.Background())

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        engine.Submit(context.Background(), SubmitRequest{
            Workflow: simpleWorkflow,
            Inputs:   map[string]any{"n": i},
        })
    }
}

func BenchmarkEngine_Throughput(b *testing.B) {
    // Measure executions/second under load
}

func BenchmarkPostgresStore_ClaimExecution(b *testing.B) {
    // Measure claim performance under contention
}
```

### Load Testing

```go
// LoadTest runs sustained load and reports metrics
func LoadTest(ctx context.Context, cfg LoadTestConfig) (*LoadTestResult, error)

type LoadTestConfig struct {
    Duration        time.Duration
    SubmitRate      float64 // submissions per second
    MaxConcurrent   int
    WorkflowFactory func() *Workflow
}

type LoadTestResult struct {
    TotalSubmitted  int
    TotalCompleted  int
    TotalFailed     int
    AvgLatency      time.Duration
    P99Latency      time.Duration
    Throughput      float64 // completions per second
}
```

---

## Appendix: Temporal Techniques Analysis

The Temporal workflow system uses event-sourced history and deterministic replay to provide strong guarantees. This section analyzes which techniques could enhance our checkpoint-based design without adopting full event-sourcing (which is explicitly a non-goal).

### Temporal's Core Techniques

| Technique | Description | Temporal Benefit |
|-----------|-------------|------------------|
| Event History | Immutable log of all decisions/events | Perfect replay, audit trail |
| Deterministic Replay | Rebuild state by replaying events | No re-execution on recovery |
| Timer Virtualization | Delays are events, not real waits | Fast replay, testable delays |
| SDK Constraints | Forbid non-deterministic operations | Guaranteed consistency |

### Analysis: What to Adopt

#### 1. Event Log for Observability (Recommended: Yes)

**What**: Add optional event logging alongside checkpoints—not as the recovery mechanism, but for observability and debugging.

**Why**: Provides audit trail without changing recovery model.

```go
// EventLog captures workflow events for observability (not recovery)
type EventLog interface {
    Append(ctx context.Context, event Event) error
    List(ctx context.Context, executionID string) ([]Event, error)
}

type Event struct {
    ID          string         `json:"id"`
    ExecutionID string         `json:"execution_id"`
    Timestamp   time.Time      `json:"timestamp"`
    Type        EventType      `json:"type"`
    StepName    string         `json:"step_name,omitempty"`
    PathID      string         `json:"path_id,omitempty"`
    Data        map[string]any `json:"data,omitempty"`
}

type EventType string

const (
    EventWorkflowStarted   EventType = "workflow_started"
    EventStepStarted       EventType = "step_started"
    EventStepCompleted     EventType = "step_completed"
    EventStepFailed        EventType = "step_failed"
    EventCheckpointSaved   EventType = "checkpoint_saved"
    EventTimerStarted      EventType = "timer_started"
    EventTimerFired        EventType = "timer_fired"
    EventWorkflowCompleted EventType = "workflow_completed"
)
```

**Tradeoff**: Additional storage and write overhead, but optional and valuable for debugging.

**Recommendation**: Implement as an optional `ExecutionCallbacks` extension. Users who want audit trails can enable it.

#### 2. Timer Abstraction (Recommended: Yes)

**What**: First-class timer/delay support that survives recovery.

**Current gap**: The design doesn't explicitly address how workflows express delays. If an activity calls `time.Sleep()`, that's lost on crash.

**Approach**: Add timer as a step type that checkpoints the target time.

```go
// Timer creates a delay that survives recovery
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
    // On first execution: compute deadline, checkpoint, then wait
    // On recovery: load deadline from checkpoint, compute remaining wait

    state := ctx.PathState()
    deadline, ok := state["timer_deadline_"+t.name].(time.Time)
    if !ok {
        deadline = time.Now().Add(t.duration)
        state["timer_deadline_"+t.name] = deadline
        // Checkpoint happens after this step
    }

    remaining := time.Until(deadline)
    if remaining > 0 {
        select {
        case <-time.After(remaining):
            return nil
        case <-ctx.Done():
            return ctx.Err()
        }
    }
    return nil
}
```

**Why not Temporal-style**: Temporal's timers are server-managed events. Our timers are simpler—just checkpointed deadlines. The workflow still "waits" during execution, but the deadline survives recovery.

**Tradeoff**: Simpler than Temporal but still provides durable delays.

#### 3. Time Virtualization for Testing (Recommended: Yes, already planned)

**What**: Fake clock injection for deterministic testing.

**Status**: Already in the test plan. Enhance with timer awareness.

```go
// Enhanced FakeClock that integrates with timers
type FakeClock struct {
    mu       sync.Mutex
    now      time.Time
    waiters  []clockWaiter
}

type clockWaiter struct {
    deadline time.Time
    ch       chan struct{}
}

func (c *FakeClock) After(d time.Duration) <-chan struct{} {
    c.mu.Lock()
    defer c.mu.Unlock()

    ch := make(chan struct{})
    deadline := c.now.Add(d)
    c.waiters = append(c.waiters, clockWaiter{deadline, ch})
    return ch
}

func (c *FakeClock) Advance(d time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.now = c.now.Add(d)

    // Fire any waiters whose deadline has passed
    remaining := []clockWaiter{}
    for _, w := range c.waiters {
        if !w.deadline.After(c.now) {
            close(w.ch)
        } else {
            remaining = append(remaining, w)
        }
    }
    c.waiters = remaining
}
```

**Benefit**: Can test hour-long workflows in milliseconds. Essential for timer testing.

#### 4. Deterministic Replay (Recommended: No)

**What**: Rebuild workflow state by replaying event history instead of checkpoints.

**Why not**:
- Requires constraining what users can do in workflows (no random, no system time, no non-deterministic I/O)
- Significantly more complex implementation
- Replay time grows with workflow length
- Conflicts with explicit non-goal

**Current approach is sufficient**: Checkpoints + idempotent activities provide adequate recovery without the complexity burden.

#### 5. SDK-level Determinism Enforcement (Recommended: Partial)

**What**: Temporal forbids non-deterministic operations in workflow code.

**Recommendation**: Don't enforce, but document and provide helpers.

```go
// workflow.Context provides deterministic alternatives
type Context interface {
    // Use instead of time.Now() - returns workflow-relative time
    Now() time.Time

    // Use instead of uuid.New() - deterministic based on execution + step
    DeterministicID(prefix string) string

    // Use instead of rand - seeded from execution ID
    Rand() *rand.Rand
}

// Example usage
func (a *MyActivity) Execute(ctx workflow.Context, params Params) (any, error) {
    // Instead of: id := uuid.New().String()
    id := ctx.DeterministicID("order")

    // Instead of: if rand.Float64() < 0.5
    if ctx.Rand().Float64() < 0.5 { ... }

    return result, nil
}
```

**Benefit**: Helps users write recoverable workflows without strict enforcement.

### Summary: Recommended Adoptions

| Technique | Adopt? | Rationale |
|-----------|--------|-----------|
| Event Log (observability) | Yes | Valuable for debugging/audit without changing recovery |
| Timer Abstraction | Yes | Addresses gap in current design |
| Time Virtualization (testing) | Yes | Already planned, enhance for timers |
| Deterministic Replay | No | Conflicts with non-goals, high complexity |
| SDK Determinism Helpers | Partial | Provide helpers, don't enforce |

### Implementation Priority

1. **Phase 1 (Core)**: Timer abstraction via checkpointed deadlines
2. **Phase 2 (Testing)**: Enhanced FakeClock with timer integration
3. **Phase 3 (Observability)**: Optional event log via callbacks
4. **Phase 4 (Helpers)**: Deterministic context methods

### Testing Implications

With these additions, the test plan should include:

```go
// Timer-specific tests
func TestTimer_SurvivesRecovery(t *testing.T) {
    // 1. Start workflow with 1-hour timer
    // 2. Checkpoint after timer step starts
    // 3. Simulate crash
    // 4. Recover - timer should have ~1 hour remaining (minus elapsed)
}

func TestTimer_FakeClock_Advance(t *testing.T) {
    // 1. Start workflow with 1-hour timer using fake clock
    // 2. Advance clock by 30 minutes - still waiting
    // 3. Advance clock by 31 minutes - timer fires
    // 4. Verify total test time < 1 second
}

func TestTimer_AlreadyElapsed(t *testing.T) {
    // 1. Start workflow with 1-minute timer
    // 2. Checkpoint
    // 3. Wait 2 minutes (in test, use fake clock)
    // 4. Simulate crash and recover
    // 5. Timer should fire immediately (deadline passed)
}

// Event log tests
func TestEventLog_CapturesWorkflowLifecycle(t *testing.T) {
    // Verify events captured: started, step_started, step_completed, ..., completed
}

func TestEventLog_IndependentOfRecovery(t *testing.T) {
    // Events are for observability only - recovery still uses checkpoints
}
```

---

## Summary

This test plan provides comprehensive coverage across:

1. **Unit tests** - Fast, isolated verification of each component
2. **Integration tests** - Component interaction and database behavior
3. **End-to-end tests** - Realistic deployment scenarios
4. **Chaos tests** - Failure resilience validation
5. **Instrumentation** - Production monitoring and development debugging

The combination of in-memory implementations (for speed), real database tests (for correctness), and simulation mode (for deterministic failure testing) enables thorough verification of the engine's reliability guarantees.
