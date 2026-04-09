# Combined Review: Reduce Consumer Boilerplate Spec

_Consolidated from three independent reviews on 2026-04-08_

---

## Verdict

Phase 1 (Foundation) is the strongest part of the proposal and should ship first. The Resume
bug fix, `ErrNoCheckpoint` sentinel, and `RunOrResume` are clear, high-value wins aligned with
the current engine. Beyond Phase 1, several design issues need resolution before implementation.

---

## Critical: Must Fix Before Implementation

### 1. `Context.ReportProgress` is a breaking interface change

**Spec ref:** Phase 3, section 3.2 (context.go)
**Reviewers:** all three

Adding `ReportProgress(detail string)` to the exported `Context` interface breaks every
external type that implements it. This directly violates the spec's own first design principle:
"Additive, not breaking."

**Recommended fix:** Use an optional side interface with a package-level helper:

```go
type ProgressReporter interface {
    ReportProgress(detail string)
}

func ReportProgress(ctx Context, detail string) {
    if pr, ok := ctx.(ProgressReporter); ok {
        pr.ReportProgress(detail)
    }
}
```

Activities call `workflow.ReportProgress(ctx, "3 of 12")`. This follows the
`io.WriterTo` / `http.Flusher` pattern from stdlib.

### 2. Heartbeat does not actually cancel the execution

**Spec ref:** Phase 4, `startHeartbeat` implementation
**Reviewers:** all three

`startHeartbeat` creates `hbCtx, hbCancel := context.WithCancel(ctx)`. On heartbeat failure it
calls `hbCancel()`, which cancels only the heartbeat goroutine's child context — not the
execution context. The workflow keeps running.

**Recommended fix:** `Runner.Execute` must create the cancellable context and pass that cancel
func into startHeartbeat:

```go
execCtx, execCancel := context.WithCancel(ctx)
defer execCancel()
stopHeartbeat := r.startHeartbeat(execCtx, execCancel, opts.Heartbeat)
```

### 3. `StepProgress` model is too coarse for path-aware execution

**Spec ref:** Phase 3, section 3.1
**Reviewers:** two of three

The proposal tracks progress by `StepName` only and the example persists on
`(job_id, step_name)`. But the engine is path-aware: callbacks already carry `PathID`, steps
can branch and run concurrently, and `Each` loops execute the same step in parallel. As
written, progress for parallel branches, joins, and Each work will overwrite itself.

**Recommended fix:** Include `PathID` in `StepProgress` and use `(execution_id, step_name,
path_id)` as the compound key. This aligns with the existing callback model.

### 4. `ExecutionResult` error contract doesn't match actual engine failure modes

**Spec ref:** Phase 1, section 1.4-1.5
**Reviewers:** two of three

The spec says `error` = infra failure, `result.Error` = workflow failure. But some
infrastructure failures (checkpoint save, activity logging) happen *after* execution has
started. Under `buildResult`, these get reclassified as workflow failures because
`status != ExecutionStatusPending`. This is misleading.

Additionally, `Timing.FinishedAt` is set via `time.Now()` rather than the execution state's
actual finish time, which may diverge if there's post-execution processing.

**Recommended fix:**
- Use a more explicit signal than status-sniffing to distinguish "never ran" from
  "ran but hit infra error." An explicit `started` bool, or tracking error origin, would be
  more robust.
- Capture `FinishedAt` from execution state where possible.
- Consider adding `WorkflowName` to `ExecutionResult` — helpful when processing results in
  bulk or logging.

---

## Design: Rethink Before Implementation

### 5. `FollowUps` on `ExecutionResult` creates a forward dependency

**Spec ref:** Phase 1, section 1.4

The Phase 1 `ExecutionResult` struct includes `FollowUps []FollowUpSpec`, but `FollowUpSpec`
doesn't exist until Phase 4. This means Phase 1 can't actually ship independently as the
phasing promises. Define `ExecutionResult` without `FollowUps` in Phase 1; add the field in
Phase 4.

### 6. `Runner` + `RunOptions` duplicates the existing API surface

**Spec ref:** Phase 4, section 4.1
**Reviewers:** two of three

`RunOptions` (10 fields) largely mirrors `ExecutionOptions`, creating a mapping layer that
consumers must learn alongside the existing one. This risks the "configuration soup" the PRD
warned about.

**Options to consider:**
- Have the Runner accept an `*Execution` instead of creating one internally:
  `func (r *Runner) Run(ctx context.Context, exec *Execution, opts RunOptions) (*ExecutionResult, error)`.
  This eliminates field duplication and shrinks `RunOptions` to just lifecycle concerns
  (PriorExecutionID, Heartbeat, CompletionHook, Timeout).
- Alternatively, ship small composable helpers around `ExecutionOptions` and callbacks before
  introducing a new "recommended" orchestration object.

### 7. `Runner.Execute` vs `Execution.Execute` name collision

**Spec ref:** Phase 1 + Phase 4

