# RFC Review: Dynamic Steps Via Execution-Local Overlay

Reviewers: Curtis + peer review
Date: 2026-04-12
Status: Review complete, revisions needed before implementation

---

## Verdict

The overlay architecture is sound. The emission and persistence semantics
need rework. The v1 scope should be cut significantly. Whether to do this
at all depends on whether the goal is operator-visible dynamic subgraphs
or just "agents can pick different actions" — the latter is already
achievable with existing primitives.

---

## What's Good

1. **Immutable Workflow + execution-local overlay is the right model.**
   Consistent with how Workflow is built and validated today. Avoids
   cross-execution contamination, preserves static validation, and keeps
   dynamic behavior scoped to one execution.

2. **String step identity fits naturally.** BranchState.CurrentStep,
   JoinState, callbacks, progress tracking, and activity logs are all
   keyed off string step names already. Dynamic steps slot in without a
   type-system change.

3. **Side-capability pattern for EmitDynamicPlan.** Type-asserting an
   internal interface rather than extending the frozen Context interface
   follows the existing repo convention (ProgressReporter). Good
   discipline.

4. **Step naming scheme.** `dyn/{branch}/{counter}/{local_name}` gives
   collision avoidance, debuggability, and checkpoint stability. The
   execution-scoped counter avoids cross-branch collisions.

5. **Wait-unwind restriction is honest.** Identifies the concrete
   problem (non-deterministic re-emission in append-only overlay) and
   rejects the combination in v1 rather than handwaving. The future
   idempotent replay sketch is credible.

---

## High-Severity Findings

### H1: Staging semantics conflict with the retry/catch checkpoint boundary

The RFC stages and persists the plan during `EmitDynamicPlan` and clears
it on non-success paths (RFC lines 151, 173, 289). But `executeActivity`
checkpoints every activity attempt before retry/catch resolution
(execution.go:1517, execution.go:1567). Retries happen afterwards in
branch.go:791, and catch routing happens afterwards in branch.go:440.

A failed attempt can durably write a pending dynamic plan before the
engine knows whether the step will retry, catch, or succeed. This leaves
stale plans in checkpoints.

**Recommendation:** Do not stage during `EmitDynamicPlan`. Instead,
buffer the plan on the activity context only. `executeActivity` should
validate, rewrite names, and register the plan into the overlay only if
the activity returns success, immediately before the existing checkpoint.
On error or wait-unwind, drop the buffer. This preserves crash-window
durability without persisting plans from failed attempts.

### H2: Failed-execution resume semantics are underspecified

The RFC says resume stays simple (RFC line 380), but today
`loadCheckpoint` actively resets failed branches for resumption
(execution.go:415), and restart-point selection walks static workflow
steps only (execution.go:1350).

If a branch failed on a dynamic step, or if it restarts from an earlier
emitting step, the append-only overlay retains orphaned prior dynamic
steps with no stated rule for reuse, invalidation, or duplication.

**Recommendation:** Add an explicit section on failed-resume semantics:
- What survives across `ResumeFrom` after `ExecutionStatusFailed`?
- Can an emitting step re-emit on retry/resume? If so, do old dynamic
  steps get a new counter prefix, or are they reused?
- How does restart-point selection work when the restart target is a
  dynamic step vs. a static step that previously emitted dynamic steps?

---

## Medium-Severity Findings

### M1: PendingDynamicPlan carries duplicated state

The RFC puts full step definitions in both the execution overlay
(`dynamicSteps`) and on BranchState (`PendingDynamicPlan`). BranchState
is already the per-branch checkpoint surface (execution_state.go:13).
Persisting full Step definitions there duplicates the source of truth,
inflates checkpoints, and creates mismatch risk.

**Recommendation:** BranchState should persist only the rewritten pending
next edges (step names + branch names) and the emitting step identity —
not full step definitions. The overlay map is the single source of truth
for step definitions.

### M2: v1 scope is too broad

The RFC says v1 should allow dynamic activity, wait, sleep, pause,
fan-out, and count-based joins (RFC lines 351, 447). In the actual
engine, these are not "just step kinds" — each has bespoke
execution/resume behavior in branch.go. The step model also carries
modifiers like Each, Retry, and Catch.

**Recommendation:** Narrow v1 aggressively:
- Dynamic activity steps only
- Dynamic Next edges (unconditional and conditional) only
- No dynamic wait, sleep, pause, join, Each, nested emission, or
  dynamic catch/retry

This is still useful for the AI agent use case. An agent emits a
subgraph of activity steps that the engine executes with full
checkpointing, retry, and observability. Wait/sleep/pause in dynamic
steps can follow once the core overlay plumbing is proven.

