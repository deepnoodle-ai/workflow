# RFC: Dynamic Steps Via Execution-Local Overlay

Status: draft

## Summary

This RFC proposes a phase-two implementation for dynamic step support
that preserves the existing immutable `Workflow` definition and adds an
execution-local overlay of synthetic steps.

The core idea is:

- `Workflow` remains static and immutable.
- `Execution` gains an append-only map of dynamic steps.
- Runtime step lookup resolves against `dynamic overlay + static workflow`.
- Activities emit a dynamic plan through a helper API.
- The engine stages that plan before the existing post-activity
  checkpoint, then activates it only if the step completes
  successfully.

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
- Solving every dynamic control-flow case in the first cut.
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

This requires a checkpoint schema bump.

The overlay is append-only:

- once a dynamic step is registered, it is immutable
- a dynamic step name is unique within the execution
- the base workflow is never modified

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

This keeps the public `Context` interface stable while still exposing a
first-class feature to activities.

### 4. Activation model

Dynamic plans are two-phase:

1. staging
2. activation

Staging means:

- validate the plan
- normalize generated step names
- register the generated steps in the execution overlay
- persist the plan as the pending next-step plan for the current branch
  and step

Activation means:

- only after the step returns success
- `handleBranching` prefers the pending dynamic plan over static
  `Step.Next`
- the pending plan is consumed exactly once

This avoids having a created plan take effect if the activity later
fails, retries, hits a catch, or unwinds into a wait.

## Checkpoint Timing

### Chosen approach

Keep the current post-activity checkpoint and make dynamic plan staging
occur before that checkpoint.

The sequence becomes:

1. activity starts
2. activity calls `EmitDynamicPlan`
3. engine validates the plan, rewrites names if needed, and records:
   - dynamic steps in execution state
   - a pending plan scoped to the current branch and current step
4. activity returns
5. `executeActivity` writes its normal checkpoint
6. branch logic consumes the pending plan on successful completion

This is the simplest durable option because it does not require moving
the existing checkpoint boundary.

### Why not rely on activity return values alone

If an activity simply returns "here is my generated graph", that plan
exists only in memory until `branch.Run` handles it. The current engine
checkpoints inside `executeActivity`, before `branch.Run` evaluates
branching. A crash in that window would lose the generated graph.

By staging the plan into execution state before the existing
checkpoint, the plan survives that crash window.

### Pending-plan persistence

Add per-branch pending dynamic routing state:

```go
type BranchState struct {
    // existing fields...
    PendingDynamicPlan     *DynamicPlan `json:"pending_dynamic_plan,omitempty"`
    PendingDynamicPlanStep string       `json:"pending_dynamic_plan_step,omitempty"`
}
```

Rules:

- the pending plan is scoped to a single branch and a single step
- it is written by `EmitDynamicPlan`
- it is consumed only when that same step completes successfully
- it is cleared when the branch advances past the step
- it is cleared on any non-success path from the step

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

Dynamic steps do not participate in `Workflow.Validate()`. The base
workflow remains statically valid on its own.

Add runtime validation for emitted plans:

- step names are unique within the plan before normalization
- step kind exclusivity still holds
- store-path rules still hold
- activity names resolve in the registry
- parameter templates compile
- edge conditions compile
- every target step resolves against:
  - staged plan local names
  - previously-created dynamic steps
  - static workflow steps

This validator should reuse the same validation logic already used for
workflow construction and binding as much as possible.

## Runtime Behavior

### Success path

1. Activity emits a dynamic plan.
2. Engine stages and persists it.
3. Activity returns success.
4. Branch records normal step output.
5. Branch branching logic checks for a pending dynamic plan first.
6. If present, it creates branch specs from `plan.Next`.
7. Branch advances or fans out.
8. Pending plan is cleared from branch state.

### Failure path

If the step returns an error:

- the pending dynamic plan is not activated
- the pending dynamic plan is cleared
- normal retry / catch / failure behavior proceeds

This avoids stale dynamic transitions leaking into a later attempt or
catch path.

### Wait-unwind path

If the step unwinds into `Wait`:

- the pending dynamic plan is not activated
- the pending dynamic plan remains safe to discard
- on resume, the activity may emit the plan again

This matches the existing replay-safety contract: code before the wait
can run again, so dynamic plan emission must also be replay-safe.

Activities that emit dynamic plans before a wait should either:

- emit deterministically, or
- wrap the plan construction in `ctx.History().RecordOrReplay`

## Joins and Branch Names

The overlay model makes dynamic joins possible because join identity is
already string-based. A dynamic join step can be stored and resumed like
any other dynamic step.

That said, the first implementation should keep the surface area small.

Recommended first-cut limits:

- allow dynamic activity steps
- allow dynamic wait, sleep, and pause steps
- allow fan-out via `plan.Next`
- allow join steps only when they use `Count`
- defer `JoinConfig.Branches` against future dynamic named branches

Reason:

- explicit named-branch joins require more runtime validation
- the current static validator assumes branch names are known from the
  workflow definition
- count-based joins avoid that extra machinery in v1

This can be relaxed later without changing the overlay model.

## Observability

Current observability surfaces can remain string-based:

- `BranchExecutionEvent.CurrentStep`
- `ActivityExecutionEvent.StepName`
- `StepProgress.StepName`
- `ActivityLogEntry.StepName`

No API change is required there. Dynamic step names will appear in the
same fields as static step names.

## Resume Semantics

Resume stays simple:

- load checkpoint
- restore `dynamicSteps`
- restore `dynamicStepCounter`
- restore branch `CurrentStep`
- resolve the current step through `execution.resolveStep`

No mutable workflow rebuild is required.

## Impacted Areas

Expected code touch points:

- `checkpoint.go`
- `execution_state.go`
- `execution.go`
- `branch.go`
- `context.go`
- `validate.go`
- tests around checkpointing, resume, branching, waits, and progress

Most of the runtime changes should be mechanical once `resolveStep` and
pending-plan state exist.

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
- it is not necessary if staging happens before the current checkpoint

## Recommended First Implementation

Build the smallest durable slice:

1. Add execution-local `dynamicSteps` to execution state and checkpoint.
2. Add `Execution.resolveStep(name)`.
3. Add `EmitDynamicPlan(ctx, plan)`.
4. Add `BranchState.PendingDynamicPlan`.
5. Stage plans before the existing post-activity checkpoint.
6. On success, consume pending dynamic next edges before static
   `Step.Next`.
7. Clear pending plans on any non-success path.
8. Support dynamic activity steps plus simple fan-out first.

This yields a useful phase-two capability without redesigning the
engine.

## Open Questions

- Should the first cut allow dynamic steps to contain their own static
  `Next` graph immediately, or should it require every emission to
  supply only entrypoints and let later steps emit more plans?
- Do we want a small metadata field such as `DisplayName` for operator
  readability, or is synthetic `Step.Name` acceptable in v1?

## Recommendation

Proceed with the execution-local overlay design.

It is the simplest model that is both:

- durable across checkpoint/resume
- additive to the current engine architecture

The key design choice is to stage dynamic plans before the existing
post-activity checkpoint rather than trying to re-time checkpointing
globally. That keeps the implementation contained and aligns with the
current execution model.
