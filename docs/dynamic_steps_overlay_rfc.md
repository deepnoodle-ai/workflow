# RFC: Dynamic Steps Via Execution-Local Overlay

Status: draft

## Summary

This RFC proposes dynamic step support that preserves the existing
immutable `Workflow` definition and adds an execution-local overlay of
synthetic steps.

Today (phase one), AI agents that need to decide on different actions
at runtime do all sub-work within a single activity: make LLM calls,
search calls, etc. internally, and surface results as a single step
output. This works but forfeits per-sub-step checkpointing,
observability, retry, and concurrent fan-out. This RFC is phase two:
giving agents a way to emit sub-step graphs that the engine executes
as first-class steps.

The core idea is:

- `Workflow` remains static and immutable.
- `Execution` gains an append-only map of dynamic steps.
- Runtime step lookup resolves against `dynamic overlay + static workflow`.
- Activities emit a dynamic plan through a helper API; the plan is
  buffered in memory during execution.
- The engine commits the plan into the overlay only if the activity
  returns success, immediately before the existing post-activity
  checkpoint.

This keeps the feature durable across checkpoint/resume without turning
`Workflow` into a mutable shared object.

## Why This Shape

The current engine already treats step identity as a string in the
places that matter for durability:

- `BranchState.CurrentStep` persists the current step name.
- `JoinState` is keyed by step name.
- Resume restores branches by resolving `CurrentStep` back to a step in
  the workflow.
- Callbacks, step progress, and activity logs all report string step
  names.

That means dynamic steps fit naturally if they also get stable string
names that survive checkpoint round-trips.

The overlay approach is simpler than mutating `Workflow` because it:

- avoids shared mutable workflow definitions across executions
- avoids invalidating static validation on the base workflow
- keeps dynamic behavior scoped to one execution
- lets checkpointing own the durable representation cleanly

## Goals

- Allow a running step to create one or more new steps for this
  execution only.
- Allow those new steps to execute next, either as a single path or as
  fan-out branches.
- Preserve behavior across checkpoint/resume.
- Keep the exported `Workflow` model immutable.
- Avoid changing frozen public signatures such as `Run`, `Resume`, and
  the core `Context` interface.

## Non-Goals

- Making `Workflow` mutable after construction.
- Supporting deletion or mutation of previously-created dynamic steps.
- Hot-swapping static workflow definitions underneath a running
  execution.
- Dynamic wait, sleep, pause, or join steps in v1.
- `Each`, `Retry`, or `Catch` modifiers on dynamic steps in v1.
- Nested dynamic emission (dynamic step emitting its own plan) in v1.
- Matching a fully general DAG builder API on day one.

## Proposed Model

### 1. Execution-local dynamic overlay

Add execution-scoped dynamic step storage:

```go
type executionState struct {
    // existing fields...
    dynamicSteps       map[string]*Step
    dynamicStepCounter int
}
```

Add the same data to `Checkpoint`:

```go
type Checkpoint struct {
    // existing fields...
    DynamicSteps       map[string]*Step `json:"dynamic_steps,omitempty"`
    DynamicStepCounter int              `json:"dynamic_step_counter,omitempty"`
}
```

This requires a checkpoint schema bump from v1 to v2.
`CheckpointSchemaVersion` becomes 2. Checkpoints written at v1 load
correctly (zero-value `DynamicSteps` is nil, counter is 0). Checkpoints
written at v2 cannot be loaded by v1 code — the existing
forward-compatibility check in `loadCheckpoint` rejects them.

The overlay is append-only:

- once a dynamic step is registered, it is immutable
- a dynamic step name is unique within the execution
- the base workflow is never modified

Concurrency: commit is serialized through the `executionState` mutex.
The counter is incremented once per plan (not per step), and all steps
in the plan are registered atomically under that lock. Two branches
committing concurrently will serialize correctly.

### 2. Step resolution goes through the execution

Introduce:

```go
func (e *Execution) resolveStep(name string) (*Step, bool)
```

Resolution order:

1. dynamic overlay
2. static workflow

All runtime lookups that currently call `workflow.GetStep(...)` should
switch to `execution.resolveStep(...)`.