### M3: dynamicStepCounter concurrency is under-specified

`dynamicStepCounter` lives on `executionState` (mutex-protected), and
`EmitDynamicPlan` is called from activity goroutines. Two branches could
theoretically call `EmitDynamicPlan` concurrently.

**Recommendation:** State explicitly: staging acquires the executionState
mutex, increments the counter once per plan (not per step), and registers
all steps atomically under that lock.

### M4: Resolution order needs a guard against `dyn/` static names

Dynamic-first resolution means the overlay always wins on name
collision. The `dyn/` prefix makes accidental collision unlikely, but
nothing prevents a static step named `dyn/foo`.

**Recommendation:** Add a validation rule in `Workflow.Validate()` that
static step names may not start with `dyn/`.

---

## Low-Severity / Clarification Items

### L1: DynamicPlan.Next semantics are ambiguous

It's unclear whether `DynamicNext` entries are the outgoing edges from
the emitting step into the dynamic subgraph, or the full edge set. The
success path description implies they replace `Step.Next`.

**Recommendation:** State explicitly: "`DynamicPlan.Next` replaces the
emitting step's static `Step.Next` edges when consumed. Each entry
specifies which dynamic step to enter and optionally names the branch,
like a static Edge." Clarify whether `DynamicNext.Step` can point
directly to a static workflow step.

### L2: No explicit "reconnect to static graph" documentation

The checkpoint example shows a dynamic step with
`next: [{ step: "finalize" }]` where `finalize` is static. This is the
critical seam between dynamic and static subgraphs. The validation
section mentions it indirectly, but it deserves a named subsection.

### L3: Terminal dynamic steps are unspecified

A static step with no Next is terminal — the branch completes. Is this
also true for dynamic steps? Almost certainly yes, but state it.

### L4: Checkpoint schema version bump is unspecified

Currently `CheckpointSchemaVersion = 1`. The RFC says "requires a
checkpoint schema bump" but doesn't specify v2. Note that v1 checkpoints
load fine (zero-value DynamicSteps), but v2 checkpoints cannot load in
v1 code. The existing forward-compatibility contract handles this.

### L5: Each modifier on dynamic steps

The RFC lists supported dynamic step kinds but doesn't mention the
`Each` modifier. If dynamic activity steps support `Each`, the naming
scheme needs to handle sub-branch naming. If not, defer it explicitly.

### L6: "Phase two" has no "phase one"

The RFC is scoped as "phase two" but doesn't say what phase one is. If
phase one is "agents do everything in one activity," state it so readers
understand the progression.

---

## Should We Do This At All?

**Do it only if the goal is first-class, operator-visible dynamic
subgraphs with per-step checkpointing, retry, and observability.**

The library already supports durable agent loops through `Context.Wait`
and `Context.History`. For many AI agent workflows, a simpler pattern
works today without any engine changes:

1. Agent activity calls LLM, gets back a plan
2. Agent stores the plan as data in branch state
3. A static "executor" step reads the plan and dispatches sub-tasks
   within a single activity, using `History.RecordOrReplay` for
   idempotency

This gives you durability and replay safety. You lose per-sub-step
observability, independent retry, and concurrent fan-out of agent
sub-tasks.

If those properties matter — and for long-running agent workflows with
expensive sub-steps they will — then the overlay RFC is the right
direction. But build the narrowest possible first cut (activity steps +
next edges only), prove the checkpoint/resume semantics end-to-end, and
expand the step kind support incrementally.

---

## Recommended Revision Checklist

- [x] Rework emission to buffer-then-register-on-success (H1)
- [x] Add failed-resume semantics section (H2)
- [x] Slim PendingDynamicPlan to edges + step identity only (M1)
- [x] Narrow v1 to dynamic activity steps and next edges only (M2)
- [x] Specify concurrency model for staging (M3)
- [x] Add `dyn/` prefix guard on static step names (M4)
- [x] Clarify DynamicPlan.Next replacement semantics (L1)
- [x] Add "reconnecting to static graph" subsection (L2)
- [x] State terminal dynamic step behavior (L3)
- [x] Specify checkpoint schema version 2 (L4)
- [x] Explicitly defer Each on dynamic steps (L5)
- [x] Define what "phase one" is (L6)
- [x] Specify crash-recovery bypass for pending next edges (R2-F1)
- [x] Add branch-name validation for DynamicNext entries (R2-F2)
- [x] Define precise commit point relative to activity logging (R2-R1)
- [x] Add rollback semantics on commit/checkpoint failure (R2-R1)
