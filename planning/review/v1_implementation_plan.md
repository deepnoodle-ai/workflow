# Workflow v1 — Implementation Plan

**Date:** 2026-04-11
**Source:** `planning/review/combined_api_review.md`
**Goal:** Take the workflow library from its current pre-v1 shape to a stable
v1 surface that we are willing to live with for the next ~5 years.
**Posture:** This is the cheap window. There are no real external consumers.
We do *one* breaking release, mean it, and tag v1 on the other side.

This document is a working plan, not a summary of the review. The review
identified the *what*; this document picks an opinion where the review left
choices open, sequences the work into coherent landable chunks, and lists the
specific edits we expect to make.

---

## Part 1 — Locked-in decisions

The review surfaced a number of judgment calls. Before any code moves, here's
where we land. Each item below is a decision we will not relitigate during
implementation; if one turns out wrong, we change it deliberately, not by
drifting.

### 1.1 Run methods: collapse to ONE

**Decision:** `Execution` exposes exactly one run method:

```go
func (e *Execution) Execute(ctx context.Context, opts ...ExecuteOption) (*ExecutionResult, error)
```

Resume becomes an option:

```go
result, err := exec.Execute(ctx, workflow.ResumeFrom(priorID))
```

Delete `Run`, `Resume`, `RunOrResume`, and `ExecuteOrResume`. No deprecation
window — we are pre-v1. The whole point of doing this now is that there are
no real consumers to break.

**Why this is the right call:** Six entry points exist because we shipped the
`error`-returning variants first and the `*ExecutionResult` variants later
without removing the old ones. The `error`-returning forms are strictly
inferior — they erase the suspended-vs-failed distinction. Keeping any of them
"as a lower-level escape hatch" is a discovery cliff masquerading as
flexibility. Pick one shape and commit.

### 1.2 Runner is the front door

**Decision:** Runner is the documented production path. README, llms.txt, and
godoc on `Execution` all lead with `Runner.Run`. `Execution.Execute` is shown
exactly once, in a "for tests and one-off scripts" sidebar.

We are not deprecating `Execution.Execute` — tests and trivial uses need it.
But every consumer-facing example uses Runner.

### 1.3 Rename Path → Branch

**Decision:** "Path" as a name for an execution thread is dead. Throughout the
codebase: `Path` → `Branch`, `PathID` → `BranchID`, `PausePath` → `PauseBranch`,
`ErrPathNotFound` → `ErrBranchNotFound`, `Edge.Path` → `Edge.BranchName`,
`JoinConfig.Paths` → `JoinConfig.Branches`, `JoinConfig.PathMappings` →
`JoinConfig.BranchMappings`.

The word "path" survives in only one place: as English prose for a
dot-notation state address (`state.results.foo`). We do not introduce a type
for that — it remains a string convention documented in one place.

`Workflow.Path()` and `Options.Path` are deleted outright.

### 1.4 Step kinds: validate mutual exclusivity now (Option A)

**Decision:** `Step` keeps its current shape. `workflow.New` rejects any step
that has more than one of `Activity`, `Join`, `WaitSignal`, `Sleep`, `Pause`
set. Modifier fields (`Each`, `Retry`, `Catch`, `Store`, `Next`) remain
free-standing.

Option B (explicit `Kind` tag) is cleaner but breaks every existing JSON
definition. Option A is sufficient for v1 and preserves wire compatibility for
the JSON shape, which is the only public format users currently serialize.
Revisit Option B in v2 if the validation approach starts to feel painful.

We will also document the precedence rules in godoc on `Step` so the chosen
behavior under "exactly one kind" is unambiguous.

### 1.5 Single templating syntax

**Decision:** One template syntax: `${...}`. Type-preservation is inferred
contextually:

- If the template spans the entire string (after trim): preserve type.
- If the template is interpolated into surrounding text: stringify.

Delete `$(...)`. The two-syntax design has been a confusion factory since
day one and is the kind of footgun a v1 should not ship with.

The detection logic is small: tokenize, check whether the single token covers
the whole input. Update `script/` to expose only one `Compile` and one
`Execute` path.

If anyone disagrees with this decision during implementation, the fallback is:
**document `${...}` as primary, hide `$(...)` in an "advanced: type
preservation" note, and revisit in v2.** I prefer the deletion.

### 1.6 ActivityRegistry as a real type, not a slice

**Decision:** Introduce `*ActivityRegistry` as a concrete type. `NewExecution`
takes it positionally. Slices of `Activity` are not accepted anywhere in the
public API. Duplicate registration returns an error.

```go
reg := workflow.NewActivityRegistry()
reg.MustRegister(workflow.ActivityFunc("fetch", fetchFn))
reg.MustRegister(activities.NewPrintActivity())

exec, err := workflow.NewExecution(wf, reg, workflow.WithInputs(...))
```

The existing internal `map[string]Activity` type alias goes away. The type
becomes opaque to consumers; only `Register`, `MustRegister`, `Get`, and
`Names` are exported.

### 1.7 Functional options for NewExecution

**Decision:** `NewExecution` becomes:

```go
func NewExecution(wf *Workflow, reg *ActivityRegistry, opts ...ExecutionOption) (*Execution, error)
```

`ExecutionOptions` the struct goes away. All ten optional fields become
functional `With*` options. `RunnerConfig` and `RunOptions` follow the same
pattern for consistency, even though they're already smaller.

### 1.8 Context: properties, not getters; fold in helpers

**Decision:** All `Get*` methods on `Context` lose the `Get`. `Wait`,
`History`, and `ReportProgress` move from package-level helpers onto the
`Context` interface.

