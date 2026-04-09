# PRD: Reduce Consumer Boilerplate

_Status: Draft_
_Created: 2026-04-08_
_Author: Curtis / Claude_

> **Note:** Code snippets and API signatures in this document are illustrative — they explore
> ideas, not prescribe implementations. When planning the actual implementation, the best design
> should be evaluated independently of these sketches.

---

## Problem Statement

The `github.com/deepnoodle-ai/workflow` library provides a strong execution engine — steps,
activities, checkpoints, retries, path branching — and the core abstractions are working well.
As we integrate it into production services, we're seeing recurring patterns in consumer code
that would be better served by the library itself.

In one internal integration, we wrote ~700 lines of code across 6 files for concerns like
heartbeating, step progress tracking, resume-or-run fallback, and structured result handling.
Much of this isn't application-specific — it's the kind of scaffolding any production consumer
would write. There are also a couple of small correctness issues (`Resume()` state management,
string-based error matching) that are easy to work around but would be cleaner as library-level
fixes.

Pulling these patterns into the library reduces boilerplate for existing consumers and makes the
library more immediately useful to new ones.

---

## Use Cases

### UC1: Run a workflow in production with crash recovery

A developer building a background job processor wants to execute workflows that survive process
crashes. When a worker picks up a workflow that was previously attempted (attempt > 1), the
system should resume from the last checkpoint if one exists, or start fresh if not — without
the developer writing branching logic or working around library bugs.