This change is mechanical and localized. It preserves the existing
string-based `CurrentStep` persistence model.

### 3. Dynamic plan emission from activities

Do not extend the exported `Context` interface directly.

Follow the repo convention of additive side capabilities instead:

```go
type DynamicPlan struct {
    Steps []*Step
    Next  []*DynamicNext
}

type DynamicNext struct {
    Step       string
    BranchName string
}

func EmitDynamicPlan(ctx Context, plan *DynamicPlan) error
```

`EmitDynamicPlan` should type-assert an internal capability on the
engine's execution context. If the context does not support dynamic
steps, it returns a clear error.

`EmitDynamicPlan` does NOT stage or persist anything. It buffers the
plan on the activity context in memory only. The activity may call
`EmitDynamicPlan` at most once per invocation; a second call returns
an error.

`DynamicPlan.Steps` defines the dynamic steps themselves.
`DynamicPlan.Next` defines the entry points from the emitting step
into the dynamic subgraph — it replaces the emitting step's static
`Step.Next` edges when consumed. Each `DynamicNext` entry specifies
which step to enter (by plan-local name, rewritten at commit) and
optionally names the branch, exactly like a static `Edge`.
`DynamicNext.Step` may reference a plan-local step, a previously
registered dynamic step, or a static workflow step — the same
resolution rules apply.

This keeps the public `Context` interface stable while still exposing a
first-class feature to activities.

### 4. Activation model

Dynamic plans are two-phase:

1. buffering (during activity execution)
2. commit (after activity success, before checkpoint)

Buffering means:

- the plan is held in memory on the activity context
- no validation, name rewriting, or persistence happens yet
- the plan is invisible to the execution overlay and checkpoint

Commit means:

- only after the activity returns success
- `executeActivity` checks whether a buffered plan exists
- if so: validate the plan, rewrite step names to execution-unique
  names, register the steps in the execution overlay, record the
  rewritten next edges on the branch state
- then write the normal post-activity checkpoint (which now includes
  the registered dynamic steps and pending next edges)
- on any non-success path (error, retry, catch, wait-unwind), the
  buffer is dropped — nothing is persisted

This avoids having a plan from a failed or retried attempt leak into
the checkpoint. The existing retry and catch machinery in
`executeActivity` and `branch.Run` does not need to know about
dynamic plans at all — by the time those paths execute, the buffer
has already been discarded.

## Checkpoint Timing

### Chosen approach

Keep the current post-activity checkpoint. Commit the dynamic plan
into the execution overlay and branch state immediately before that
checkpoint, only on the success path.

The sequence becomes:

1. activity starts
2. activity calls `EmitDynamicPlan` — plan is buffered in memory only
3. activity returns
4. if the activity returned an error: discard the buffer, proceed to
   normal retry / catch / failure handling — no dynamic state is
   persisted
5. if the activity returned success:
   a. `executeActivity` fires AfterActivityExecution callback
   b. `executeActivity` logs the activity via `activityLogger`
   c. if logging fails: discard the buffer, return the log error —
      no dynamic state is committed
   d. `executeActivity` commits the buffered plan:
      - validate the plan (step names, activity references, edges)
      - rewrite local step names to execution-unique names
      - register the rewritten steps in `executionState.dynamicSteps`
      - record the rewritten next edges on the branch state
   e. if commit validation fails: roll back any steps registered in
      the overlay during this commit, clear pending next edges from
      the branch, return the validation error
   f. `executeActivity` calls `saveCheckpoint`
   g. if `saveCheckpoint` fails: roll back the overlay and branch
      state mutations from this commit, return the checkpoint error
6. branch logic consumes the pending next edges and advances

The commit point is after successful activity logging and before
`saveCheckpoint`. This placement matters because activity logging
happens before checkpointing in the current `executeActivity`, and a
logging failure returns an error that aborts the step. If the commit
happened before logging, a log failure would leave committed dynamic
state in the live execution that was never checkpointed — creating a
divergence between in-memory and durable state.