```go
type Context interface {
    context.Context
    VariableContainer

    Inputs() Inputs
    Logger() *slog.Logger
    Compiler() script.Compiler
    BranchID() string
    StepName() string

    Wait(topic string, timeout time.Duration) (any, error)
    History() *History
    ReportProgress(detail ProgressDetail)
}
```

The side interfaces `SignalAware`, `ActivityHistoryAware`, `ProgressReporter`
disappear. They existed only because the package-level helpers needed runtime
type assertion; method dispatch makes them obsolete.

`workflowtest` ships a `FakeContext` so consumer tests don't have to implement
the full interface by hand. `NewContext` and `ExecutionContextOptions` move to
`workflowtest` (or stay internal if `FakeContext` doesn't need them).

`VariableContainer` gets folded into `Context` and deleted as a separate type.
Its methods shorten: `SetVariable` → `Set`, `GetVariable` → `Get`,
`ListVariables` → `Keys`, `DeleteVariable` → `Delete`. (`Get` on the variable
container does *not* conflict with anything else after the `Get*` rename.)

### 1.9 Two-phase validation

**Decision:** Validation happens in two phases.

**Phase 1 — `workflow.New`:** Structural only. No activity registry, no
compiler. Catches:
- Duplicate step names (`ErrDuplicateStepName`)
- Empty step names
- Dangling edge destinations
- Dangling catch destinations
- Step-kind mutual exclusivity (decision 1.4)
- Illegal modifier combinations (e.g., `Retry` on `Pause`)
- `JoinConfig.Branches` references existing upstream branch names
- `SleepConfig.Duration > 0`
- `WaitSignalConfig.Timeout > 0`
- `RetryConfig.MaxRetries >= 0`, `MaxDelay >= BaseDelay`, etc.
- `Options.StartAt` (new, decision 1.13) references an existing step
- Reachability (warn but do not error on unreachable steps — too aggressive
  for v1)

**Phase 2 — `NewExecution`:** Binding validation. Has the registry and
compiler. Catches:
- Activity references resolve in the registry (`ErrUnknownActivity`)
- Parameter templates compile
- Edge condition expressions compile
- `WaitSignalConfig.Topic` templates compile
- `SignalStore` is configured if any step uses `WaitSignalConfig` (warn, not
  error — see decision 1.10)

Both phases collect *all* problems into a `*ValidationError` rather than
failing on the first.

### 1.10 SignalStore requirement: warn, don't fail

**Decision:** If a workflow uses `WaitSignalConfig` and `NewExecution` is
called without a `SignalStore`, log a warning. Do not fail. The current
runtime error remains as the actual failure mode.

**Why:** A consumer who declares `WaitSignal` but never reaches that step has
done nothing wrong. The warning is enough; failing closed forces consumers to
configure infrastructure they may not actually exercise. (We should reconsider
this in v2 if it bites anyone.)

### 1.11 Checkpoint: stable wire format with versioning

**Decision:** Commit. `Checkpoint` is the on-disk contract for every backend.
Make it explicit:

1. Add `SchemaVersion int` (start at 1).
2. Type `Status` as `ExecutionStatus`, not `string`.
3. Replace `interface{}` with `any` everywhere.
4. After the Path → Branch rename, the field becomes
   `BranchStates map[string]*BranchState` and `BranchCounter int`.
5. Document the round-trip contract: every field a backend stores must
   round-trip exactly. `BranchState.PauseRequested`, `BranchState.Wait`, and
   `BranchState.Variables` are explicitly called out as "drop these and pause
   silently breaks."
6. Add an optional `AtomicUpdate(ctx, execID, fn) error` side interface.
   `PauseBranchInCheckpoint` uses it when implemented; otherwise falls back to
   load-modify-write. This unlocks Postgres-style row locking for production
   deployments.
7. **Versioning rule:** within a major version, fields can only be added.
   Renaming or retyping a field requires bumping `SchemaVersion`. The library
   ships a one-direction migrator (`migrate(old) Checkpoint`) when needed.

We do **not** make `Checkpoint` opaque. The "easy to build a Postgres
Checkpointer with real columns" story is one of the library's actual selling
points. Hiding the struct destroys it.

### 1.12 `"state."` prefix: bare names on the write side

**Decision:**

- **Read side (templates):** `state.foo` and `inputs.foo` stay. They are the
  documented namespace prefixes inside template expressions.
- **Write side (config fields):** `Step.Store`, `WaitSignalConfig.Store`,
  `CatchConfig.Store`, `Output.Variable` all expect bare variable names.
  Validation rejects strings starting with `state.` with an explicit error.
  No silent strip.
- **Nested writes:** `Store: "results.inner"` for nested writes is its own
  documented feature, separate from the bare-name rule. The store value is a
  state path, not a variable name; we'll document this distinction in the
  godoc on `Store`.

### 1.13 Explicit start step

**Decision:** Add `Options.StartAt string`. Default: `Steps[0].Name` when
omitted. Validated to reference an existing step. The "first in slice"
behavior is preserved as the default, so unchanged consumers keep working,
but the implicit ordering dependency is now overrideable.

### 1.14 `Input.Type` is metadata

**Decision:** Do not enforce. Rename to make intent obvious:
`Input.Type` → `Input.TypeDoc string` (or just leave it `Type` and document
loudly in godoc that it is documentation only). We will pick the rename
during implementation if it doesn't break too much; otherwise the godoc fix
is sufficient.

We are **not** building a schema language for inputs. Consumers who need
typed validation can validate before calling `NewExecution`.

### 1.15 Child workflows: stay supported, fix the bugs

**Decision:** Child workflows stay in the public API. We fix the concrete
bugs:
1. Delete `ChildWorkflowSpec.Sync` (the `ExecuteSync`/`ExecuteAsync` split is
   the source of truth).
2. Drop `ChildWorkflowResult.Error` (return value is the source of truth).
3. Align all timeouts on `time.Duration` — delete the `float64` seconds in
   `activities/child_workflow_activity.go`.
4. Make the `DefaultChildWorkflowExecutor` cleanup timeout configurable via
   `ChildWorkflowExecutorOptions`; default to no timeout (or 1h) instead of
   the arbitrary 5m.
5. Document async-vs-checkpoint semantics: what happens to a running async
   child if the parent checkpoints. (The honest answer may be "the child
   keeps running but the parent loses its handle on resume" — say so in
   godoc and add a TODO for v1.1.)
6. Document the "wait for child completion" pattern with a worked example.

We do **not** demote child workflows to `experimental/`. They are referenced
in too many places and the bugs above are individually small. The honest
documentation in #5 is the right escape valve for the unresolved semantics.

### 1.16 Activities: tier split

**Decision:**

```
activities/             # safe, in-process primitives only
    print, json, fail, time helpers, random helpers
activities/httpx/       # http client (safe but I/O)
activities/contrib/     # shell, file, anything environment-specific or risky
```

`activities.NewWaitActivity` is **deleted**. The name collides with the
durable wait. Anyone wanting an in-process delay can call `time.Sleep` inside
their own activity.

`PrintActivity` accepts an injected `io.Writer`. Defaults to `os.Stdout` for
backward compatibility but lets tests and production callers redirect.

All activity timeouts that currently use `float64` seconds become
`time.Duration`.

### 1.17 Activity function naming

**Decision:**

- `NewActivityFunction` → `ActivityFunc` (returns `Activity`)
- `NewTypedActivityFunction` → `TypedActivityFunc[T, R]`
- `NewTypedActivity` stays — it wraps a struct, not a function
- Internal `ActivityFunction` and `TypedActivityFunction` struct types are
  unexported

Matches `http.Handler`/`http.HandlerFunc` convention.

### 1.18 Skipped: things we are NOT doing in this pass

- **Fluent builder DSL.** The struct + JSON form is canonical. A parallel Go
  DSL diverges over time. Skip permanently.
- **`workflow.New` → `workflow.Define` rename.** Lowest-value rename in the
  whole list. The cost (every example, every test) outweighs the readability
  win. Skip.
- **Workflow versioning, deadlines, idempotency keys, saga/compensation,
  rate limiting.** Phase C from the review. Real features, but they belong
  to v1.1+, not the v1 cleanup. We add hooks where needed (e.g., room for a
  `Workflow.Version` field) but no implementation.
- **OTel adapter.** v1.1.
- **Demoting `cmd/workflow`.** Keep it. Document its role.

---

## Part 2 — Sequencing strategy

### 2.1 Why a single breaking release

The pre-v1 window is the only time we get to make breaking changes for free.
Splitting this work over multiple "minor breaking" releases means consumers
migrate twice and we explain ourselves twice. We instead land everything on a
branch (`v1`), let it stabilize, and tag v1.0.0 once.

The branch strategy:

```
main (current state)
  └── v1 (long-lived feature branch for the cleanup)
       ├── PR #1 — Path → Branch rename (mechanical, isolated)
       ├── PR #2 — Surface shrink (hide internals)
       ├── PR #3 — Run method collapse + functional options
       ├── ... etc
       └── final merge → tag v1.0.0
```

PRs land into `v1`, not `main`. `main` keeps the current shape until v1 is
ready to merge. Each PR runs the test suite green before merging. The
worktree at `.claude/worktrees/signals-pause` (already in memory) is unrelated
and continues independently — it merges into `main` first, then into `v1`.

### 2.2 Why this order

The PR order is chosen so that each PR:

1. **Compiles standalone.** No PR leaves the tree in a broken state.
2. **Has a small, reviewable diff** where possible. The Path→Branch rename is
   the one exception — it's mechanical and large, but cheap to review with the
   right tooling.
3. **Doesn't undo earlier PR work.** Renaming things twice is a tax we avoid.

The dependency order:

```
PR1 (Path → Branch rename)
  → PR2 (Surface shrink — hide BranchState, ExecutionState, etc.)
  → PR3 (Validation phase 1 + step kinds + StartAt)
  → PR4 (ActivityRegistry + functional options + run method collapse)
  → PR5 (Validation phase 2 + binding checks)
  → PR6 (Context properties + fold helpers + VariableContainer collapse)
  → PR7 (Checkpoint stable wire format)
  → PR8 (Single template syntax)
  → PR9 (Activities tier split + naming)
  → PR10 (Child workflow fixes)
  → PR11 (Error model cleanup + completion hook fix)
  → PR12 (ExecutionResult helpers + minor polish)
  → PR13 (Documentation rewrite — README, llms.txt, suspension doc, prod checklist)
  → tag v1.0.0
```

The two big disruptive PRs (PR1 and PR4) come first so that everything after
them is built on the post-rename, post-functional-options world. PR13 is last
because the docs need to match the final shape.

### 2.3 Test strategy

The library has a substantial test suite. Most of these PRs will require
significant test updates because so many tests poke at the public API
directly.

**Three rules:**

1. **No PR is mergeable until `make test-all` is green.** No exceptions.
2. **Add a regression test for every behavior change.** If we change duplicate
   step name from "silent overwrite" to "error", we add a test for that
   error.
3. **`workflowtest.FakeContext` lands as part of PR6** so that consumer-side
   test ergonomics are not regressed by the Context method changes.

I suspect we'll find that many tests touch internals that are about to be
hidden. That's fine — they move into `_test.go` files in the same package
(internal tests), or get rewritten to test through the public API.

### 2.4 Migration guide

There is no migration guide because there are no external consumers. But we
do write a `MIGRATION.md` *in the v1 branch* that lists every breaking change,
with before/after snippets. This is what we hand to future Curtis when he
spins up a new consumer.

---

## Part 3 — PR-by-PR work breakdown

Each section below is one logical PR. The work is sized so each PR is
reviewable as a unit. Where a PR touches a large number of files
mechanically (e.g., the rename), the description calls that out so the
reviewer knows to skim.

### PR 1 — Path → Branch rename

**Estimated diff size:** Large but mechanical. ~1000-1500 lines, mostly
identifier replacement.

**Changes:**

| Old | New |
|---|---|
| `Path` (struct in `path.go`) | `Branch` |
| `PathID` | `BranchID` |
| `PathState` | `BranchState` |
| `PathSnapshot` | `BranchSnapshot` |
| `PathOptions` | `BranchOptions` |
| `PathSpec` | `BranchSpec` |
| `PathLocalState` | `BranchLocalState` |
| `PausePath` (method on Execution) | `PauseBranch` |
| `UnpausePath` | `UnpauseBranch` |
| `PausePathInCheckpoint` | `PauseBranchInCheckpoint` |
| `UnpausePathInCheckpoint` | `UnpauseBranchInCheckpoint` |
| `ErrPathNotFound` | `ErrBranchNotFound` |
| `Edge.Path` | `Edge.BranchName` |
| `JoinConfig.Paths` | `JoinConfig.Branches` |
| `JoinConfig.PathMappings` | `JoinConfig.BranchMappings` |
| `Workflow.Path()` | **DELETED** |
| `Options.Path` | **DELETED** |
| `Workflow.path` (private field) | **DELETED** |
| `Checkpoint.PathStates` | `Checkpoint.BranchStates` |
| `Checkpoint.PathCounter` | `Checkpoint.BranchCounter` |
| `Context.GetPathID` | `Context.GetBranchID` (renamed again in PR6) |
| `PathExecutionEvent` | `BranchExecutionEvent` |
| `pathSnapshots` (private channel) | `branchSnapshots` |
| `activePaths` (private map) | `activeBranches` |

**Files touched (~25):** `path.go`, `path_state.go`, `path_local_state.go`,
`path_join_test.go`, `path_test.go`, `step.go`, `workflow.go`, `execution.go`,
`execution_state.go`, `checkpoint.go`, `pause.go`, `pause_test.go`,
`execution_callbacks.go`, `runner.go`, all tests that reference the names,
`llms.txt`, `README.md` (just the rename — full docs rewrite is PR13).

**Validation:** `make test-all` green. Plus a search for the substring
`"path"` (case-insensitive) in identifiers across the tree to confirm
nothing slipped through.

**Risk:** The substring `path` shows up in legitimate places (file paths,
state paths, "the path through the graph" English prose). We need to be
careful with the rename — IDE rename refactor by symbol is mandatory; no
sed-based substring replace.

### PR 2 — Surface shrink

**Goal:** Hide everything that is orchestration plumbing.

**Unexport (lowercase):**

- `Branch` → `branch` (after PR1)
- `BranchOptions`, `BranchSpec`, `BranchSnapshot` → unexport
- `BranchState`, `JoinState` — keep exported, but document round-trip contract
  (these are checkpoint wire format)
- `ExecutionState` → `executionState`
- `ExecutionAdapter` → `executionAdapter`
- `WaitState`, `WaitKind`, `NewSignalWait`, `NewSleepWait` → unexport
- `BranchLocalState` → `branchLocalState`
- `Patch`, `PatchOptions`, `NewPatch`, `GeneratePatches`, `ApplyPatches` →
  unexport (or move to `internal/patch/`)
- `WaitRequest`, `JoinRequest` → unexport
- `IsWaitUnwind` → unexport (the engine handles this; consumers don't need it)
- `PauseRequest` → unexport

**Move:**

- `NewContext`, `ExecutionContextOptions` → move to `workflowtest` as part of
  the `FakeContext` story (PR6 wires this up; PR2 just moves them into a
  package-private form so PR6 has somewhere to land them).

**Decision: `WorkflowFormatter`.** Either document it on `ExecutionOptions`
(soon to be `WithFormatter` option) or delete it. **Decision: delete.** It is
undocumented and not referenced in any example. If we need it back, we add it.

**Files touched:** Roughly the same files as PR1, plus deletion of any test
that pokes at the now-private types. Internal tests move from `_test.go`
files in their own packages to internal `_test.go` files in the root
package as needed.

**Validation:** `make test-all` green. The total number of exported
identifiers in the root package drops from ~75 to ~55-60.

### PR 3 — Validation phase 1 + step kinds + StartAt

**Goal:** `workflow.New` becomes the structural validator and starts failing
fast.

**Changes:**

1. Add `Options.StartAt string`. Default to `Steps[0].Name`. Validate
   reference.
2. Reject duplicate step names with `ErrDuplicateStepName`.
3. Reject steps with multiple kind fields set
   (`Activity`/`Join`/`WaitSignal`/`Sleep`/`Pause`).
4. Reject illegal modifier combinations (`Retry` on a pure pause/wait/sleep
   step).
5. Reject dangling catch destinations.
6. Validate `JoinConfig.Branches` reference upstream branch names that
   actually exist on incoming edges.
7. Validate `SleepConfig.Duration > 0`, `WaitSignalConfig.Timeout > 0`,
   `RetryConfig` sanity.
8. Collect all problems into `*ValidationError`, not the first one.

**New errors:**

```go
var (
    ErrDuplicateStepName    = errors.New("workflow: duplicate step name")
    ErrInvalidStepKind      = errors.New("workflow: invalid step kind combination")
    ErrUnknownStartStep     = errors.New("workflow: start step not found")
    ErrUnknownCatchTarget   = errors.New("workflow: catch destination not found")
    ErrUnknownJoinBranch    = errors.New("workflow: join branch not found")
    ErrInvalidRetryConfig   = errors.New("workflow: invalid retry config")
    ErrInvalidSleepConfig   = errors.New("workflow: invalid sleep config")
    ErrInvalidWaitConfig    = errors.New("workflow: invalid wait config")
)
```

**Files touched:** `workflow.go`, `validate.go`, `validate_test.go`, `step.go`
(godoc updates for step kinds), `errors.go`, plus new tests.

### PR 4 — ActivityRegistry + functional options + run method collapse

**Goal:** The biggest reshape. After this PR, the canonical caller looks like
post-v1 code.

**New types and signatures:**

```go
// activity_registry.go (new file)
type ActivityRegistry struct { /* opaque */ }

func NewActivityRegistry() *ActivityRegistry
func (r *ActivityRegistry) Register(a Activity) error
func (r *ActivityRegistry) MustRegister(a Activity) *ActivityRegistry
func (r *ActivityRegistry) Get(name string) (Activity, bool)
func (r *ActivityRegistry) Names() []string

// execution.go
type ExecutionOption func(*executionConfig)

func NewExecution(wf *Workflow, reg *ActivityRegistry, opts ...ExecutionOption) (*Execution, error)

func WithInputs(m map[string]any) ExecutionOption
func WithCheckpointer(cp Checkpointer) ExecutionOption
func WithSignalStore(ss SignalStore) ExecutionOption
func WithLogger(l *slog.Logger) ExecutionOption
func WithExecutionID(id string) ExecutionOption
func WithExecutionCallbacks(cb ExecutionCallbacks) ExecutionOption
func WithStepProgressStore(s StepProgressStore) ExecutionOption
func WithActivityLogger(al ActivityLogger) ExecutionOption
func WithScriptCompiler(c script.Compiler) ExecutionOption

// Execute options
type ExecuteOption func(*executeConfig)
func ResumeFrom(priorID string) ExecuteOption

// On Execution:
func (e *Execution) Execute(ctx context.Context, opts ...ExecuteOption) (*ExecutionResult, error)
```

**Deletions:**

- `ExecutionOptions` (the struct)
- `Execution.Run`
- `Execution.Resume`
- `Execution.RunOrResume`
- `Execution.ExecuteOrResume`
- The `ActivityRegistry map[string]Activity` type alias (the new struct
  takes the name)

**Runner adjustment:** `RunnerConfig` and `RunOptions` move to functional
options for consistency:

```go
runner := workflow.NewRunner(workflow.WithRunnerLogger(l), workflow.WithDefaultTimeout(5*time.Minute))
result, err := runner.Run(ctx, exec,
    workflow.WithHeartbeat(hbCfg),
    workflow.WithCompletionHook(hook),
    workflow.WithRunTimeout(30*time.Second),
    workflow.WithResumeFrom(priorID),
)
```

(Naming TBD during implementation — we want the option names to read well at
the call site without colliding between Execute/Run/Runner option sets. May
land as `runner.Run(ctx, exec, RunResumeFrom(...))` or similar.)

**Files touched:** `execution.go`, `runner.go`, `activity.go`,
`activity_functions.go`, plus every test and example. This is the largest
PR after PR1.

**Test strategy:** Every test that constructs an `ExecutionOptions` literal
needs updating. We do this in two passes:

1. Mechanical pass: update test setup to use the new constructor.
2. Coverage pass: add explicit tests for `ErrDuplicateActivity`,
   `ErrUnknownActivity`, and the rejection of `nil` registry.

### PR 5 — Validation phase 2 (binding checks)

**Goal:** `NewExecution` validates everything that requires the registry and
compiler.

**Changes:**

1. Activity references resolve in the registry.
2. Parameter templates compile.
3. Edge condition expressions compile.
4. `WaitSignalConfig.Topic` templates compile.
5. `Step.Store`, `WaitSignalConfig.Store`, `CatchConfig.Store`,
   `Output.Variable` reject `state.` prefix.
6. Warn if any step uses `WaitSignalConfig` and no `SignalStore` is configured
   (do not error — decision 1.10).
7. All problems collected into `*ValidationError`.

**New errors:**

```go
var (
    ErrUnknownActivity      = errors.New("workflow: activity not registered")
    ErrInvalidTemplate      = errors.New("workflow: invalid template")
    ErrInvalidExpression    = errors.New("workflow: invalid edge condition")
    ErrInvalidStorePath     = errors.New("workflow: store field must be a bare variable name")
)
```

**Files touched:** `execution.go`, `validate.go`, `validate_test.go`, plus
new tests. The compiler interface may need a `Validate(string) error` method
if it doesn't already have one — confirm during implementation.

### PR 6 — Context properties + fold helpers

**Goal:** Context becomes idiomatic Go.

**Interface changes:**

```go
type Context interface {
    context.Context

    Inputs() Inputs              // typed wrapper around map[string]any
    Set(key string, value any)
    Get(key string) (any, bool)
    Keys() []string
    Delete(key string)

    Logger() *slog.Logger
    Compiler() script.Compiler
    BranchID() string
    StepName() string

    Wait(topic string, timeout time.Duration) (any, error)
    History() *History
    ReportProgress(detail ProgressDetail)
}
```

**Deletions:**

- `VariableContainer` interface (folded into `Context`)
- Package-level `workflow.Wait`
- Package-level `workflow.ActivityHistory`
- Package-level `workflow.ReportProgress`
- `SignalAware` interface
- `ActivityHistoryAware` interface
- `ProgressReporter` interface
- `InputsFromContext`, `VariablesFromContext` (helpers replaced by direct
  context methods)

**New type:**

```go
type Inputs struct {
    m map[string]any
}

func (i Inputs) Get(key string) (any, bool)
func (i Inputs) Keys() []string
func (i Inputs) Len() int
func (i Inputs) ToMap() map[string]any
```

(Decision pending: `Inputs` as a struct vs as `map[string]any` directly.
A struct gives us a place to add typed accessors later — `GetString`,
`GetInt`. Lean toward struct.)

**workflowtest additions:**

```go
type FakeContext struct { /* ... */ }
func NewFakeContext(opts FakeContextOptions) *FakeContext
```

A `FakeContext` lets consumer tests construct a Context without depending on
unexported types in the root package.

**Files touched:** `context.go`, `wait.go`, `activity_history.go`,
`progress.go`, `variable_container.go` (delete), `workflowtest/fake_context.go`
(new), every activity that calls `workflow.Wait` or `workflow.ActivityHistory`,
every test using `Get*` methods.

### PR 7 — Checkpoint stable wire format

**Goal:** Lock the on-disk shape.

**Changes:**

```go
type Checkpoint struct {
    SchemaVersion int                    `json:"schema_version"`
    ID            string                 `json:"id"`
    ExecutionID   string                 `json:"execution_id"`
    WorkflowName  string                 `json:"workflow_name"`
    Status        ExecutionStatus        `json:"status"`
    Inputs        map[string]any         `json:"inputs"`
    Outputs       map[string]any         `json:"outputs"`
    Variables     map[string]any         `json:"variables"`
    BranchStates  map[string]*BranchState `json:"branch_states"`
    JoinStates    map[string]*JoinState   `json:"join_states"`
    BranchCounter int                    `json:"branch_counter"`
    Error         string                 `json:"error,omitempty"`
    StartTime     time.Time              `json:"start_time,omitzero"`
    EndTime       time.Time              `json:"end_time,omitzero"`
    CheckpointAt  time.Time              `json:"checkpoint_at"`
}

const CheckpointSchemaVersion = 1
```

**New optional side interface:**

```go
type AtomicCheckpointer interface {
    AtomicUpdate(ctx context.Context, execID string, fn func(*Checkpoint) error) error
}
```

`PauseBranchInCheckpoint` checks `if ac, ok := cp.(AtomicCheckpointer); ok {
ac.AtomicUpdate(...) }`. Falls back to load-modify-write otherwise.

**Documentation:** Add a section to llms.txt describing the round-trip
contract. This goes in PR13 along with the rest of the doc rewrite, but
the godoc on `Checkpoint` itself lands in this PR.

**Files touched:** `checkpoint.go`, `checkpointer.go`, `checkpointer_file.go`,
`checkpointer_null.go`, `checkpointer_fenced.go`, `pause.go` (uses the
optional interface), tests.

### PR 8 — Single template syntax

**Goal:** One syntax, contextual type inference.

**Changes:**

1. Update `script/` template parser to accept only `${...}`.
2. Add contextual type inference: if the template token covers the whole
   trimmed input, the result preserves type; otherwise it's stringified.
3. Delete all `$(...)` parsing code paths.
4. Update every example, every test, every doc to use only `${...}`.

**Risk:** This is the most "consumer would notice" breaking change. But the
pre-v1 posture means we don't need a deprecation. The alternative — keeping
both syntaxes for the v1 release "to be safe" — perpetuates the footgun
forever.

**Files touched:** `script/`, `script_compiler_test.go`, every test that uses
templates, `llms.txt`, `README.md`, examples.

### PR 9 — Activities tier split + naming

**Goal:** `activities/` is no longer a grab-bag.

**Changes:**

1. Move `shell` and `file` activities from `activities/` to
   `activities/contrib/`.
2. Move `http` activity from `activities/` to `activities/httpx/`.
3. Delete `activities/wait_activity.go` (the in-process sleep landmine).
4. `PrintActivity` accepts injected `io.Writer` (default `os.Stdout`).
5. All `float64` second timeouts become `time.Duration`.
6. Rename `NewActivityFunction` → `ActivityFunc`,
   `NewTypedActivityFunction` → `TypedActivityFunc`.
7. Unexport `ActivityFunction`, `TypedActivityFunction` struct types.

**Files touched:** `activities/*.go` (most files), `activity.go`,
`activity_functions.go`, `examples/`, `cmd/workflow/main.go` (if it
references the moved activities).

### PR 10 — Child workflow fixes

**Goal:** Per decision 1.15.

**Changes:**

1. Delete `ChildWorkflowSpec.Sync`.
2. Drop `ChildWorkflowResult.Error` field; rely on the error return.
3. `activities/child_workflow_activity.go` uses `time.Duration` for
   `Timeout`, not `float64` seconds.
4. `ChildWorkflowExecutorOptions.CleanupTimeout time.Duration` (default 0
   == no timeout, or 1h).
5. Document async-vs-checkpoint semantics in godoc on `ExecuteAsync`.
6. Add a worked "wait for child completion" example to `examples/`.

**Files touched:** `child_workflow.go`, `activities/child_workflow_activity.go`,
`examples/`, tests.

### PR 11 — Error model + completion hook fix

**Changes:**

1. Stop substring-matching `"timeout"` in `ClassifyError`. Use
   `errors.Is(context.DeadlineExceeded)` only.
2. Document on the `ErrorTypeAll` constant itself that it does not match
   fatal errors.
3. Type `WorkflowError.Details` more narrowly, or remove it. **Decision:**
   keep it as `any`, but document that it is not guaranteed to round-trip
   through `Checkpoint.Error string`. If a consumer wants persistent
   structured details, they wrap their own error.
4. Prefix all error strings with `workflow: ` consistently.
5. Fix `runner.go:138-148` to attach `result.FollowUps = followUps` even
   when `hookErr != nil`. Log the hook error separately.
6. Update `completion_hook.go` godoc to match.

**Files touched:** `errors.go`, `runner.go`, `completion_hook.go`, tests.

### PR 12 — ExecutionResult helpers

**Goal:** Polish that consumers will actually use.

**New methods on `*ExecutionResult`:**

```go
func (r *ExecutionResult) OutputString(key string) (string, bool)
func (r *ExecutionResult) OutputInt(key string) (int, bool)
func (r *ExecutionResult) OutputBool(key string) (bool, bool)
func (r *ExecutionResult) WaitReason() SuspensionReason  // returns "" if not suspended
func (r *ExecutionResult) Topics() []string              // nil if not suspended
func (r *ExecutionResult) NextWakeAt() (time.Time, bool) // false if no wake-at
```

Consider `OutputAs[T any](r *ExecutionResult, key string) (T, bool)` as a
package-level generic helper.

**Files touched:** `execution_result.go`, `execution_result_test.go`.

### PR 13 — Documentation rewrite

**Goal:** The library is recommendable to someone reading the README.

**Changes:**

1. **README.md** — rewrite the quick example to use `Runner` and the new
   constructors. Add a "Production checklist" section.
2. **llms.txt** — full rewrite reflecting v1 API. Lead with `Runner`. Section
   ordering: Concepts → Quick example with Runner → Workflow definition →
   Steps → Activities → Execution → Suspension model → Production checklist
   → Reference.
3. **`docs/suspension.md`** (new) — the consolidated suspension document.
   Sections: 3 reasons table, sequence diagram, replay-safety contract,
   "schedule a resume from WakeAt" recipe, dominant-reason precedence rule.
4. **`docs/production_checklist.md`** (new) — checkbox list per §2.22 of the
   review.
5. **`MIGRATION.md`** (new, in v1 branch) — every breaking change with
   before/after.
6. **godoc on `Checkpoint`** — round-trip contract section explicitly listing
   `BranchState.PauseRequested`, `BranchState.Wait`, `BranchState.Variables`
   as load-bearing.
7. **godoc on `Step`** — document the (now-validated) "exactly one kind"
   rule and the modifier fields.
8. **godoc on `Context.Wait`** — replay-safety, deadline behavior, what a
   custom `Context` implementer must preserve to be wait-compatible.

**Files touched:** `README.md`, `llms.txt`, `docs/` (new), `MIGRATION.md`
(new in v1 branch), godoc throughout.

---

## Part 4 — Risks and watchouts

### 4.1 The Path → Branch rename leaks into prose

**Risk:** "path" in English (a path through the graph, a state path) is fine
to keep, but it's easy to over-rename and produce weird sentences in godoc
like "the branch through the graph."

**Mitigation:** During PR1, walk the godoc changes manually. The mechanical
identifier rename is safe; prose changes get a human reading pass.

### 4.2 Checkpoint backwards compat for consumers who already wrote one

**Risk:** Anyone with checkpoints from the old shape can't load them after
the rename. The field name changes from `path_states` to `branch_states`.

**Mitigation:** We are pre-v1, so this is acceptable. But we add a note in
MIGRATION.md and bump `SchemaVersion` to 1 as the marker. If a future
migration is needed within v1, we add a `migrate0to1` shim.

### 4.3 Functional options collide between Execute, Run, Runner

**Risk:** `WithLogger` could exist on `NewExecution`, `NewRunner`, and
`Runner.Run`. Unclear which one a caller is configuring.

**Mitigation:** Each option set lives in its own type
(`ExecutionOption`, `RunnerOption`, `RunOption`). The constructor names
disambiguate. We accept some name collision in user code (`workflow.WithLogger`
on Execution vs `workflow.WithRunnerLogger` on Runner) and pick names that
read clearly. Prefer prefixed names over generic ones — `WithExecutionLogger`,
`WithRunnerLogger`, `WithRunLogger` — when in doubt.

### 4.4 Single template syntax breaks every existing JSON workflow

**Risk:** Consumers with JSON workflow definitions that use `$(...)` won't
load.

**Mitigation:** Pre-v1, accepted breakage. MIGRATION.md calls it out
prominently. If any test workflow YAML/JSON files in the repo use `$(...)`,
they get rewritten in PR8.

### 4.5 PR ordering produces churn in tests

**Risk:** Tests get rewritten 2-3 times across PR1 (rename), PR4 (constructor
collapse), PR6 (context method renames).

**Mitigation:** Accept the churn. The alternative — landing all three changes
in one mega-PR — is unreviewable. Test churn during a v1 cleanup is
expected; we are not optimizing for git blame here.

### 4.6 Hidden contract in `Checkpoint` round-trip

**Risk:** Consumer-built Postgres `Checkpointer` implementations may silently
drop fields we add later (e.g., a new `BranchState.SomeFlag`). The library
keeps working until the new field is the difference between correct and
incorrect behavior.

**Mitigation:** This is the cost of having a wire format. Mitigate via:
1. The documented round-trip contract.
2. A `workflowtest.RoundTripCheckpointer(t, cp)` test helper that consumers
   can run against their checkpointer in their own test suite to verify
   field preservation. (Stretch — defer to v1.1 if PR scope balloons.)
3. SchemaVersion bumps when we introduce a load-bearing field.

### 4.7 The plan is too big to land before we lose interest

**Risk:** This is 13 PRs. Realistic risk that the v1 branch goes stale.

**Mitigation:** Each PR is independently mergeable into the `v1` branch. If
we lose momentum partway through, the v1 branch is still in a coherent
intermediate state — we either continue later or merge what we have and
release v1 as the partial cleanup. The PRs are ordered so that even
PR1+PR2+PR4 alone (the rename + surface shrink + constructor collapse)
would be a meaningful release on their own.

If the work needs to compress, the priority order for "bare minimum v1" is:
PR1, PR2, PR4, PR3, PR7, PR13. The others are improvements but not
foundational.

---

## Part 5 — Open questions to resolve before starting

These are decisions that the review left implicit and that we need to nail
down before PR4 lands. None of them blocks PR1-PR3.

1. **`ExecuteOption` vs `ExecuteOpts`?** Pure naming. Lean toward
   `ExecuteOption` (singular) to match `ExecutionOption`.

2. **`MustRegister` vs `Register`-only?** A `Must*` variant lets registration
   chain in `init()`. I prefer keeping both. Confirm.

3. **`Inputs` as a struct vs raw `map[string]any`?** Lean struct (room to
   add typed accessors). Confirm during PR6.

4. **Should `Workflow.Validate()` still exist as a public method, or only
   run inside `New`?** Lean toward keeping it public so consumers can
   validate before constructing — useful for editors/linters. The
   `*ValidationError` return type is the same in both cases.

5. **`Runner.Run` vs `Runner.Execute`?** Today it's `Run`. Renaming to
   `Execute` would symmetrize with `Execution.Execute`, but `Run` reads
   better and matches `http.Handler.ServeHTTP` /
   `http.Server.ListenAndServe` precedent. Keep `Run`.

6. **Naming for the deleted `activities.NewWaitActivity` replacement?** I say
   no replacement — consumers can call `time.Sleep` themselves. Confirm
   during PR9.

7. **Do we ship a `workflow/otel` adapter as part of v1 polish, or v1.1?**
   Defer to v1.1. The v1 release should ship `ExecutionCallbacks` as the
   stable interface; the OTel adapter is an obvious downstream package.

---

## Part 6 — Definition of done

The v1 branch is ready to merge to `main` and tag `v1.0.0` when:

- [ ] All 13 PRs merged into `v1`
- [ ] `make test-all` green
- [ ] `make cover` shows no regression in coverage from current main
- [ ] llms.txt and README rewritten and reviewed
- [ ] MIGRATION.md complete with before/after for every breaking change
- [ ] At least one example program in `examples/` exercises every major
      surface (Runner, signal wait, sleep, pause, child workflow, branching,
      join, retry, catch)
- [ ] Manual smoke test: build a small new program against the v1 branch
      from scratch, following only the docs. Anything confusing gets
      a doc fix.
- [ ] One day of soak time on the branch with no changes before tagging.

---

## Appendix — File-level change inventory

| File | PRs that touch it | Notes |
|---|---|---|
| `workflow.go` | 1, 3 | Rename + structural validation + StartAt |
| `step.go` | 1, 3, 11 | Rename Edge.Path; godoc step kinds; error consts |
| `execution.go` | 1, 2, 4, 5 | Rename, surface shrink, functional options, binding validation |
| `execution_state.go` | 1, 2 | Rename + unexport |
| `execution_adapter.go` | 1, 2 | Rename + unexport |
| `execution_result.go` | 12 | Helper methods |
| `path.go` | 1, 2 | Rename Path → branch + unexport |
| `path_state.go` | 1, 2 | Rename → branch_state.go |
| `path_local_state.go` | 1, 2 | Rename → branch_local_state.go + unexport |
| `pause.go` | 1, 7 | Rename + AtomicCheckpointer integration |
| `wait.go` | 6 | Delete SignalAware, fold Wait into Context |
| `wait_state.go` | 1, 2 | Rename + unexport |
| `activity.go` | 4, 9 | ActivityRegistry struct, deletion of map alias |
| `activity_functions.go` | 9 | Naming: ActivityFunc, TypedActivityFunc |
| `activity_history.go` | 6 | Delete ActivityHistoryAware, fold History into Context |
| `context.go` | 1, 6 | Rename + property methods + fold helpers |
| `variable_container.go` | 6 | Delete file (folded into Context) |
| `checkpoint.go` | 1, 7 | Rename + SchemaVersion + typed Status |
| `checkpointer.go` | 7 | AtomicCheckpointer optional interface |
| `checkpointer_file.go` | 7 | Schema version handling |
| `runner.go` | 4, 11 | Functional options + completion hook fix |
| `completion_hook.go` | 11 | Godoc fix |
| `errors.go` | 3, 5, 11 | New error constants, classifier cleanup |
| `validate.go` | 3, 5 | Two-phase validation |
| `progress.go` | 6 | Delete ProgressReporter, fold into Context |
| `child_workflow.go` | 1, 10 | Rename + drop Sync field, time.Duration |
| `script_compiler.go` | 8 | Single template syntax |
| `script/*.go` | 8 | Template parser update |
| `activities/*.go` | 9 | Tier split + Duration + delete wait_activity |
| `activities/contrib/` | 9 | New subpackage for shell, file |
| `activities/httpx/` | 9 | New subpackage for http |
| `workflowtest/*.go` | 6 | FakeContext |
| `cmd/workflow/main.go` | 4, 8, 9 | Update to new constructors and templates |
| `examples/*` | 4, 8, 9, 10 | Update everything; add child-workflow example |
| `llms.txt` | 13 | Full rewrite |
| `README.md` | 13 | Quick example rewrite + production checklist |
| `docs/suspension.md` | 13 | New |
| `docs/production_checklist.md` | 13 | New |
| `MIGRATION.md` | 13 | New |

---

**End of plan.** Revise freely. Anything in Part 1 (locked-in decisions) that
we want to change should change here before any code moves.