**Currently:** The developer writes a 30-line block that tries `Resume()`, string-matches the
error to detect missing checkpoints, creates a second `Execution` object (because the first is
tainted by `Resume()`'s premature state change), and falls back to `Run()`. This pattern is
fragile and every consumer must rediscover it.

**Desired outcome:** A single `RunOrResume()` call that does the right thing. The developer
doesn't think about checkpoint existence or execution object lifecycle.

### UC2: Track which step is currently executing

A developer building a workflow dashboard wants to show users which step their workflow is on,
how long each step took, and whether any failed. The library fires activity callbacks, but the
developer must manually derive step status transitions (pending -> running -> completed/failed),
handle retry deduplication, and persist the progress state.

**Currently:** One internal integration implements a 170-line `StepProgressCallbacks` struct
that listens to `BeforeActivityExecution` / `AfterActivityExecution`, manually tracks state
transitions, persists to a database, and supports status overrides. Every consumer with a UI
will rebuild this.

**Desired outcome:** The library tracks step progress internally and calls a simple
`StepProgressStore` interface that the consumer implements to persist to their backend. The
consumer writes ~15 lines instead of ~170.

### UC3: Report progress within a long-running step

A developer has a step like "scrape 50 URLs" or "analyze 12 findings" that runs for minutes.
Users see the step as "running" with no indication of progress. The developer wants activities
to report intra-step progress (e.g., "Processing 3 of 12 items") without building custom
plumbing.

**Currently:** No mechanism exists. Developers either leave long steps opaque or build ad-hoc
progress reporting outside the workflow engine.

**Desired outcome:** Activities call `ctx.ReportProgress("Processing 3 of 12 items")` and the
detail flows through the same step progress mechanism to the consumer's store.

### UC4: Heartbeat to prove liveness in distributed deployments

A developer running workflows on distributed workers needs to prove the worker is alive. If a
worker crashes, a reaper process reclaims the workflow and assigns it to another worker. The
heartbeat must cancel the workflow if the lease is lost (another worker claimed it).

**Currently:** The developer spawns a goroutine that calls a heartbeat function on an interval,
wires its cancellation to the execution context, and coordinates shutdown before status
finalization. ~50 lines of concurrency management per consumer.

**Desired outcome:** The developer passes a `HeartbeatFunc` and interval to a `Runner`. The
runner manages the goroutine lifecycle, cancels the execution on lease loss, and cleans up on
completion.

### UC5: Get structured results after execution

A developer wants to handle workflow completion in a clean way: extract outputs, classify errors
(timeout vs. activity failure vs. infrastructure failure), and record timing — without scattered
post-execution calls.

**Currently:** `Run()` returns only `error`. The developer separately calls `GetOutputs()`,
classifies the error by type (checking `context.DeadlineExceeded`, etc.), and manually records
timing. The error/success branching is ~20 lines of scattered logic.

**Desired outcome:** Execution returns an `*ExecutionResult` with status, outputs, typed error,
and timing. A single `result.Completed()` check replaces the scattered classification.

### UC6: Validate workflows at registration time

A developer registers workflow definitions at application startup. If a workflow references an
undefined step, uses a nonexistent activity name, or has unreachable steps, the error only
surfaces at runtime — potentially at 2am in production, minutes into a long execution.

**Currently:** The library validates edge references during `New()` but doesn't check
reachability, activity existence, or join configuration validity. Runtime failures deep in
execution are the first signal.

**Desired outcome:** `wf.Validate(activities...)` catches structural problems eagerly. Workflows
fail at deploy time, not at runtime.

### UC7: Test workflows without ceremony

A developer writing unit tests for a workflow wants to verify the control flow, branching logic,
and output extraction. Setting up a checkpointer, logger, callbacks, and execution options for a
simple test is heavyweight.

**Currently:** Even a basic test requires constructing `ExecutionOptions` with a null
checkpointer, discarding logger, and full activity list. Tests are verbose and discourage
coverage.

**Desired outcome:** `workflow.TestRun(t, wf, activities, inputs)` runs a workflow with sensible
defaults in one line.

### UC8: Chain workflows after completion

A developer wants parent workflows to trigger child workflows on completion — for example, a
research workflow triggers a report generation workflow with the research outputs as inputs. The
chaining should be durable (survive crashes) and async (the parent doesn't block).

**Currently:** One internal integration implements a 150-line trigger outbox pattern: custom
`OnCompleteFunc` type, `writeTriggers()`, background processor, deduplication, retry with
backoff. The existing `ChildWorkflowExecutor` is synchronous and tightly coupled.

**Desired outcome:** The library provides a `CompletionHook` that returns `FollowUpSpec`
descriptors. The consumer persists these to their own durable outbox. The hook is a standard
contract; the outbox remains consumer-owned.

---

## Proposed Solution

The solution is organized into four phases, each building on the previous.

**Design constraint: no breaking changes to existing APIs by default.** New functionality should
be additive — new methods, new types, new interfaces — rather than changing the signatures or
behavior of existing APIs like `Run()`, `Resume()`, or `Checkpointer`. If we decide a breaking
change is warranted (e.g., changing `Run()` to return `*ExecutionResult`), that's an explicit
decision, not something we drift into.

### Phase 1: Foundation

Fix correctness bugs and establish the building blocks that everything else depends on.

**Resume() bug fix and ErrNoCheckpoint sentinel.** `Resume()` currently calls `start()` before
validating the checkpoint, tainting the execution object on failure. The fix: load and validate
the checkpoint before calling `start()`. Add `ErrNoCheckpoint` as a sentinel error so consumers
use `errors.Is()` instead of string matching.

**RunOrResume method.** A single entry point for workers with crash recovery:

```go
func (e *Execution) RunOrResume(ctx context.Context, priorExecutionID string) error
```

Internally, this loads the checkpoint, falls back to `Run()` if none exists, or applies the
checkpoint and resumes. This requires extracting `resumeFromCheckpoint()` from the current
`Resume()` internals — a cleaner internal factoring regardless.

**Structured ExecutionResult.** Add an `Execute()` method that returns `(*ExecutionResult, error)`
alongside the existing `Run()` / `Resume()` methods (which continue to return `error`). This
keeps backward compatibility while giving consumers a richer return type when they want it:

- `error` means infrastructure failure (can't load checkpoint, invalid workflow). Result is nil.
- `*ExecutionResult` contains status, outputs, typed `*WorkflowError`, and timing. Always
  non-nil when error is nil.

```go
type ExecutionResult struct {
    Status  ExecutionStatus
    Outputs map[string]any
    Error   *WorkflowError
    Timing  ExecutionTiming
}
```

### Phase 2: Developer Experience

**Workflow validation.** Add `Validate(activities ...Activity) error` to `Workflow`. Checks: all
steps reachable from start, no undefined activity references, join configurations reference
valid paths, no orphan steps. Returns a `*ValidationError` with per-problem details. Called
automatically by `NewExecution`, available standalone for registration-time validation.

**Testing utilities.** Three helpers:

- `TestRun(t, wf, activities, inputs)` — runs a workflow with in-memory checkpointing and
  sensible defaults
- `MemoryCheckpointer` — exported in-memory checkpointer for tests
- `MockActivity(name, result)` and `MockActivityFunc(name, fn)` — stub activities for testing
  control flow

### Phase 3: Production Patterns

**Step progress tracking.** The library internally tracks step state transitions
(pending -> running -> completed/failed) and exposes them through a `StepProgressStore`
interface:

```go
type StepProgressStore interface {
    UpdateProgress(ctx context.Context, executionID string, progress *StepProgress) error
}
```

Consumers implement this interface to write to their backend (~15 lines). The library owns the
derivation logic that consumers currently build themselves (~170 lines).

Intra-activity progress reporting via `ctx.ReportProgress(detail)` on the workflow `Context`
interface. The detail string flows through `StepProgressStore.UpdateProgress()`.

An `OverrideStepStatus` escape hatch on `Execution` for patterns like review pauses. Documented
as temporary — a first-class Wait step type should replace it.

**Lease-fenced checkpointer wrapper.** A utility function:

```go
func WithFencing(inner Checkpointer, fenceCheck func(ctx context.Context) error) Checkpointer
```

Wraps any `Checkpointer` with a pre-save validation check. The fenceCheck validates the worker
still holds the lease. This establishes a documented, tested pattern for distributed
checkpointing.

### Phase 4: Composition

**Execution Runner.** A reusable runner that manages the full execution lifecycle:

```go
type Runner struct { ... }

func NewRunner(cfg RunnerConfig) *Runner

func (r *Runner) Execute(
    ctx context.Context, wf *Workflow, activities []Activity, opts RunOptions,
) (*ExecutionResult, error)
```

`RunnerConfig` holds reusable settings (heartbeat interval, run timeout, logger). `RunOptions`
holds per-execution concerns (execution ID, attempt number, checkpointer, callbacks, step
progress store, heartbeat function, completion hook).

The Runner internally composes: heartbeat goroutine management, RunOrResume, step progress
tracking, structured results, and completion hooks. It is the recommended entry point for
production consumers.

**Completion hooks.** A `CompletionHook` function type in `RunOptions` that returns
`[]FollowUpSpec` after successful execution. Follow-up specs are included in the
`ExecutionResult` for the consumer to persist and process asynchronously. The library provides
the hook point and descriptor format; the outbox/processing remains consumer-owned.

---

## Edge Cases and Error States

- **RunOrResume with corrupted checkpoint:** If checkpoint data is present but fails to
  deserialize, return an infrastructure error (not silently fall back to fresh run). The
  consumer needs to know their checkpoint store has a problem.
- **Heartbeat fails during activity execution:** The runner cancels the execution context. The
  activity receives context cancellation and should abort. The checkpoint from the last completed
  step survives for the next worker to resume from.
- **StepProgressStore.UpdateProgress fails:** Log the error but don't fail the workflow. Progress
  tracking is observability, not correctness. The workflow should continue executing.
- **CompletionHook returns error:** The workflow result is still "completed" (the work was done).
  The hook error is surfaced separately so the consumer can retry follow-up creation without
  re-executing the workflow.
- **Validate() with no activities provided:** Only run structural checks (reachability, edge
  validity, join config). Skip activity name validation. This supports early validation before
  activities are available.
- **Concurrent resume of the same execution:** The fenced checkpointer prevents stale workers
  from overwriting state. Only the worker holding the current lease can checkpoint. Stale workers
  receive a fence error and should abort.
- **OverrideStepStatus called for nonexistent step:** No-op with a warning log. Don't panic or
  error — this is a best-effort escape hatch.

---

## Scope Boundaries

**In scope:**

- Resume() bug fix and ErrNoCheckpoint sentinel
- RunOrResume method on Execution
- Structured ExecutionResult return type (additive — new method, not changing existing signatures)
- Workflow.Validate() with structural and activity checks
- TestRun, MemoryCheckpointer, MockActivity test helpers
- StepProgressStore interface and internal step progress tracker
- Intra-activity progress via ctx.ReportProgress()
- OverrideStepStatus escape hatch
- WithFencing checkpointer wrapper
- Runner with heartbeat, timeout, and lifecycle management
- CompletionHook and FollowUpSpec types

**Out of scope:**

- Application-specific concerns (billing, webhooks, dead-letter queues, metrics)
- Durable outbox implementation for workflow chaining (consumer-owned)
- First-class Wait/Pause step type (future work, OverrideStepStatus bridges the gap)
- Workflow versioning or migration

---

## Success Metrics

**Primary — Integration boilerplate reduction:**

- Baseline: One internal integration requires ~700 lines across 6 files
- Target: <350 lines after adopting all four phases
- Measured by: line count of consumer integration layer after migration

**Primary — Time to production integration:**

- Baseline: Building robust integration patterns required multiple iterations internally
- Target: A new consumer can go from "workflow defined" to "running in production with crash
  recovery, progress tracking, and heartbeating" in under a day
- Measured by: time to integrate for the next production consumer

**Secondary — Bug surface area:**

- Baseline: String-matching error checks, double-Execution workarounds, manual state
  derivation — all sources of subtle bugs
- Target: Zero workarounds required for core library operations
- Measured by: absence of string-matching error checks and object-recreation workarounds in
  consumer code

**Secondary — Test coverage encouragement:**

- Target: TestRun helper used in >80% of workflow test files across consumers
- Measured by: grep for TestRun usage in consumer test suites

---

## Open Questions and Risks

**Open questions:**

- Should we eventually change `Run()`/`Resume()` signatures to return `(*ExecutionResult, error)`
  as a breaking change? The default approach is additive (new `Execute()` method), but if we're
  confident in the consumer base we could do a clean break. Defer until we see adoption patterns.

- What is the right default for `OverrideStepStatus` — escape hatch now, or defer until a proper
  Wait step type is designed? Shipping it establishes an API that may be hard to evolve. But not
  shipping it means consumers can't migrate their review-pause patterns.

- Should the Runner be opinionated about checkpoint cleanup on success? Deleting checkpoints
  after successful completion saves storage but loses forensic data. A TTL-based approach may be
  better but adds complexity.

**Risks:**

- **StepProgressStore performance.** Calling `UpdateProgress` after every step transition adds
  latency to the critical path. If the store is a remote database, this could meaningfully slow
  execution. Mitigation: make the call async (fire-and-forget with error logging) or batch
  updates.

- **Runner abstraction level.** If the Runner is too opinionated, consumers with unusual needs
  will bypass it and lose the benefits. If it's too flexible, it becomes configuration soup.
  Mitigation: keep RunnerConfig minimal (3-4 fields) and let RunOptions carry per-execution
  customization.

---

## Dependencies

**Internal:**

- Existing `Checkpointer` interface — extended by `WithFencing`, not replaced
- Existing `ExecutionCallbacks` interface — step progress tracker composes via `CallbackChain`
- Existing `WorkflowError` type — used in `ExecutionResult` for typed error classification
- `context.Context` plumbing in activities — needed for `ReportProgress` on workflow `Context`

**External:**

- None. All proposed changes are library-internal. Consumers opt in by adopting new APIs.

---

## Implementation Priority

| Phase | Proposals | Rationale |
|-------|-----------|-----------|
| 1. Foundation | Resume fix, ErrNoCheckpoint, RunOrResume, ExecutionResult | Correctness fixes unblock everything. Small, high impact. |
| 2. DX | Validate(), TestRun, MemoryCheckpointer, MockActivity | Makes the library approachable. Low risk, immediate value. |
| 3. Production | StepProgressStore, ReportProgress, WithFencing, OverrideStepStatus | Biggest boilerplate win. Careful interface design needed. |
| 4. Composition | Runner, CompletionHook, FollowUpSpec | Highest leverage but depends on Phases 1-3. |

Phases 1 and 2 can ship independently and quickly. Phase 3 benefits from real-world feedback on
the Phase 1 APIs. Phase 4 composes everything and should be designed after the pieces are proven.