Rollback on commit or checkpoint failure means removing the
just-registered steps from `dynamicSteps` and clearing
`PendingDynamicNext` / `PendingDynamicNextStep` from the branch state.
The counter is NOT rolled back — wasting a counter value is harmless
and avoids needing to reason about concurrent counter state.

This is the simplest durable option because it does not require moving
the existing checkpoint boundary, and it guarantees that only
successful, logged, and checkpointed attempts persist dynamic state.

### Why buffer-then-commit instead of stage-on-emit

An earlier draft staged the plan into execution state during
`EmitDynamicPlan`, before the activity returned. This conflicts with
the current retry/catch checkpoint boundary: `executeActivity`
checkpoints every attempt before retry and catch resolution happen in
`branch.Run`. A failed attempt would durably write a pending plan
before the engine knew whether the step would retry, catch, or
succeed — leaving stale plans in checkpoints.

By buffering in memory and committing only on success, the dynamic
plan never reaches durable state unless the activity succeeds. Retry
and catch paths discard the buffer implicitly.

### Why not rely on activity return values alone

If the plan existed only in the activity's return value, it would live
in memory until `branch.Run` handles branching. A crash between the
activity return and branching would lose the plan.

By committing into execution state before the existing checkpoint, the
plan survives that crash window.

### Pending next-edge persistence

Add per-branch pending dynamic routing state:

```go
type BranchState struct {
    // existing fields...
    PendingDynamicNext     []*DynamicNext `json:"pending_dynamic_next,omitempty"`
    PendingDynamicNextStep string         `json:"pending_dynamic_next_step,omitempty"`
}
```

Note: BranchState carries only the rewritten next edges and the
emitting step identity — not full step definitions. The execution
overlay (`dynamicSteps`) is the single source of truth for step
definitions. This avoids duplicating step definitions across both
structures and keeps checkpoint size bounded.

Rules:

- the pending next edges are scoped to a single branch and step
- they are written by the commit phase of `executeActivity`
- they are consumed only when that same step's branch advances
- they are cleared when the branch advances past the step

This keeps replay behavior understandable.

## Step Naming

Dynamic steps need execution-local unique names.

The simplest durable rule is:

- caller supplies local step names within the emitted plan
- engine rewrites them to execution-unique names at staging time
- engine rewrites all intra-plan references to match
- references from the plan to static workflow steps are left unchanged

Example:

- activity emits local steps `gather`, `score`, `finish`
- engine stages them as:
  - `dyn/main/12/gather`
  - `dyn/main/12/score`
  - `dyn/main/12/finish`

Benefits:

- no collision with static workflow step names
- no collision across branches
- no extra identity type is needed in checkpoints

Tradeoff:

- logs and progress events will report synthetic step names

That is acceptable for the first cut. If operators later want a nicer
display label, it can be added as metadata without changing the durable
identity.

## Validation

### Static workflow validation

`Workflow.Validate()` gains one new rule: static step names must not
start with `dyn/`. This reserves the prefix for dynamic step names and
prevents accidental collisions with the overlay.

Dynamic steps do not participate in `Workflow.Validate()`. The base
workflow remains statically valid on its own.

### Dynamic plan validation (at commit time)

Add runtime validation for emitted plans, executed during the commit
phase of `executeActivity`:

- step names are unique within the plan before normalization
- all steps are activity-kind (v1 restriction)
- no `Each`, `Retry`, or `Catch` modifiers (v1 restriction)
- activity names resolve in the registry
- parameter templates compile
- edge conditions compile
- every target step in `Next` edges resolves against:
  - plan-local names (before rewriting)
  - previously-created dynamic steps in the overlay
  - static workflow steps

**Branch-name validation for `DynamicNext` entries:**

- `BranchName` must not be `"main"` (reserved)
- `BranchName` values must be unique within the emitted plan's `Next`
  entries — two entries must not claim the same branch name
- `BranchName` must not collide with any existing branch ID in the
  current execution state (checked at commit time against
  `executionState.branchStates`)
- `BranchName` must not collide with any statically-declared branch
  name from the workflow's edge definitions

These rules mirror what `Workflow.Validate()` and
`GenerateBranchID` enforce for static edges today, applied at
runtime to the dynamic plan.

This validator should reuse the same validation logic already used for
workflow construction and binding as much as possible.