Both types have an `Execute` method with completely different signatures and semantics. If
the Runner accepts an `*Execution` (see #6), this resolves naturally — call the Runner method
`Run`. If not, one of them should be renamed to avoid confusion.

### 8. `OverrideStepStatus` — deprecated on arrival, inaccessible through Runner

**Spec ref:** Phase 3, section 3.3

Two problems:
- Shipping something pre-deprecated signals "we know this is wrong." Either commit to the
  escape hatch or defer until the proper Wait step type is designed. A deprecated-from-day-one
  API will calcify.
- The Runner creates the Execution internally and never exposes it, so consumers using the
  Runner (the "recommended entry point") can't call `OverrideStepStatus`. Design conflict.

**Recommendation:** Defer this entirely. Consumers already have their workaround. Ship the real
solution (Wait step) when ready.

### 9. `CompletionHook` / `FollowUpSpec` duplicates existing child workflow vocabulary

**Spec ref:** Phase 4, section 4.2

The codebase already has `ChildWorkflowSpec`, `ChildWorkflowExecutor`, and async handles.
Adding `FollowUpSpec` creates a second vocabulary for workflow chaining, making the package
less cohesive.

**Recommendation:** Either reuse `ChildWorkflowSpec` or explicitly refactor the child-workflow
API first. If `FollowUpSpec` is intentionally different (descriptor vs. execution request),
document why the distinction exists and whether `ChildWorkflowSpec` should eventually converge
with it.

### 10. `WithFencing` is under-specified for retries and catch handlers

**Spec ref:** Phase 3, section 3.4

A fence-check failure returns from `SaveCheckpoint`, but checkpoint errors bubble up through
step execution and are fed into retry matching. Without special classification, a lease-loss
error can be retried or caught like a normal activity failure — defeating the point of fencing.

Also, the wrapping logic checks `errors.Is(err, ErrFenceViolation)` on the consumer's
`FenceFunc` return. The consumer shouldn't need to know about library sentinels. Always wrap:

```go
if err := f.fenceCheck(ctx); err != nil {
    return fmt.Errorf("%w: %w", ErrFenceViolation, err)
}
```

**Additionally:** Specify that `ErrFenceViolation` must bypass retry/catch logic. The engine
should treat it as non-retryable and non-catchable, similar to `ErrorTypeFatal`.

---

## Go Idioms

### 11. Test helpers should be in a `workflowtest` package, not behind a build tag

**Spec ref:** Phase 2, section 2.2
**Reviewers:** two of three

The `//go:build !release` approach is non-standard for Go libraries. It requires every
consumer's build system to know about it, and someone importing `workflow.MockActivity` in
production code won't get a compile error — only a mysterious build failure in release mode.

The stdlib pattern is a separate package: `workflowtest`. This is discoverable, conventional
(`net/http/httptest`, `io/iotest`), and provides clean separation.

### 12. `MockActivityFunc` adds no value

**Spec ref:** Phase 2, section 2.4

`MockActivityFunc(name, fn)` is literally `NewActivityFunction(name, fn)`. The "signals test
intent" justification is what a comment or package name (`workflowtest`) already provides.
Drop it — two mock helpers (`MockActivity`, `MockActivityError`) are sufficient.

### 13. `Validate` signature: variadic vs slice

**Spec ref:** Phase 2, section 2.1

`Validate(activities ...Activity)` is ergonomic for the zero-arg case but awkward when you
have a pre-existing slice: `wf.Validate(mySlice...)`. A `[]Activity` parameter with a nil
check is more consistent with how activities are passed elsewhere in the API. Minor, but
worth being intentional.

---

## Enhancements Worth Considering

### 14. `StepProgressStore` should be async by default

**Reviewers:** two of three

The spec acknowledges the latency risk ("a slow store will add latency to the execution
critical path") but leaves it to consumers. Since progress tracking is explicitly "observability,
not correctness," the internal tracker should call the store asynchronously (fire-and-forget
with error logging, or a small buffered channel) by default.

### 15. `StepProgress.Duration` is ambiguous for running steps

The spec says "for running steps, this is time since start" but doesn't specify when it's
computed. By the time the store persists it, it's already stale. Consider dropping `Duration`
from the struct — consumers can compute it from `StartedAt` + current time for running steps
and `FinishedAt - StartedAt` for completed ones.

### 16. `stepProgressTracker` initialization is unspecified

The tracker needs the full step list to emit initial "pending" states. The spec doesn't say
where it gets this, how it handles steps on branches that are never taken (stay "pending"
forever? become "skipped"?), or what happens with dynamically spawned paths from `Each`.

### 17. `ValidateNames` variant

Consider supporting `ValidateNames(activityNames ...string)` for cases where you want to
validate a YAML definition against a known list before instantiating Go activity structs.

### 18. `NullCheckpointer` reference is dangling

`RunOptions` docs say "Defaults to NullCheckpointer if not set" but this type isn't defined
in the spec or visible in the current codebase. Either define it or reference where it exists.

### 19. `FollowUpSpec.Delay` for chaining

Chaining workflows often requires a "wait N minutes before starting the next one" pattern.
This could be a first-class field rather than buried in `Metadata`. Worth considering, though
keeping it minimal initially is also defensible.

### 20. `TestRunWithOptions` silently overrides fields

`TestRunWithOptions` overwrites `Workflow`, `Activities`, and `Inputs` from the opts struct
without warning. Someone setting those fields on `ExecutionOptions` will be confused when
they're ignored. Better to take a smaller struct with only the overridable fields.

---

## Implementation Guidance

**Ship in this order, gating on review feedback:**

1. **Phase 1 minus `FollowUps` field** — Resume fix, ErrNoCheckpoint, RunOrResume. These are
   unambiguously good. Add `ExecutionResult`/`Execute` without `FollowUps`.
2. **Phase 2 in a `workflowtest` package** — MemoryCheckpointer, Validate, MockActivity,
   TestRun. Keep it small and practical.
3. **Phase 3 with fixes** — StepProgress with PathID, ReportProgress as optional interface,
   WithFencing with retry-bypass semantics. Defer OverrideStepStatus. Make store calls async.
4. **Phase 4 after real pressure** — Runner and CompletionHook are high-leverage but depend on
   getting Phases 1-3 right. Resolve the Runner-owns-Execution vs Runner-accepts-Execution
   question before implementing. Reconcile FollowUpSpec with ChildWorkflowSpec.
