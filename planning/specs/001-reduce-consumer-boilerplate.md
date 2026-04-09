# Spec: Reduce Consumer Boilerplate

_Status: Draft (Revised)_
_Created: 2026-04-08_
_Revised: 2026-04-08_
_PRD: [001-reduce-consumer-boilerplate](../prds/001-reduce-consumer-boilerplate.md)_
_Review: [001-review-combined](001-review-combined.md)_

---

## Design Principles

1. **Additive, not breaking.** New methods and types. Existing `Run()`, `Resume()`, `Checkpointer`, `Context` signatures are untouched.
2. **Compose, don't inherit.** Each piece works standalone. The Runner composes them but isn't required.
3. **Interfaces are small.** One-method interfaces where possible. Consumers implement only what they need.
4. **Errors are values.** Sentinel errors for programmatic matching. `errors.Is` and `errors.As` everywhere.

---

## Phase 1: Foundation

### 1.1 ErrNoCheckpoint Sentinel

**File:** `errors.go`

```go
// ErrNoCheckpoint is returned when Resume or RunOrResume cannot find a
// checkpoint for the given execution ID. Use errors.Is to check for it.
var ErrNoCheckpoint = errors.New("no checkpoint found")
```

The existing `loadCheckpoint` method currently returns `fmt.Errorf("no checkpoint found for execution %q", ...)`. Change it to wrap the sentinel:

```go
func (e *Execution) loadCheckpoint(ctx context.Context, priorExecutionID string) error {
    checkpoint, err := e.checkpointer.LoadCheckpoint(ctx, priorExecutionID)
    if err != nil {
        return fmt.Errorf("loading checkpoint: %w", err)
    }
    if checkpoint == nil {
        return fmt.Errorf("%w: execution %q", ErrNoCheckpoint, priorExecutionID)
    }
    // ... rest unchanged
}
```

Consumers migrate from string matching to:

```go
if errors.Is(err, workflow.ErrNoCheckpoint) {
    // fall back to fresh run
}
```

### 1.2 Resume() Bug Fix

**File:** `execution.go`

**Problem:** `Resume()` calls `start()` before `loadCheckpoint()`. If checkpoint loading fails, the execution is marked as started and cannot be reused for `Run()`.

**Fix:** Load and validate the checkpoint *before* calling `start()`:

```go
func (e *Execution) Resume(ctx context.Context, priorExecutionID string) error {
    // Load checkpoint FIRST, before marking as started
    if err := e.loadCheckpoint(ctx, priorExecutionID); err != nil {
        return err
    }

    // Return early if already completed
    if e.state.GetStatus() == ExecutionStatusCompleted {
        e.logger.Info("execution already completed from checkpoint")
        return nil
    }

    // Now mark as started
    if err := e.start(); err != nil {
        return err
    }

    return e.run(ctx)
}
```

This means a failed `Resume()` (e.g., no checkpoint) leaves the execution object clean for a subsequent `Run()`.

### 1.3 RunOrResume

**File:** `execution.go`

A single entry point for crash-recovery workers. Attempts to resume from a prior checkpoint; falls back to a fresh run if none exists.

```go
// RunOrResume attempts to resume from a prior execution's checkpoint. If no
// checkpoint exists, it starts a fresh run. This is the recommended entry point
// for workers with crash recovery.
//
// If a checkpoint exists but is corrupted or cannot be loaded, RunOrResume
// returns the error rather than silently falling back to a fresh run.
func (e *Execution) RunOrResume(ctx context.Context, priorExecutionID string) error {
    err := e.Resume(ctx, priorExecutionID)
    if errors.Is(err, ErrNoCheckpoint) {
        return e.Run(ctx)
    }
    return err
}
```

The key semantic: only `ErrNoCheckpoint` triggers fallback. Any other error (corrupted data, infrastructure failure) propagates. This is why the sentinel matters.

### 1.4 Structured ExecutionResult

**File:** `execution_result.go` (new)

A richer return type for consumers who want structured output without scattered post-execution calls.

```go
// ExecutionResult contains the outcome of a workflow execution.
// When returned from Execute/ExecuteOrResume, it is always non-nil if error is nil.
type ExecutionResult struct {
    // WorkflowName identifies which workflow was executed.
    WorkflowName string

    // Status is the final execution status.
    Status ExecutionStatus

    // Outputs contains the workflow's output values, keyed by output name.
    // Empty if the workflow failed before producing outputs.
    Outputs map[string]any

    // Error is the classified workflow error, if the execution failed.
    // nil when Status is ExecutionStatusCompleted.
    Error *WorkflowError

    // Timing contains execution duration measurements.
    Timing ExecutionTiming
}

// ExecutionTiming captures wall-clock timing for the execution.
type ExecutionTiming struct {
    StartedAt  time.Time
    FinishedAt time.Time
    Duration   time.Duration
}

// Completed returns true if the execution finished successfully.
func (r *ExecutionResult) Completed() bool {
    return r.Status == ExecutionStatusCompleted
}

// Failed returns true if the execution finished with an error.
func (r *ExecutionResult) Failed() bool {
    return r.Status == ExecutionStatusFailed
}
```

**Changes from prior draft:**
- Added `WorkflowName` — useful when processing results in bulk or logging.
- Removed `FollowUps` field. That's a Phase 4 concern and would create a forward dependency on `FollowUpSpec`, which doesn't exist until Phase 4. Added there instead.
- `Duration` is computed from state timing, not wall clock (see `buildResult` below).

### 1.5 Execute and ExecuteOrResume

**File:** `execution.go`