### Reconnecting to the static graph

Dynamic steps reconnect to the static workflow by referencing static
step names in their `Next` edges. This is the intended exit path from
a dynamic subgraph.

Example: a dynamic step `dyn/main/3/summarize` with
`next: [{ step: "finalize" }]` routes back to the static `finalize`
step. The engine resolves `finalize` through `resolveStep`, which
checks the overlay first (no match) then the static workflow (match).

A dynamic step with no `Next` edges is a terminal step. When a branch
reaches a terminal dynamic step, the branch completes — same behavior
as a static terminal step.

## Runtime Behavior

### Success path

1. Activity calls `EmitDynamicPlan` — plan buffered in memory.
2. Activity returns success.
3. `executeActivity` commits the buffered plan: validate, rewrite
   names, register steps in overlay, record next edges on branch.
4. `executeActivity` writes its normal checkpoint.
5. Branch records normal step output.
6. Branch branching logic checks for pending dynamic next edges first.
7. If present, it creates branch specs from the pending edges.
8. Branch advances or fans out.
9. Pending next edges are cleared from branch state.

### Failure path

If the step returns an error:

- the buffered plan is discarded — it was never persisted
- normal retry / catch / failure behavior proceeds
- no dynamic state reaches the checkpoint

Because commit happens only on success, there is no stale dynamic
state to clean up on error paths. Retry and catch do not need to know
about dynamic plans at all.

### Wait-unwind path

**V1 restriction: steps that call `EmitDynamicPlan` must not unwind
into a wait.** The engine should return an error if an activity both
emits a dynamic plan and unwinds into `Wait` in the same invocation.

The problem is replay safety. If the step unwinds into a wait and
replays on resume, a non-deterministic activity (e.g., an AI agent
calling an LLM) may emit a different plan the second time. Under the
buffer-then-commit model, no steps from the first emission were
persisted (the activity didn't return success), but the second
emission could produce a different plan — and the engine has no way
to know which is "correct."

Requiring deterministic emission or `RecordOrReplay` wrappers is too
burdensome for the primary use case — AI agents whose output is
inherently non-deterministic. Rejecting the combination entirely in v1
is the simplest correct answer.

This restriction can be relaxed in a future version once replay
idempotency is proven (see below).

### Replay idempotency (future)

If the wait-unwind restriction is relaxed in a future version, the
engine should enforce idempotent re-emission:

- If the branch already has committed dynamic next edges for the
  current step when `EmitDynamicPlan` is called on replay, the engine
  returns the existing rewritten step names instead of committing a
  new plan.
- The overlay's append-only invariant is preserved — no steps are
  replaced or orphaned.
- The activity receives the same synthetic names it would have received
  on the original call, making downstream references stable.

This makes replay safe without requiring the activity itself to be
deterministic.

## Joins and Branch Names

The overlay model makes dynamic joins possible in the future because
join identity is already string-based. A dynamic join step can be
stored and resumed like any other dynamic step.

However, v1 does not support dynamic join steps. The first
implementation supports only dynamic activity steps (see Recommended
First Implementation below).

Future versions may add dynamic join, wait, sleep, and pause steps.
Recommended future limits for joins:

- allow join steps only when they use `Count`
- defer `JoinConfig.Branches` against future dynamic named branches

Reason:

- explicit named-branch joins require more runtime validation
- the current static validator assumes branch names are known from the
  workflow definition
- count-based joins avoid that extra machinery

This can be added later without changing the overlay model.

## Observability

Current observability surfaces can remain string-based:

- `BranchExecutionEvent.CurrentStep`
- `ActivityExecutionEvent.StepName`
- `StepProgress.StepName`
- `ActivityLogEntry.StepName`

No API change is required there. Dynamic step names will appear in the
same fields as static step names.

## Resume Semantics

### Normal resume (suspended execution)

Resume from a suspended execution is straightforward:

- load checkpoint
- restore `dynamicSteps`
- restore `dynamicStepCounter`
- restore branch `CurrentStep`
- resolve the current step through `execution.resolveStep`

No mutable workflow rebuild is required.

### Failed-execution resume

Today `loadCheckpoint` resets failed branches for resumption, and
restart-point selection walks static workflow steps. Dynamic overlays
add two new concerns: orphaned steps and re-emission.

**Restart on a dynamic step.** If a branch failed on a dynamic step
(e.g., `dyn/main/3/summarize`), it can resume from that step because
`resolveStep` finds it in the overlay. No special handling is needed
beyond what normal resume already does.

**Restart on an earlier emitting step.** If the execution restarts from
a static step that previously emitted dynamic steps, the overlay still
contains those steps from the prior run. On re-execution, the activity
may emit a new plan. The engine handles this by:

- assigning new counter-based names to the re-emitted plan
  (`dyn/main/5/gather` vs. the prior `dyn/main/3/gather`)
- the prior dynamic steps remain in the overlay but are unreachable —
  no branch's `CurrentStep` or `Next` edge points to them

Unreachable dynamic steps are inert: they consume a small amount of
checkpoint space but have no runtime effect. The engine does NOT
attempt to garbage-collect them — the append-only invariant is
preserved.

**V1 restriction: no overlay reset on resume.** The engine does not
clear or prune the dynamic overlay on resume. This is the simplest
correct behavior because:

- pruning requires knowing which dynamic steps are reachable, which
  requires a graph walk that doesn't exist today
- unreachable steps are harmless
- the counter ensures no naming collisions

A future version may add optional overlay compaction for long-running
executions with many restart cycles, but this is not needed for v1.

**Pending next edges on crashed branches.** If the process crashes
after the activity succeeded and checkpointed (with committed dynamic
state) but before branch logic consumed the pending next edges, the
branch resumes at `CurrentStep` with `PendingDynamicNextStep` set
to the same step.

The current engine resumes a branch by reconstructing it at
`CurrentStep` and re-running the step from the top of the run loop.
There is no existing "activity already succeeded, skip to branching"
path. Dynamic steps require one:

On branch start or resume, before executing the activity, check
whether `PendingDynamicNextStep == CurrentStep`. If so, the activity
already succeeded and committed its plan in a prior run — bypass
activity execution entirely and go straight to the branching phase,
which consumes the pending next edges normally.

This is a small addition to the branch run loop (a guard at the top
of the activity-execution path). Without it, the engine would
re-execute a completed activity and the activity might emit a
different plan, violating the append-only overlay invariant.

## Impacted Areas

Expected code touch points:

- `checkpoint.go` — add `DynamicSteps`, `DynamicStepCounter` fields;
  bump schema version to 2
- `execution_state.go` — add `dynamicSteps`, `dynamicStepCounter`;
  add `BranchState.PendingDynamicNext`, `PendingDynamicNextStep`;
  update `ToCheckpoint` / `FromCheckpoint`
- `execution.go` — add `resolveStep`; add commit logic in
  `executeActivity` (after logging, before checkpoint); add rollback
  on commit/checkpoint failure; update resume to use `resolveStep`
  instead of `workflow.GetStep`
- `branch.go` — add pending-next-edge bypass at top of activity
  execution path (skip activity if `PendingDynamicNextStep ==
  CurrentStep`); add pending-next-edge consumption before static
  `Step.Next` in branching logic
- `context.go` — add buffered plan field on `executionContext`; add
  internal `dynamicPlanEmitter` interface for type assertion
- `validate.go` — add `dyn/` prefix rejection for static steps; add
  runtime plan validator (step kinds, branch names, edge targets)
- tests around checkpointing, resume, branching, and progress

Most of the runtime changes should be mechanical once `resolveStep` and
pending-next-edge state exist. The branch run-loop bypass for committed
pending edges is the most novel piece.

## Alternatives Considered

### Mutate `Workflow` directly

Rejected because:

- it makes workflow definitions execution-sensitive
- it risks cross-execution contamination
- it complicates validation and concurrency semantics

### Return dynamic graph from the activity result only

Rejected because:

- it is not durable across the current activity-checkpoint boundary
- a crash can lose the generated graph

### Move all checkpointing later, after branch advancement

Rejected for now because:

- it changes a larger existing behavior surface
- it is not necessary if commit happens before the current checkpoint

### Stage plan into execution state during EmitDynamicPlan

Rejected because:

- `executeActivity` checkpoints every attempt before retry/catch
  resolution in `branch.Run`
- a failed attempt would durably persist a pending plan before the
  engine knows whether the step will retry, catch, or succeed
- buffer-then-commit-on-success avoids this entirely

## Recommended First Implementation

Build the smallest durable slice:

1. Add execution-local `dynamicSteps` to execution state and checkpoint.
2. Add `Execution.resolveStep(name)`.
3. Add `EmitDynamicPlan(ctx, plan)` — buffers plan in memory only.
4. Add `BranchState.PendingDynamicNext` (rewritten edges only, not
   full step definitions).
5. On activity success, commit the buffered plan: validate, rewrite
   names, register in overlay, record next edges on branch — then
   write the existing post-activity checkpoint.
6. On any non-success path, discard the buffer — nothing is persisted.
7. On branch advance, consume pending dynamic next edges before static
   `Step.Next`.
8. Reject `EmitDynamicPlan` if the emitting step unwinds into a wait.
9. Reject `EmitDynamicPlan` if the activity has already called it once
   (no overwrite within a single invocation).

**Supported in v1:**

- Dynamic activity steps only.
- Unconditional and conditional next edges.
- Fan-out from the emitting step via multiple `DynamicNext` entries.
- Dynamic steps may define their own static `Next` edges to other
  dynamic steps or back to static workflow steps.

**Deferred from v1:**

- Dynamic wait, sleep, pause, and join steps — each has bespoke
  execution/resume behavior that multiplies the test surface.
- `Each` modifier on dynamic steps — requires sub-branch naming.
- `Retry` and `Catch` on dynamic steps.
- Catch edges from dynamic steps.
- Nested dynamic emission (a dynamic step emitting its own plan).

This yields a useful capability for AI agent workflows — an agent
activity emits a subgraph of activity steps, and the engine executes
them with full checkpointing, retry, and observability — without
redesigning the engine. Dynamic wait/sleep/pause can follow once the
core overlay plumbing is proven end-to-end.

## Checkpoint Example

A checkpoint with two dynamic steps after the pending next edges have
been consumed and the branch has advanced into the dynamic subgraph:

```json
{
  "workflow_name": "research-pipeline",
  "dynamic_steps": {
    "dyn/main/1/gather": {
      "name": "dyn/main/1/gather",
      "activity": "web-search",
      "parameters": { "query": "climate change mitigation strategies" },
      "next": [{ "step": "dyn/main/1/summarize" }]
    },
    "dyn/main/1/summarize": {
      "name": "dyn/main/1/summarize",
      "activity": "llm-summarize",
      "next": [{ "step": "finalize" }]
    }
  },
  "dynamic_step_counter": 2,
  "branches": {
    "main": {
      "current_step": "dyn/main/1/gather",
      "status": "running"
    }
  }
}
```

The `dynamic_steps` map is restored on resume. `resolveStep` finds
`dyn/main/1/gather` in the overlay and execution continues.

## Open Questions

- Do we want a small metadata field such as `DisplayName` for operator
  readability, or is synthetic `Step.Name` acceptable in v1?
- How should catch edges work for dynamic steps in a future version?
  If a dynamic step fails with no catch, the failure propagates to the
  branch — same as static steps. But can a dynamic plan include its
  own catch edges? The overlay model supports it, but the routing
  semantics need a concrete decision before implementation. Deferred
  from v1.
- Nested emission (a dynamic step emitting its own plan) — the naming
  scheme supports it (`dyn/branch/N/name` with incrementing counter),
  but the interaction with fan-out and catch adds complexity. Deferred
  from v1.

## Recommendation

Proceed with the execution-local overlay design.

It is the simplest model that is both:

- durable across checkpoint/resume
- additive to the current engine architecture

The key design choice is buffer-then-commit: buffer plans in memory
during activity execution, commit into the overlay only on success,
immediately before the existing post-activity checkpoint. This keeps
the implementation contained, avoids stale plans from failed attempts,
and aligns with the current execution model without moving checkpoint
boundaries.