New methods that return `*ExecutionResult` alongside `error`. The distinction:
- `error` = infrastructure failure (can't load checkpoint, invalid workflow). Result is nil.
- `*ExecutionResult` = the execution ran. Inspect `result.Status` for the outcome.

```go
// Execute runs the workflow and returns a structured result.
//
// An error return means the execution could not be attempted (infrastructure
// failure). When error is nil, result is non-nil and contains the execution
// outcome — including failures, which are represented in result.Error rather
// than the error return.
func (e *Execution) Execute(ctx context.Context) (*ExecutionResult, error) {
    err := e.Run(ctx)
    return e.buildResult(err)
}

// ExecuteOrResume is the structured-result equivalent of RunOrResume.
func (e *Execution) ExecuteOrResume(ctx context.Context, priorExecutionID string) (*ExecutionResult, error) {
    err := e.RunOrResume(ctx, priorExecutionID)
    return e.buildResult(err)
}
```

The internal `buildResult` method:

```go
func (e *Execution) buildResult(runErr error) (*ExecutionResult, error) {
    // If the execution was never started, this is an infrastructure error.
    // We check e.started rather than status because some infra errors
    // (e.g., checkpoint save failure) can occur after execution begins,
    // and we don't want to misclassify those.
    if !e.started {
        return nil, runErr
    }

    result := &ExecutionResult{
        WorkflowName: e.workflow.Name(),
        Status:       e.state.GetStatus(),
        Outputs:      e.state.GetOutputs(),
        Timing: ExecutionTiming{
            StartedAt:  e.state.GetStartTime(),
            FinishedAt: e.state.GetEndTime(),
        },
    }
    result.Timing.Duration = result.Timing.FinishedAt.Sub(result.Timing.StartedAt)

    if result.Status == ExecutionStatusFailed && runErr != nil {
        result.Error = ClassifyError(runErr)
    }

    return result, nil
}
```

**Changes from prior draft:**
- Uses `e.started` (an explicit boolean) instead of status-sniffing to distinguish "never ran" from "ran but hit infra error." This is more robust because some infrastructure failures (checkpoint save, activity logging) happen *after* execution has started and would otherwise be misclassified as workflow failures.
- Uses `e.state.GetEndTime()` instead of `time.Now()` for `FinishedAt`. The execution state records the actual finish time in `SetFinished()`. This avoids divergence if there's post-execution processing.

**Prerequisite:** Add a `GetEndTime()` accessor to `ExecutionState`:

```go
// GetEndTime returns the execution end time.
func (s *ExecutionState) GetEndTime() time.Time {
    s.mutex.RLock()
    defer s.mutex.RUnlock()
    return s.endTime
}
```

### Phase 1 Consumer Migration

Before:
```go
exec1, _ := workflow.NewExecution(opts)
err := exec1.Resume(ctx, priorID)
if err != nil && strings.Contains(err.Error(), "no checkpoint found") {
    exec2, _ := workflow.NewExecution(opts) // must recreate — exec1 is tainted
    err = exec2.Run(ctx)
}
if err != nil {
    // manually classify error, extract outputs, compute timing...
}
```

After:
```go
exec, _ := workflow.NewExecution(opts)
result, err := exec.ExecuteOrResume(ctx, priorID)
if err != nil {
    return err // infrastructure failure
}
if result.Completed() {
    fmt.Println(result.Outputs)
}
```

---

## Phase 2: Developer Experience

### 2.1 Workflow Validation

**File:** `validate.go` (new)

Structural validation that catches problems at registration time rather than deep into a runtime execution.

```go
// ValidationProblem describes a single structural issue in a workflow.
type ValidationProblem struct {
    // Step is the name of the step where the problem was found.
    // Empty for workflow-level problems.
    Step string

    // Message describes the problem.
    Message string
}

func (p ValidationProblem) String() string {
    if p.Step != "" {
        return fmt.Sprintf("step %q: %s", p.Step, p.Message)
    }
    return p.Message
}

// ValidationError contains all problems found during validation.
type ValidationError struct {
    Problems []ValidationProblem
}

func (e *ValidationError) Error() string {
    var b strings.Builder
    fmt.Fprintf(&b, "workflow validation failed (%d problems):", len(e.Problems))
    for _, p := range e.Problems {
        fmt.Fprintf(&b, "\n  - %s", p)
    }
    return b.String()
}
```

The `Validate` method on `Workflow`:

```go
// Validate checks the workflow for structural problems: unreachable steps,
// invalid join configurations, and dangling catch handler references.
//
// Returns nil if the workflow is valid. Returns *ValidationError if problems
// are found. Call this at registration/startup time to fail fast.
//
// Validate does not check activity names. Activity mismatches surface
// immediately at runtime when the step executes, and validating them here
// would require passing activities before they're available.
func (w *Workflow) Validate() error {
    var problems []ValidationProblem

    // 1. Reachability: all steps reachable from start via BFS
    reachable := w.reachableSteps()
    for _, step := range w.steps {
        if !reachable[step.Name] {
            problems = append(problems, ValidationProblem{
                Step:    step.Name,
                Message: "unreachable from start step",
            })
        }
    }

    // 2. Join configuration validity
    for _, step := range w.steps {
        if step.Join == nil {
            continue
        }
        for _, path := range step.Join.Paths {
            if !w.pathExists(path) {
                problems = append(problems, ValidationProblem{
                    Step:    step.Name,
                    Message: fmt.Sprintf("join references unknown path %q", path),
                })
            }
        }
    }

    // 3. Catch handler next-step validity
    for _, step := range w.steps {
        for _, c := range step.Catch {
            if _, ok := w.stepsByName[c.Next]; !ok {
                problems = append(problems, ValidationProblem{
                    Step:    step.Name,
                    Message: fmt.Sprintf("catch handler references unknown step %q", c.Next),
                })
            }
        }
    }

    if len(problems) > 0 {
        return &ValidationError{Problems: problems}
    }
    return nil
}
```

Helper methods on Workflow (private):

```go
// reachableSteps returns the set of step names reachable from the start step.
func (w *Workflow) reachableSteps() map[string]bool { ... }

// pathExists returns whether a named path is defined on any edge in the workflow.
func (w *Workflow) pathExists(name string) bool { ... }
```

### 2.2 Testing Utilities

**Package:** `workflowtest` (new package, `workflowtest/` directory)

The standard Go pattern for test helpers is a separate package (`net/http/httptest`, `io/iotest`). This is discoverable, conventional, and provides clean separation. A `//go:build !release` tag is non-standard for Go libraries: it requires every consumer's build system to know about it, and someone importing test helpers in production code won't get a compile error — only a mysterious build failure in release mode.

**File:** `workflowtest/workflowtest.go`

```go
package workflowtest

import (
    "context"
    "testing"

    "github.com/deepnoodle-ai/workflow"
)

// TestOptions allows overriding execution settings for test runs.
// Only fields that make sense to customize in tests are exposed.
type TestOptions struct {
    // ExecutionID sets a fixed execution ID. Auto-generated if empty.
    ExecutionID string

    // Checkpointer overrides the default in-memory checkpointer.
    Checkpointer workflow.Checkpointer

    // Callbacks receives execution lifecycle events.
    Callbacks workflow.ExecutionCallbacks

    // StepProgressStore receives step progress updates.
    StepProgressStore workflow.StepProgressStore
}

// Run executes a workflow with sensible defaults for testing.
// It uses an in-memory checkpointer, discards logs, and fails the test on
// infrastructure errors. Returns the execution result for assertions.
func Run(
    t testing.TB,
    wf *workflow.Workflow,
    activities []workflow.Activity,
    inputs map[string]any,
) *workflow.ExecutionResult {
    t.Helper()
    return RunWithOptions(t, wf, activities, inputs, TestOptions{})
}

// RunWithOptions is like Run but allows overriding execution options.
func RunWithOptions(
    t testing.TB,
    wf *workflow.Workflow,
    activities []workflow.Activity,
    inputs map[string]any,
    opts TestOptions,
) *workflow.ExecutionResult {
    t.Helper()

    checkpointer := opts.Checkpointer
    if checkpointer == nil {
        checkpointer = NewMemoryCheckpointer()
    }

    exec, err := workflow.NewExecution(workflow.ExecutionOptions{
        Workflow:           wf,
        Activities:         activities,
        Inputs:             inputs,
        ExecutionID:        opts.ExecutionID,
        Checkpointer:       checkpointer,
        ExecutionCallbacks: opts.Callbacks,
        StepProgressStore:  opts.StepProgressStore,
    })
    if err != nil {
        t.Fatalf("workflowtest.Run: creating execution: %v", err)
    }

    result, err := exec.Execute(context.Background())
    if err != nil {
        t.Fatalf("workflowtest.Run: executing: %v", err)
    }
    return result
}
```

**Changes from prior draft:**
- Moved to a `workflowtest` package instead of build-tagged `testing.go`.
- `TestRunWithOptions` takes a `TestOptions` struct instead of `ExecutionOptions`. Only testable fields are exposed, so consumers can't set `Workflow`/`Activities`/`Inputs` on the options struct and be confused when they're ignored.
- Functions named `Run`/`RunWithOptions` since the package name provides the `workflowtest.` prefix.

### 2.3 MemoryCheckpointer

**File:** `workflowtest/memory_checkpointer.go`

An in-memory checkpointer for testing. Lives in `workflowtest` so it's discoverable alongside the other test helpers.

```go
package workflowtest

// MemoryCheckpointer is an in-memory Checkpointer for use in tests.
// It is safe for concurrent use.
type MemoryCheckpointer struct {
    mu          sync.RWMutex
    checkpoints map[string]*workflow.Checkpoint
}

// NewMemoryCheckpointer returns a new in-memory checkpointer.
func NewMemoryCheckpointer() *MemoryCheckpointer {
    return &MemoryCheckpointer{
        checkpoints: make(map[string]*workflow.Checkpoint),
    }
}

func (m *MemoryCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *workflow.Checkpoint) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    m.checkpoints[checkpoint.ExecutionID] = deepCopyCheckpoint(checkpoint)
    return nil
}

func (m *MemoryCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*workflow.Checkpoint, error) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    cp, ok := m.checkpoints[executionID]
    if !ok {
        return nil, nil // Follows existing convention: nil, nil = not found
    }
    return deepCopyCheckpoint(cp), nil
}

func (m *MemoryCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    delete(m.checkpoints, executionID)
    return nil
}

// Checkpoints returns a snapshot of all stored checkpoints, keyed by execution ID.
// Useful for test assertions.
func (m *MemoryCheckpointer) Checkpoints() map[string]*workflow.Checkpoint {
    m.mu.RLock()
    defer m.mu.RUnlock()
    result := make(map[string]*workflow.Checkpoint, len(m.checkpoints))
    for k, v := range m.checkpoints {
        result[k] = deepCopyCheckpoint(v)
    }
    return result
}
```

### 2.4 Mock Activities

**File:** `workflowtest/mock.go`

Stubs for testing control flow without real activity implementations.

```go
package workflowtest

// MockActivity creates a stub activity that always returns the given result.
func MockActivity(name string, result any) workflow.Activity {
    return workflow.NewActivityFunction(name, func(ctx workflow.Context, params map[string]any) (any, error) {
        return result, nil
    })
}

// MockActivityError creates a stub activity that always returns the given error.
func MockActivityError(name string, err error) workflow.Activity {
    return workflow.NewActivityFunction(name, func(ctx workflow.Context, params map[string]any) (any, error) {
        return nil, err
    })
}
```

**Change from prior draft:** Dropped `MockActivityFunc`. It's literally `workflow.NewActivityFunction(name, fn)` — the `workflowtest` package name already signals test intent. Two mock helpers are sufficient.

Usage:

```go
result := workflowtest.Run(t, wf, []workflow.Activity{
    workflowtest.MockActivity("fetch-data", map[string]any{"items": 42}),
    workflowtest.MockActivity("process", "done"),
}, nil)
```

---

## Phase 3: Production Patterns

### 3.1 Step Progress Tracking

**File:** `step_progress.go` (new)

The library tracks step state transitions and calls a consumer-provided store.

#### Types

```go
// StepStatus represents the execution state of a step.
type StepStatus string

const (
    StepStatusPending   StepStatus = "pending"
    StepStatusRunning   StepStatus = "running"
    StepStatusCompleted StepStatus = "completed"
    StepStatusFailed    StepStatus = "failed"
    StepStatusSkipped   StepStatus = "skipped"
)

// StepProgress describes the current state of a step within an execution.
type StepProgress struct {
    // StepName identifies the step.
    StepName string

    // PathID identifies the execution path this step is running on.
    // Required because the engine is path-aware: the same step can run
    // concurrently on different paths (branching, Each loops).
    PathID string

    // Status is the current step status.
    Status StepStatus

    // ActivityName is the activity bound to this step.
    ActivityName string

    // Attempt is the current attempt number (1-based). Increments on retries.
    Attempt int

    // Detail is an optional progress message set by activities via
    // workflow.ReportProgress(). Empty unless the activity reports intra-step
    // progress.
    Detail string

    // StartedAt is when the step began executing. Zero for pending steps.
    StartedAt time.Time

    // FinishedAt is when the step completed or failed. Zero for running steps.
    FinishedAt time.Time

    // Error is the error message if the step failed. Empty otherwise.
    Error string
}
```

**Changes from prior draft:**
- Added `PathID` field. The engine is path-aware: callbacks carry `PathID`, steps can branch and run concurrently, and `Each` loops execute the same step in parallel. Without `PathID`, progress for parallel branches would overwrite itself. The compound key for persistence is `(execution_id, step_name, path_id)`.
- Dropped `Duration` field. For running steps, any computed duration is stale by the time the store persists it. Consumers compute duration from `StartedAt` + current time (running) or `FinishedAt - StartedAt` (completed). Cleaner contract.

#### Store Interface

```go
// StepProgressStore persists step progress updates. Implement this interface
// to write step progress to your backend (database, cache, API, etc.).
//
// UpdateStepProgress is called asynchronously on every step state transition
// and on intra-activity progress reports. Errors are logged but do not fail
// the workflow — step progress is observability, not correctness.
type StepProgressStore interface {
    UpdateStepProgress(ctx context.Context, executionID string, progress StepProgress) error
}
```

One method. Consumers implement ~15 lines.

#### Internal Tracker

The library provides an internal `stepProgressTracker` that implements `ExecutionCallbacks` and drives the store. It is not exported — consumers interact only through `StepProgressStore`.

```go
// stepProgressTracker listens to execution callbacks and derives step
// state transitions. It calls the StepProgressStore asynchronously on
// each transition.
type stepProgressTracker struct {
    BaseExecutionCallbacks
    executionID string
    store       StepProgressStore
    logger      *slog.Logger
    mu          sync.Mutex
    steps       map[stepKey]*StepProgress // keyed by (step_name, path_id)
}

type stepKey struct {
    stepName string
    pathID   string
}
```

**Async dispatch:** Since progress tracking is observability and the spec says errors don't fail the workflow, the tracker dispatches store calls asynchronously via a fire-and-forget goroutine. This keeps `UpdateStepProgress` latency off the critical execution path. Errors are logged.

```go
func (t *stepProgressTracker) dispatch(ctx context.Context, progress StepProgress) {
    go func() {
        if err := t.store.UpdateStepProgress(ctx, t.executionID, progress); err != nil {
            t.logger.Error("step progress update failed",
                "step", progress.StepName,
                "path", progress.PathID,
                "error", err,
            )
        }
    }()
}
```

**Tracker initialization:** The tracker needs the full step list to emit initial "pending" states. It gets this from the workflow's `Steps()` method when the `BeforeWorkflowExecution` callback fires. Steps on branches that are never taken remain "pending" — this is accurate (they were never reached). When `Each` loops dynamically spawn paths, the tracker picks them up from `BeforeActivityExecution` callbacks and creates entries on the fly.

The tracker is wired in automatically when `ExecutionOptions.StepProgressStore` is set:

```go
// In ExecutionOptions:
type ExecutionOptions struct {
    // ... existing fields ...

    // StepProgressStore receives step progress updates during execution.
    // When set, the library automatically tracks step state transitions
    // and calls UpdateStepProgress on each change. Calls are async —
    // store latency does not affect execution speed.
    StepProgressStore StepProgressStore
}
```

Wiring in `NewExecution`:

```go
// In NewExecution, after building execution:
if opts.StepProgressStore != nil {
    tracker := newStepProgressTracker(execution.ID(), opts.StepProgressStore, logger)
    // Compose with any existing callbacks
    chain := NewCallbackChain(opts.ExecutionCallbacks, tracker)
    execution.executionCallbacks = chain
}
```

### 3.2 Intra-Activity Progress Reporting

**File:** `progress.go` (new)

Adding `ReportProgress` directly to the `Context` interface would break every external type that implements it. Instead, use an optional side interface with a package-level helper — the same pattern as `io.WriterTo` and `http.Flusher` in stdlib.

```go
// ProgressReporter is an optional interface that workflow contexts may
// implement to support intra-activity progress reporting.
type ProgressReporter interface {
    ReportProgress(detail string)
}

// ReportProgress reports intra-activity progress. If the context supports
// progress reporting (i.e., a StepProgressStore is configured), the detail
// string is forwarded to the store. Otherwise this is a no-op.
//
// Example:
//
//     workflow.ReportProgress(ctx, "Processing 3 of 12 items")
func ReportProgress(ctx Context, detail string) {
    if pr, ok := ctx.(ProgressReporter); ok {
        pr.ReportProgress(detail)
    }
}
```

**Change from prior draft:** The `Context` interface is **not modified**. This was the most critical review finding — adding a method to the exported `Context` interface would break every external implementation, violating our first design principle. The package-level `ReportProgress` function is the public API. Activities call `workflow.ReportProgress(ctx, "...")`.

Implementation in `executionContext`:

```go
// executionContext gains a progressReporter field but the Context interface
// is unchanged. It implements ProgressReporter as a separate interface.
type executionContext struct {
    context.Context
    *PathLocalState
    logger           *slog.Logger
    compiler         script.Compiler
    pathID           string
    stepName         string
    progressReporter func(detail string) // nil when no store is configured
}

func (w *executionContext) ReportProgress(detail string) {
    if w.progressReporter != nil {
        w.progressReporter(detail)
    }
}
```

The `progressReporter` function is injected by the execution when creating the activity context. It captures the tracker reference and calls `dispatch` with the detail. When no `StepProgressStore` is configured, it remains nil and `ReportProgress` is a no-op.

### 3.3 WithFencing Checkpointer Wrapper

**File:** `checkpointer_fenced.go` (new)

Wraps any `Checkpointer` with a pre-save fence check for distributed lease validation.

```go
// FenceFunc validates that the current worker still holds its lease or lock.
// Return nil if the fence is still valid. Return an error to abort the
// checkpoint save — the execution will receive the error and should terminate.
type FenceFunc func(ctx context.Context) error

// ErrFenceViolation is returned when a fence check fails, indicating the
// worker has lost its lease and should stop processing. ErrFenceViolation
// is always wrapped with the original fence check error for context.
//
// ErrFenceViolation bypasses retry and catch handlers. The engine treats it
// as non-retryable and non-catchable, similar to ErrorTypeFatal. A lost
// lease is not a recoverable activity error — retrying on the same worker
// is pointless and catching it would mask the real problem.
var ErrFenceViolation = errors.New("fence violation: lease lost")

// WithFencing wraps a Checkpointer with a pre-save fence validation. Before
// each SaveCheckpoint call, fenceCheck is called. If it returns an error, the
// save is aborted and the error is returned wrapped with ErrFenceViolation.
//
// LoadCheckpoint and DeleteCheckpoint pass through to the inner checkpointer
// without fence checks.
//
// Use this with distributed workers to prevent stale workers from overwriting
// checkpoint state after losing their lease.
func WithFencing(inner Checkpointer, fenceCheck FenceFunc) Checkpointer {
    return &fencedCheckpointer{inner: inner, fenceCheck: fenceCheck}
}

type fencedCheckpointer struct {
    inner      Checkpointer
    fenceCheck FenceFunc
}

func (f *fencedCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error {
    if err := f.fenceCheck(ctx); err != nil {
        // Always wrap so consumers don't need to know about library sentinels.
        // If the consumer already returned ErrFenceViolation, don't double-wrap.
        if !errors.Is(err, ErrFenceViolation) {
            return fmt.Errorf("%w: %w", ErrFenceViolation, err)
        }
        return err
    }
    return f.inner.SaveCheckpoint(ctx, checkpoint)
}

func (f *fencedCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
    return f.inner.LoadCheckpoint(ctx, executionID)
}

func (f *fencedCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
    return f.inner.DeleteCheckpoint(ctx, executionID)
}
```

**Change from prior draft:** Specified that `ErrFenceViolation` must bypass retry and catch logic. In the engine, `MatchesErrorType` should be updated to treat fence violations like fatal errors:

```go
// In MatchesErrorType, add a check before the existing logic:
func MatchesErrorType(err error, errorType string) bool {
    // Fence violations are never retryable or catchable
    if errors.Is(err, ErrFenceViolation) {
        return false
    }
    // ... existing classification logic
}
```

This ensures a fence failure propagates immediately rather than being caught by a retry config or catch handler. The consumer's fence function doesn't need to know about library sentinels — `WithFencing` always wraps the error.

Consumer usage:

```go
checkpointer := workflow.WithFencing(pgCheckpointer, func(ctx context.Context) error {
    if !leaseManager.StillHoldsLease(ctx, workerID) {
        return fmt.Errorf("worker %s lost lease", workerID)
    }
    return nil
})
```

### 3.4 OverrideStepStatus — Deferred

**Not included in this spec.** The review correctly identified two problems:

1. Shipping something pre-deprecated signals "we know this is wrong." It establishes an API surface that will calcify.
2. The Runner creates the Execution internally (in the prior draft) and never exposes it, so Runner consumers can't call it anyway.

Consumers already have working workarounds for review-pause patterns. A first-class Wait step type is the right solution and should be designed separately.

---

## Phase 4: Composition

### 4.1 Runner

**File:** `runner.go` (new)

The Runner is the recommended entry point for production consumers. It composes heartbeating, timeout management, structured results, and completion hooks into a single `Run` call.

#### Configuration Types

```go
// RunnerConfig holds reusable settings for a Runner. These are typically set
// once at application startup and shared across all executions.
type RunnerConfig struct {
    // Logger is the structured logger. Defaults to a discard logger.
    Logger *slog.Logger

    // DefaultTimeout is applied to every execution unless overridden in
    // RunOptions. Zero means no timeout.
    DefaultTimeout time.Duration
}

// RunOptions holds per-execution lifecycle settings that the Runner manages
// on top of a caller-created Execution.
type RunOptions struct {
    // PriorExecutionID triggers resume-or-run behavior. When set, the Runner
    // attempts to resume from this execution's checkpoint before falling back
    // to a fresh run. When empty, always starts fresh.
    PriorExecutionID string

    // Heartbeat configures periodic liveness checks. Optional.
    Heartbeat *HeartbeatConfig

    // CompletionHook is called after successful execution to produce follow-up
    // workflow specs. Optional. See CompletionHook for details.
    CompletionHook CompletionHook

    // Timeout overrides RunnerConfig.DefaultTimeout for this execution.
    // Zero means use the default; negative means no timeout.
    Timeout time.Duration
}

// HeartbeatConfig configures periodic liveness reporting.
type HeartbeatConfig struct {
    // Interval is how often the heartbeat function is called.
    Interval time.Duration

    // Func is called on each heartbeat tick. Return nil to indicate liveness.
    // Return an error to signal lease loss — the Runner cancels the execution
    // context, causing the workflow to abort. The in-progress step receives
    // context cancellation and should clean up.
    Func HeartbeatFunc
}

// HeartbeatFunc is called periodically to prove worker liveness.
// Return nil to continue. Return an error to abort the execution.
type HeartbeatFunc func(ctx context.Context) error
```

**Changes from prior draft:**
- The Runner accepts an `*Execution` instead of creating one internally. This eliminates the duplication between `RunOptions` and `ExecutionOptions`. `RunOptions` shrinks to just lifecycle concerns (PriorExecutionID, Heartbeat, CompletionHook, Timeout). The consumer creates the `Execution` with their existing `ExecutionOptions` and passes it to the Runner.
- The Runner method is named `Run` instead of `Execute` to avoid collision with `Execution.Execute`.

#### Runner

```go
// Runner manages the full lifecycle of workflow executions. It composes
// heartbeating, crash recovery (RunOrResume), structured results, and
// completion hooks.
//
// Create a Runner once at startup and call Run for each workflow execution.
type Runner struct {
    logger         *slog.Logger
    defaultTimeout time.Duration
}

// NewRunner creates a Runner with the given configuration.
func NewRunner(cfg RunnerConfig) *Runner {
    logger := cfg.Logger
    if logger == nil {
        logger = slog.New(slog.NewTextHandler(io.Discard, nil))
    }
    return &Runner{
        logger:         logger,
        defaultTimeout: cfg.DefaultTimeout,
    }
}
```

#### Run Method

```go
// Run executes a workflow with full lifecycle management. It:
//
//  1. Starts a heartbeat goroutine (if configured)
//  2. Applies a timeout (if configured)
//  3. Calls ExecuteOrResume or Execute (depending on PriorExecutionID)
//  4. Stops the heartbeat and collects the result
//  5. Calls the CompletionHook (if configured and execution succeeded)
//  6. Returns the structured ExecutionResult
//
// The caller creates the Execution with their own ExecutionOptions.
// The Runner manages lifecycle concerns on top of that.
//
// The error return indicates infrastructure failures (execution couldn't run).
// Workflow-level failures are in result.Error.
func (r *Runner) Run(
    ctx context.Context,
    exec *Execution,
    opts RunOptions,
) (*ExecutionResult, error) {
    // Apply timeout
    timeout := r.resolveTimeout(opts.Timeout)
    if timeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, timeout)
        defer cancel()
    }

    // Create a cancellable context for the execution. The heartbeat
    // cancels this context on lease loss.
    execCtx, execCancel := context.WithCancel(ctx)
    defer execCancel()

    // Start heartbeat
    if opts.Heartbeat != nil {
        stopHeartbeat := r.startHeartbeat(execCtx, execCancel, opts.Heartbeat)
        defer stopHeartbeat()
    }

    // Run or resume
    var result *ExecutionResult
    var err error
    if opts.PriorExecutionID != "" {
        result, err = exec.ExecuteOrResume(execCtx, opts.PriorExecutionID)
    } else {
        result, err = exec.Execute(execCtx)
    }
    if err != nil {
        return nil, err
    }

    // Completion hook
    if result.Completed() && opts.CompletionHook != nil {
        followUps, hookErr := opts.CompletionHook(ctx, result)
        if hookErr != nil {
            r.logger.Error("completion hook failed",
                "execution_id", exec.ID(),
                "error", hookErr,
            )
            // Result is still "completed" — the work was done.
            // Hook error doesn't change the execution outcome.
        }
        result.FollowUps = followUps
    }

    return result, nil
}
```

**Critical fix from review:** The heartbeat now cancels the *execution context*, not just its own child context. The prior draft created `hbCtx, hbCancel := context.WithCancel(ctx)` inside `startHeartbeat`, which meant `hbCancel()` only cancelled the heartbeat goroutine — the workflow kept running. Now, `Runner.Run` creates the cancellable execution context and passes `execCancel` to `startHeartbeat`.

Heartbeat goroutine management (private):

```go
// startHeartbeat launches a goroutine that calls the heartbeat function on
// the configured interval. Returns a stop function that blocks until the
// goroutine exits.
//
// execCancel is called on heartbeat failure to cancel the execution context.
func (r *Runner) startHeartbeat(ctx context.Context, execCancel context.CancelFunc, cfg *HeartbeatConfig) func() {
    // A separate cancel to stop the heartbeat goroutine itself on normal completion
    hbCtx, hbCancel := context.WithCancel(ctx)

    done := make(chan struct{})
    go func() {
        defer close(done)
        ticker := time.NewTicker(cfg.Interval)
        defer ticker.Stop()
        for {
            select {
            case <-hbCtx.Done():
                return
            case <-ticker.C:
                if err := cfg.Func(hbCtx); err != nil {
                    r.logger.Error("heartbeat failed, canceling execution", "error", err)
                    execCancel() // cancel the execution, not just the heartbeat
                    return
                }
            }
        }
    }()

    return func() {
        hbCancel() // stop the heartbeat goroutine
        <-done     // wait for it to exit
    }
}

func (r *Runner) resolveTimeout(override time.Duration) time.Duration {
    if override < 0 {
        return 0 // explicit no-timeout
    }
    if override > 0 {
        return override
    }
    return r.defaultTimeout
}
```

### 4.2 Completion Hooks

**File:** `completion_hook.go` (new)

Completion hooks let workflows declare follow-up work after successful execution. The library calls the hook and includes the results in `ExecutionResult`. The consumer owns the durable outbox.

```go
// CompletionHook is called after a workflow completes successfully. It returns
// follow-up specs describing workflows that should be triggered as a result
// of this execution.
//
// The hook runs synchronously after the execution completes. Keep it fast —
// it should build descriptors, not execute workflows. The consumer persists
// the FollowUpSpecs to their own durable outbox for async processing.
//
// Returning an error does not change the execution result — the workflow is
// still completed. The error is logged and the consumer can inspect
// result.FollowUps to see what was produced before the error.
type CompletionHook func(ctx context.Context, result *ExecutionResult) ([]FollowUpSpec, error)

// FollowUpSpec describes a workflow that should be triggered after a
// successful execution. It is a descriptor, not an execution request —
// the consumer is responsible for persisting and processing these.
//
// FollowUpSpec is intentionally separate from ChildWorkflowSpec.
// ChildWorkflowSpec is an execution request used within a running workflow
// (sync or async child execution). FollowUpSpec is a post-completion
// descriptor for the consumer's outbox — different lifecycle, different
// owner, different semantics.
type FollowUpSpec struct {
    // WorkflowName identifies which workflow to trigger.
    WorkflowName string

    // Inputs are the input values for the follow-up workflow.
    Inputs map[string]any

    // Metadata is arbitrary data the consumer can use for routing,
    // deduplication, or prioritization. The library does not inspect it.
    Metadata map[string]any
}
```

**Change from prior draft:** Added explicit documentation on why `FollowUpSpec` is separate from `ChildWorkflowSpec`. The review correctly identified the vocabulary overlap. The distinction: `ChildWorkflowSpec` is an *execution request* used within a running workflow (the engine or executor acts on it). `FollowUpSpec` is a *post-completion descriptor* for the consumer's outbox (the consumer acts on it). Different lifecycle, different owner.

### 4.3 FollowUps on ExecutionResult

The `FollowUps` field is added to `ExecutionResult` in Phase 4, not Phase 1:

```go
// In execution_result.go, added in Phase 4:
type ExecutionResult struct {
    // ... Phase 1 fields ...

    // FollowUps contains follow-up workflow specs produced by completion hooks.
    // Empty when no hooks are configured or the execution did not complete
    // successfully. Added in Phase 4.
    FollowUps []FollowUpSpec
}
```

### 4.4 Runner Consumer Example

Full production consumer using the Runner:

```go
func processWorkflow(ctx context.Context, job *Job) error {
    // Create the execution with all the standard options
    exec, err := workflow.NewExecution(workflow.ExecutionOptions{
        Workflow:          job.Workflow,
        Activities:        allActivities,
        Inputs:            job.Inputs,
        ExecutionID:       job.ExecutionID,
        Checkpointer:      workflow.WithFencing(pgCheckpointer, leaseCheck(job.ID)),
        StepProgressStore: &DBStepProgressStore{db: db, jobID: job.ID},
        Logger:            slog.Default(),
    })
    if err != nil {
        return fmt.Errorf("creating execution: %w", err)
    }

    // Run with lifecycle management
    runner := workflow.NewRunner(workflow.RunnerConfig{
        Logger:         slog.Default(),
        DefaultTimeout: 30 * time.Minute,
    })

    result, err := runner.Run(ctx, exec, workflow.RunOptions{
        PriorExecutionID: job.PriorExecutionID, // non-empty on retry attempts

        Heartbeat: &workflow.HeartbeatConfig{
            Interval: 10 * time.Second,
            Func: func(ctx context.Context) error {
                return leaseManager.Renew(ctx, job.ID, workerID)
            },
        },

        CompletionHook: func(ctx context.Context, result *workflow.ExecutionResult) ([]workflow.FollowUpSpec, error) {
            if reportWF := result.Outputs["report_workflow"]; reportWF != nil {
                return []workflow.FollowUpSpec{{
                    WorkflowName: reportWF.(string),
                    Inputs:       result.Outputs,
                }}, nil
            }
            return nil, nil
        },
    })
    if err != nil {
        return fmt.Errorf("executing workflow: %w", err)
    }

    if result.Failed() {
        return fmt.Errorf("workflow failed: %s", result.Error.Cause)
    }

    // Persist follow-ups to outbox
    for _, f := range result.FollowUps {
        if err := outbox.Enqueue(ctx, f); err != nil {
            log.Error("failed to enqueue follow-up", "workflow", f.WorkflowName, "error", err)
        }
    }

    return nil
}
```

The `DBStepProgressStore` the consumer writes:

```go
type DBStepProgressStore struct {
    db    *sql.DB
    jobID string
}

func (s *DBStepProgressStore) UpdateStepProgress(ctx context.Context, executionID string, p workflow.StepProgress) error {
    _, err := s.db.ExecContext(ctx, `
        INSERT INTO step_progress (job_id, execution_id, step_name, path_id, status, detail, started_at, finished_at, error)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT (job_id, step_name, path_id) DO UPDATE SET
            status = EXCLUDED.status,
            detail = EXCLUDED.detail,
            started_at = EXCLUDED.started_at,
            finished_at = EXCLUDED.finished_at,
            error = EXCLUDED.error`,
        s.jobID, executionID, p.StepName, p.PathID, p.Status, p.Detail, p.StartedAt, p.FinishedAt, p.Error,
    )
    return err
}
```

~15 lines. Down from ~170.

---

## New Files Summary

| File | Phase | Contents |
|------|-------|----------|
| `execution_result.go` | 1 | `ExecutionResult`, `ExecutionTiming` |
| `validate.go` | 2 | `Validate()`, `ValidationError`, `ValidationProblem`, helpers |
| `workflowtest/workflowtest.go` | 2 | `Run`, `RunWithOptions`, `TestOptions` |
| `workflowtest/memory_checkpointer.go` | 2 | `MemoryCheckpointer` |
| `workflowtest/mock.go` | 2 | `MockActivity`, `MockActivityError` |
| `step_progress.go` | 3 | `StepProgress`, `StepStatus`, `StepProgressStore`, `stepProgressTracker` |
| `progress.go` | 3 | `ProgressReporter` interface, `ReportProgress` package-level func |
| `checkpointer_fenced.go` | 3 | `WithFencing`, `FenceFunc`, `ErrFenceViolation` |
| `runner.go` | 4 | `Runner`, `RunnerConfig`, `RunOptions`, `HeartbeatConfig`, `HeartbeatFunc` |
| `completion_hook.go` | 4 | `CompletionHook`, `FollowUpSpec` |

## Modified Files Summary

| File | Phase | Changes |
|------|-------|---------|
| `errors.go` | 1 | Add `ErrNoCheckpoint` sentinel |
| `execution.go` | 1, 3 | Fix `Resume()` ordering, add `RunOrResume()`, `Execute()`, `ExecuteOrResume()`, `buildResult()`. Add `StepProgressStore` to `ExecutionOptions`, wire tracker in `NewExecution`. |
| `execution_state.go` | 1 | Add `GetEndTime()` accessor |
| `context.go` | 3 | Add `progressReporter` field to `executionContext`, implement `ProgressReporter` interface. **No changes to `Context` interface.** |
| `errors.go` | 3 | Update `MatchesErrorType` to treat `ErrFenceViolation` as non-retryable/non-catchable |

---

## Design Decisions

### Why `Execute()` + `Run()` instead of changing `Run()`?

Backward compatibility. Existing consumers using `Run()` don't need to change. The `Execute()` methods are opt-in for consumers who want structured results. If adoption is universal, we can deprecate `Run()`/`Resume()` later.

### Why not a `StepProgressCallback` instead of `StepProgressStore`?

The store interface is more constrained. A callback could do anything; a store communicates that the purpose is persistence. It also makes the contract clearer: the library owns state derivation, the consumer owns persistence. The single-method interface is easy to mock in tests.

### Why `StepProgress` as a value type (not pointer)?

Step progress updates are frequent, short-lived, and should not be mutated after being passed to the store. Value semantics make this clear and avoid shared-mutation bugs.

### Why async store dispatch?

The spec says "errors from UpdateStepProgress are logged but do not fail the workflow." If the result is discarded anyway, blocking the execution on a potentially slow remote store call adds latency to the critical path for no correctness benefit. Fire-and-forget with error logging matches the stated semantics.

### Why does the heartbeat cancel on fence failure rather than return an error?

The execution is already in progress. The only safe action is to stop it. Context cancellation is the standard Go mechanism for cooperative shutdown. The activity receives cancellation and can clean up. The checkpoint from the last completed step survives for the next worker.

### Why isn't `FollowUpSpec` richer (priority, delay, etc.)?

Keep it minimal. The consumer's outbox owns scheduling semantics. `Metadata map[string]any` is the escape hatch for anything the consumer needs. We can promote common metadata patterns to first-class fields later based on real usage.

### Why `testing.TB` instead of `*testing.T`?

`testing.TB` is the common interface between `*testing.T` and `*testing.B`. Test helpers that accept `TB` work in both unit tests and benchmarks. No cost, strictly more flexible.

### Why `workflowtest` package instead of build-tagged `testing.go`?

The `//go:build !release` approach is non-standard for Go libraries. It requires every consumer's build system to know about the tag, and importing test helpers in production code won't produce a compile error — only a mysterious failure in release mode. The separate `workflowtest` package follows the stdlib convention (`net/http/httptest`, `io/iotest`), is discoverable, and provides clean import-time separation.

### Why `ReportProgress` as a package-level function instead of a `Context` method?

Adding a method to the exported `Context` interface breaks every external implementation. The optional side interface pattern (`ProgressReporter` + `ReportProgress(ctx, detail)`) follows `io.WriterTo` / `http.Flusher` conventions. Activities call `workflow.ReportProgress(ctx, "...")` — clean and non-breaking.

### Why does the Runner accept `*Execution` instead of creating one internally?

The prior draft had `RunOptions` with 10 fields that largely mirrored `ExecutionOptions`, creating configuration soup and a learning burden. By accepting a caller-created `*Execution`, the Runner focuses purely on lifecycle concerns (heartbeat, timeout, completion hooks, resume-or-run). `RunOptions` shrinks to 4 fields. Consumers use the same `ExecutionOptions` they already know.

### Why `FollowUpSpec` separate from `ChildWorkflowSpec`?

`ChildWorkflowSpec` is an *execution request* — the engine or executor acts on it within a running workflow. `FollowUpSpec` is a *post-completion descriptor* — the consumer acts on it after the workflow finishes. Different lifecycle (during vs. after execution), different owner (engine vs. consumer), different semantics (immediate action vs. outbox entry). Collapsing them would conflate concerns.

### Why `ErrFenceViolation` bypasses retry and catch handlers?

A fence violation means the worker lost its lease. Retrying on the same worker is pointless — another worker has already claimed the work. Catching it in a step's catch handler would mask the real problem and potentially corrupt state. Treating it like `ErrorTypeFatal` (non-retryable, non-catchable) is the only safe behavior.

---

## What Was Deferred

| Item | Reason | When to Revisit |
|------|--------|-----------------|
| `OverrideStepStatus` | Deprecated-on-arrival is a design smell. A first-class Wait step type is the right solution. | When Wait/Pause step type is designed. |
| `ValidateNames(activityNames ...string)` | Nice-to-have for YAML-first workflows. Low urgency. | When YAML-defined workflows need validation without Go activity structs. |
| `FollowUpSpec.Delay` | Scheduling semantics belong to the consumer's outbox, not the library. | If multiple consumers independently implement delay in `Metadata`. |
| Change `Run()`/`Resume()` to return `*ExecutionResult` | Breaking change. Wait to see adoption patterns of `Execute()`/`ExecuteOrResume()`. | After Phase 1 ships and real consumers adopt it. |

---

## Implementation Order

Within each phase, implement in the order listed. Each item should be a separate, reviewable commit.

**Phase 1** (no dependencies, start immediately):
1. `GetEndTime()` accessor on `ExecutionState`
2. `ErrNoCheckpoint` sentinel + update `loadCheckpoint`
3. `Resume()` bug fix (reorder `start()` / `loadCheckpoint()`)
4. `RunOrResume()` method
5. `ExecutionResult`, `ExecutionTiming` types
6. `Execute()`, `ExecuteOrResume()`, `buildResult()`

**Phase 2** (depends on Phase 1 for `Execute` return type):
1. `workflowtest` package with `MemoryCheckpointer`
2. `Validate()` + `ValidationError`
3. `MockActivity`, `MockActivityError`
4. `Run`, `RunWithOptions`, `TestOptions`

**Phase 3** (depends on Phase 1; independent of Phase 2):
1. `StepProgress`, `StepStatus`, `StepProgressStore` types
2. `stepProgressTracker` (internal) + wire into `NewExecution` with async dispatch
3. `ProgressReporter` interface + `ReportProgress` package-level func
4. `ErrFenceViolation` + update `MatchesErrorType` to bypass retry/catch
5. `WithFencing`, `FenceFunc`

**Phase 4** (depends on Phases 1 + 3):
1. `CompletionHook`, `FollowUpSpec` types
2. Add `FollowUps` field to `ExecutionResult`
3. `Runner`, `RunnerConfig`, `RunOptions`, `HeartbeatConfig`
4. `Runner.Run()` with heartbeat, timeout, hook orchestration
