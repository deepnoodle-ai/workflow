# Workflow — Combined Public API Review

**Date:** 2026-04-11
**Sources synthesized:** `api_review.md`, `public-api-review.md`, `public_api_review_2026-04-11.md`
**Scope:** `github.com/deepnoodle-ai/workflow` — root module, `script/`, `activities/`, `workflowtest/`, `cmd/workflow/`, `examples/`

This document is the union of three independent reviews, reorganized into a single prioritized plan. Where the reviews disagreed I picked a direction and said why. Where they converged I flagged it — those items are the most certain wins.

The library has a genuinely good core: `Workflow` / `Step` / `Activity` / `Execution` / `Checkpointer` / `Runner`, plus a mature suspension model (Pause / Wait / Sleep / SignalStore / ActivityHistory). The work to reach production-ready is mostly **editorial**: shrink the surface, fail faster, commit to names, and pick one blessed path for each concern.

---

## Part 1 — Executive Summary

### The ten highest-leverage changes (all three reviewers agree or strongly imply)

1. **Collapse the five Run/Execute methods into one primary entry point.** Today: `Run` / `Resume` / `RunOrResume` / `Execute` / `ExecuteOrResume` on `*Execution`, plus `Runner.Run`. Consumers can't tell which to pick.
2. **Promote `Runner` to the front door.** It's the cleanest part of the library and should lead the docs, not appear as an afterthought.
3. **Shrink the exported root surface.** Hide `Path`, `PathState`, `PathSnapshot`, `PathOptions`, `PathSpec`, `JoinState`, `ExecutionState`, `ExecutionAdapter`, `WaitState`, `WaitKind`, `NewSignalWait`, `NewSleepWait`, `NewContext`, `ExecutionContextOptions`, `Patch`/`PatchOptions`/`GeneratePatches`/`ApplyPatches`, `SignalAware`, `ActivityHistoryAware`, `PathLocalState`. These are orchestration plumbing, not product surface.
4. **Fail fast on duplicates and bad bindings.** Duplicate step names, duplicate activity names, and duplicate workflow registrations silently overwrite today. Missing activity references fail only at step-execution time. Reject them at construction.
5. **Stop overloading "Path".** It means four different things (execution branch, dot-notation state address, edge-level branch name, dead `Workflow.Path` field). Rename the execution thread concept to `Branch` and delete the dead field.
6. **Make step kinds explicit.** A `Step` can set `Activity`, `Join`, `WaitSignal`, `Sleep`, `Pause`, and `Each` simultaneously; the engine silently picks one by precedence. Either enforce "exactly one kind" in validation or introduce a `Kind` tag.
7. **Commit to a stable shape for `Checkpoint`** (it is the on-disk wire format for every custom backend) or make it opaque. Today it is the worst of both — fully exported, leaking `PathState`/`JoinState`/`PathCounter`, while still treated as internal.
8. **Normalize the `"state."` prefix convention.** It is sometimes required, sometimes stripped, sometimes optional, across `Step.Store`, `WaitSignalConfig.Store`, `CatchConfig.Store`, and expression templates. Pick one convention and enforce it.
9. **Introduce an `ActivityRegistry` type** to replace `[]Activity` slices. One-time setup, duplicate detection, shared across executions in a worker.
10. **Validate activity-name references and template syntax at construction time.** `Workflow.Validate()` explicitly skips these today; users learn at runtime.

### The five highest-leverage changes after that

11. **Fold `workflow.Wait`, `ReportProgress`, and `ActivityHistory(ctx)` into `Context` methods.** Type-assertion-to-side-interface (`SignalAware`, `ActivityHistoryAware`) is un-Go-like and fragile; method dispatch is compile-time checked.
12. **Rename Context `Get*` methods to property style** (`Logger()` not `GetLogger()`), matching Go stdlib convention.
13. **Split built-in activities into tiers.** `shell`, `file`, `http`, and `print` are not peer primitives. Move risky ones to `contrib/`. Rename or remove `activities.NewWaitActivity` — its name collides with the durable `workflow.Wait` and `SleepConfig`, confusing every new user.
14. **Fix `ChildWorkflowSpec.Sync` redundancy** and other child-workflow inconsistencies (duplicated error channels, `float64` seconds vs `time.Duration` drift between core and the activity wrapper).
15. **Fix the `CompletionHook` contract/behavior mismatch.** Docs say follow-ups are inspectable even on hook error; `Runner.Run` only assigns them when `hookErr == nil`.

### What NOT to change

These are **good** and should stay:

- The core concepts (Workflow, Step, Activity, Edge, Execution, ExecutionResult, Checkpointer, Runner).
- The split between untyped `Activity` and generic `TypedActivity[TParams, TResult]`.
- The durable suspension model (Pause, Wait, Sleep, SignalStore, ActivityHistory, SuspensionInfo).
- `workflowtest` as a separate package following the `httptest` convention.
- `WithFencing` and the `Checkpointer` + `FenceFunc` pattern for distributed workers.
- Structured `ExecutionResult` with `Completed()` / `Failed()` / `Suspended()` / `NeedsResume()` helpers.
- `ExecutionCallbacks` + `BaseExecutionCallbacks` + `CallbackChain`.
- The bundled expr engine as the default `script.Compiler`, with the ability to swap it out.

---

## Part 2 — Priority-ordered recommendations

### 2.1 Shrink the exported surface

The root package exports ~75 identifiers. After a hide pass, that should drop to ~50. Every exported name is a versioning burden and a discovery noise source.

**Hide (unexport or move under `internal/`):**

| Identifier | Where | Why |
|---|---|---|
| `Path`, `PathOptions`, `PathSpec`, `PathSnapshot` | `path.go` | Orchestration threads; consumers never construct these. |
| `PathState`, `JoinState`, `ExecutionState` | `execution_state.go` | Engine state; leaks through `Checkpoint`. See §2.7. |
| `ExecutionAdapter` | `execution_adapter.go` | Internal. |
| `WaitState`, `WaitKind`, `NewSignalWait`, `NewSleepWait` | `wait_state.go` | Internal wait plumbing. |
| `NewContext`, `ExecutionContextOptions` | `context.go:61-94` | Context construction is an engine concern. If tests need this, move it to `workflowtest`. |
| `PathLocalState` | `path_local_state.go` | Internal state container. |
| `Patch`, `PatchOptions`, `NewPatch`, `GeneratePatches`, `ApplyPatches` | `variable_container.go` | Diff plumbing, not consumer API. |
| `SignalAware`, `ActivityHistoryAware` | `wait.go`, `activity_history.go` | Side interfaces that only exist because helpers type-assert. Fold into `Context`. See §2.10. |
| `WaitRequest`, `JoinRequest` | `path.go` | Internal. |

**Keep exported (but document clearly as "not part of stable API if you want to change something"):**

- `History` — users do interact with it via `RecordOrReplay`.
- `WorkflowFormatter` — either document or delete; currently silent.

**Delete outright:**

- `Workflow.Path()` and `Options.Path` field. Never used in examples. Confuses naming. Dead code.
- `ActivityRegistry` as a bare `map[string]Activity` type alias (activity.go:9). Either promote it to a real type (see §2.4) or delete.

### 2.2 Collapse execution entry points

Today's surface:

| Method | Returns | Semantics |
|---|---|---|
| `Execution.Run(ctx)` | `error` | Fresh run |
| `Execution.Resume(ctx, priorID)` | `error` | Resume, fail if no checkpoint |
| `Execution.RunOrResume(ctx, priorID)` | `error` | Resume or fresh |
| `Execution.Execute(ctx)` | `(*ExecutionResult, error)` | Fresh, structured result |
| `Execution.ExecuteOrResume(ctx, priorID)` | `(*ExecutionResult, error)` | Resume or fresh, structured result |
| `Runner.Run(ctx, exec, RunOptions)` | `(*ExecutionResult, error)` | All of the above + heartbeat, timeout, hook |

Six entry points for "run this thing". The `error`-returning variants lose the terminal-vs-infrastructure distinction that `ExecutionResult` carries, so they're strictly less useful once you understand the error model.

**Recommendation (stronger of the three reviews):**

1. Make `Execute` the **only** method on `Execution`. Delete `Run`, `Resume`, `RunOrResume`. Keep `ExecuteOrResume` only if we don't want to fold resume into options.
2. Or, fold resume into options:
   ```go
   // Fresh
   result, err := exec.Execute(ctx)
   // Resume or fresh
   result, err := exec.Execute(ctx, workflow.WithResumeFrom(priorID))
   ```
3. Promote `Runner` to the documented production path. README and llms.txt should lead with it. `Execution.Execute` becomes "for tests and one-off scripts."
4. Mark the old methods `// Deprecated:` for one release, then delete.

CLAUDE.md currently says these signatures are "frozen." That commitment is premature — the library is pre-v1, there are no real consumers to break, and leaving a confusing surface in place just delays the pain. Unfreeze.

### 2.3 Fail fast on duplicates and bad bindings

Multiple silent-overwrite bugs that behave like behavior drift instead of errors:

| Location | Behavior | Fix |
|---|---|---|
| `workflow.New` (workflow.go:61-68) | Duplicate `Step.Name` overwrites earlier entries. | Reject with `ErrDuplicateStepName`. |
| `NewExecution` (execution.go:180-183) | Duplicate `Activity.Name()` overwrites earlier entries. | Reject with `ErrDuplicateActivity`. |
| `MemoryWorkflowRegistry.Register` (child_workflow.go:73-83) | Duplicate `Workflow.Name()` overwrites. | Reject (or make overwrite opt-in via a separate `Replace` method). |
| `NewExecution` | Missing activity reference — `Step.Activity` not in the activity set — fails only when the step is reached. | Validate at construction. |
| `Workflow.Validate` | Does not check edge condition syntax or parameter template syntax. | Compile templates in Validate when a compiler is supplied; attach errors to `ValidationError.Problems`. |
| `Workflow.Validate` | Does not check legal step-kind combinations. | See §2.6. |
| `Workflow.Validate` | Does not check `JoinConfig.Paths` reference real branch names upstream. | Add. |
| `Workflow.Validate` | Does not check `RetryConfig` sanity (e.g., `MaxDelay > BaseDelay`, `MaxRetries >= 0`). | Add. |
| `SleepConfig.Duration` | Enforced at runtime, not validation. | Move to validation. |

**Recommendation:** Introduce a two-phase validation story.

- **Phase 1 (`workflow.New` / `workflow.Define`):** Structural validation only — no activity set, no compiler needed. Duplicate step names, unreachable steps, dangling edges, dangling catch handlers, join path references, legal step-kind combinations, `SleepConfig.Duration > 0`.
- **Phase 2 (`NewExecution`):** Binding validation — requires the activity set and compiler. Activity references resolve, parameter templates compile, edge conditions compile, `WaitSignal.Topic` templates compile.

Both phases should return a `*ValidationError` carrying **all** problems, not the first one. Today's `ValidationError.Problems` structure already supports that; use it.

### 2.4 Introduce an `ActivityRegistry` type

Passing `[]Activity` to every `NewExecution` is tedious, inefficient for workers handling many workflows, and loses duplicate detection.

**Recommendation:**

```go
type ActivityRegistry struct { ... }

func NewActivityRegistry() *ActivityRegistry
func (r *ActivityRegistry) Register(a Activity) error          // rejects duplicates
func (r *ActivityRegistry) RegisterFunc(name string, fn ExecuteActivityFunc) error
func (r *ActivityRegistry) RegisterTyped[T, R any](name string, fn func(Context, T) (R, error)) error
func (r *ActivityRegistry) Get(name string) (Activity, bool)
func (r *ActivityRegistry) Names() []string
```

`ExecutionOptions` / `NewExecution` accepts `*ActivityRegistry` instead of `[]Activity`. Workers build one registry at startup and reuse it. Duplicate detection is mandatory. Binding validation (§2.3) runs against the registry.

For backwards-compat during the transition, `NewExecution` can still accept `[]Activity` and wrap it internally; then deprecate the slice path in the next release.

### 2.5 Resolve the "Path" overload

The word "path" means four different things:

1. **Execution thread** (`*Path` in `path.go`) — a goroutine with its own state copy.
2. **Dot-notation state address** (`state.results.a`) — used in templating, `PathMappings`, and `Output.Path`.
3. **Edge-level branch name** (`Edge.Path string`, step.go:22) — a label for a branched path.
4. **Dead `Workflow.Path()` / `Options.Path` field** — never used in examples, not in llms.txt.

All three reviews flag this. "Branch" is the Go-idiomatic term for #1 and #3. "State path" or "address" is the right term for #2. #4 should be deleted.

**Recommendation:**

- **#1 (execution thread):** Rename `Path` → `Branch`. Cascading: `PathID` → `BranchID`, `PathState` → `BranchState` (if kept public, which it shouldn't be — see §2.1), `PathSnapshot` → `BranchSnapshot`, `PausePath` → `PauseBranch`, `UnpausePath` → `UnpauseBranch`, `PausePathInCheckpoint` → `PauseBranchInCheckpoint`, `ErrPathNotFound` → `ErrBranchNotFound`, `Context.GetPathID` → `Context.BranchID`.
- **#2 (state address):** Keep the word "path" in prose only (e.g., "state paths like `state.results.a`"). Don't introduce a new type. The `JoinConfig.PathMappings` field becomes `JoinConfig.BranchMappings` with string values; the dot-notation within those values is documented as "state paths."
- **#3 (edge-level branch name):** Rename `Edge.Path` → `Edge.BranchName`. `JoinConfig.Paths` → `JoinConfig.Branches`.
- **#4 (dead field):** Delete `Workflow.Path()`, `Options.Path`, and the `path` field on the struct.

This is a big rename but pays off in clarity and should happen before v1.

### 2.6 Make step kinds explicit

`Step` allows multiple mutually exclusive fields:

- `Activity` — execute an activity
- `Join` — wait for branches
- `WaitSignal` — wait for an external signal
- `Sleep` — durable wall-clock wait
- `Pause` — operator hold

Precedence is resolved at runtime in `path.go:383-405` (Join > WaitSignal > Sleep > Pause > Activity). A user who sets both `Activity` and `WaitSignal` on a step has their activity silently ignored — no validation error, no warning.

`Each`, `Retry`, `Catch`, `Store`, `Next` are modifiers and can legally coexist with any kind.

**Two viable approaches, pick one:**

**Option A — Enforce "exactly one kind" in validation.** Simpler, preserves the JSON shape. At `workflow.New`, reject a step that has more than one of `Activity` / `Join` / `WaitSignal` / `Sleep` / `Pause` set. Also reject illegal modifier combinations (e.g., `Retry` on a `Pause` step).

**Option B — Introduce an explicit `Kind` tag.** More intrusive, more self-documenting:

```go
type StepKind string
const (
    StepKindActivity   StepKind = "activity"
    StepKindJoin       StepKind = "join"
    StepKindWaitSignal StepKind = "wait_signal"
    StepKindSleep      StepKind = "sleep"
    StepKindPause      StepKind = "pause"
)

type Step struct {
    Name string
    Kind StepKind  // required; validated at New
    // ... kind-specific fields ...
}
```

Option B is better in every respect except that it breaks every existing JSON definition. Option A is sufficient for v1 if we commit to it.

**Recommendation: Option A now, consider Option B in v2.** At minimum, document the precedence rules in godoc on `Step` so users know what they're getting.

### 2.7 Checkpoint: commit to stable wire format

`Checkpoint` (checkpoint.go:5-21) is a 13-field fully-exported struct that every custom `Checkpointer` implementation must marshal and round-trip. It currently:

- Exposes engine internals via `PathStates map[string]*PathState`, `JoinStates map[string]*JoinState`, `PathCounter int`.
- Uses `map[string]interface{}` instead of `map[string]any`.
- Types `Status` as `string` instead of `ExecutionStatus`.
- Has no `SchemaVersion` field for future migrations.

This is "internal struct treated as public," which is the worst shape.

**Recommendation:** Commit to it being a stable wire format, and act accordingly.

1. Add `SchemaVersion int` (or similar) and document the versioning rule: "new fields can be added at a higher schema version; existing fields are never renamed or retyped in the same version."
2. Change `Status` to `ExecutionStatus`. Change `interface{}` to `any`.
3. Move `PathState` / `JoinState` into their own exported types (after the `Path` → `Branch` rename: `BranchState`, `JoinState`) with explicit godoc saying what backends must preserve across round-trips.
4. Document a "what your custom Checkpointer must round-trip" contract in one place — especially `BranchState.PauseRequested`, `BranchState.Wait`, `BranchState.Variables`. A backend that silently drops any of these breaks pause and suspension correctness.
5. Add an `AtomicUpdate(ctx, execID string, fn func(*Checkpoint) error) error` optional side interface. `PausePathInCheckpoint` can use it when the backend implements it (Postgres row locks, optimistic concurrency) and fall back to load-modify-write otherwise.

If #4 feels too expensive, the alternative is to make `Checkpoint` opaque and force all backends through a `CheckpointSerializer` interface that stores bytes. But that throws away the "easy to build a Postgres Checkpointer with real columns" story, which is one of the library's biggest selling points. Don't do that; commit to Option 1 and mean it.

### 2.8 `ExecutionOptions`: functional options + split required/optional

`ExecutionOptions` has 12 fields (execution.go:59-81). Two are required (`Workflow`, `Activities`). Ten are optional. There is no compile-time enforcement of the required pair, and `WorkflowFormatter` is completely undocumented.

**Recommendation (favored across reviews):**

```go
func NewExecution(wf *Workflow, registry *ActivityRegistry, opts ...ExecutionOption) (*Execution, error)

workflow.WithInputs(map[string]any{"url": "..."})
workflow.WithCheckpointer(cp)
workflow.WithSignalStore(ss)
workflow.WithLogger(logger)
workflow.WithExecutionID("custom")
workflow.WithExecutionCallbacks(cb)
workflow.WithStepProgressStore(store)
workflow.WithActivityLogger(al)
workflow.WithScriptCompiler(c)
workflow.WithResumeFrom(priorID)  // if resume is folded here (§2.2)
```

Required arguments live in the positional slots; optional config lives in functional options. This is the canonical Go pattern and fixes four problems at once:

1. Compile-time enforcement of `Workflow` + `Activities` (now `Registry`).
2. Discoverability via godoc — each `With*` has its own entry.
3. Sensible defaults are invisible to the caller rather than scattered in `NewExecution`.
4. Extensibility: new options don't churn the struct.

`RunnerConfig` and `RunOptions` should follow the same pattern for consistency, though they're smaller and already reasonable.

### 2.9 Context: idiomatic method names, fold helpers in

`Context` currently exposes Java-style `Get*` accessors:

```go
type Context interface {
    context.Context
    VariableContainer
    ListInputs() []string
    GetInput(key string) (any, bool)
    GetLogger() *slog.Logger
    GetCompiler() script.Compiler
    GetPathID() string
    GetStepName() string
}
```

The stdlib uses `Get` only when there's a real verb (e.g., `Header.Get(key)`). Rename to property style:

```go
type Context interface {
    context.Context
    VariableContainer

    Inputs() Inputs        // typed, with Get/Keys
    Logger() *slog.Logger
    Compiler() script.Compiler
    BranchID() string      // post-rename
    StepName() string

    // Folded-in helpers:
    Wait(topic string, timeout time.Duration) (any, error)
    History() *History
    ReportProgress(detail ProgressDetail)
}
```

**Fold in three helpers** that currently live as package-level functions and type-assert the context at runtime:

- `workflow.Wait(ctx, topic, timeout)` → `ctx.Wait(topic, timeout)`
- `workflow.ActivityHistory(ctx)` → `ctx.History()`
- `workflow.ReportProgress(ctx, detail)` → `ctx.ReportProgress(detail)`

This deletes three public package-level helpers AND the `SignalAware` / `ActivityHistoryAware` / `ProgressReporter` side interfaces. All three existed only because the helpers needed runtime dispatch. Method dispatch does the same thing at compile time.

Consumers whose tests use a fake `Context` now need to implement the full interface, which is an acceptable tradeoff for the simpler model. `workflowtest` can provide a `FakeContext` to make this trivial.

**Also consider:** `VariableContainer` methods are `SetVariable` / `GetVariable` / `ListVariables` / `DeleteVariable`. On a type named `VariableContainer` those prefixes are redundant. Shorten to `Set` / `Get` / `Keys` / `Delete`. Or just fold the interface into `Context` and delete `VariableContainer` as a separate type (it has no other implementers in the public API).

### 2.10 Normalize state references and the `"state."` prefix

The `"state."` prefix is inconsistently handled:

- `Step.Store: "state.counter"` works; so does `Step.Store: "counter"` because `path.go:446-459` strips the prefix.
- `WaitSignalConfig.Store` also strips (`path.go:533-537`).
- `CatchConfig.Store` also strips (`path.go:1167-1173`).
- `Output.Variable` expects a bare variable name.
- `Output.Path` selects a branch (concept #3 from §2.5).
- Expression templates use `state.foo` for reads and `inputs.foo` for inputs.

The mental model is fuzzy: users have to remember where the prefix is optional, where it means nested fields, where it means a branch name, and where it's part of a template namespace.

**Recommendation:**

- **Read side (templates):** Keep `state.foo` and `inputs.foo` as template namespaces. They are well-understood and match Step Functions conventions.
- **Write side (config fields):** `Step.Store`, `WaitSignalConfig.Store`, `CatchConfig.Store`, `Output.Variable` all refer to **bare variable names**. Reject strings starting with `state.` in validation (the explicit "this field is a variable, not a path" message is better than the silent strip).
- **Document nested-field stores separately.** If a store string supports `Store: "results.inner"` to write nested, that is a distinct feature that deserves explicit docs, not a silent extension of the bare-name contract.

### 2.11 Simplify templating syntax

Two template syntaxes (`${...}` stringifies, `$(...)` preserves type) differ by one character and cause real confusion. Users default to `${...}`, hit a type mismatch, and don't know why.

**Recommendation:** Single syntax with contextual type inference.

- If the template spans the entire value after trim (`Parameters: {"n": "${state.counter}"}`) → preserve type.
- If the template is interpolated into a larger string (`Parameters: {"msg": "Count: ${state.counter}"}`) → stringify.

This matches most templating engines and removes a whole class of surprise. The detection logic is trivial (check whether tokens consume the whole string).

If single-syntax inference feels too magic, the fallback is: document `${...}` as the primary syntax everywhere, and mention `$(...)` only in an "advanced: type preservation" sidebar. Today both appear in the quick example with no explanation.

### 2.12 Error model cleanup

- **Stop substring-matching `"timeout"`** (errors.go:105). Use `errors.Is(context.DeadlineExceeded)` and `errors.Is(context.Canceled)` only. Require custom timeouts to wrap in `*WorkflowError{Type: ErrorTypeTimeout}` explicitly.
- **Document that `ErrorTypeAll` doesn't match fatal errors** on the `ErrorTypeAll` constant itself, not in a separate paragraph users may miss.
- **Type `WorkflowError.Details`** more narrowly. Today it is `interface{}`; after a round-trip through `Checkpoint.Error string` it's lost. Either serialize it structured or drop it.
- **Prefix error strings consistently.** Some errors use `workflow: ` prefix (good), others don't. Pick one.

### 2.13 Consolidate Pause / Sleep / Wait / Signal documentation

The suspension story has grown to four states (`Waiting`, `Suspended`, `Paused`, plus `Running`), two trigger modes each for pause and wait, three suspension reasons, replay-safety contract, and out-of-process helpers. It's the best-designed feature in the library and also the hardest to learn.

**Recommendation: one dedicated doc.** A `doc.go` or `docs/suspension.md` containing:

1. A table mapping the 3 suspension reasons (`WaitingSignal`, `Sleeping`, `Paused`) to their triggers (imperative, declarative, external) and resume mechanics.
2. A sequence diagram: "worker dies mid-wait → new worker resumes from checkpoint → wait completes."
3. The replay-safety contract in one paragraph, followed by an `ActivityHistory.RecordOrReplay` example.
4. A "how to schedule a resume from Suspension.WakeAt" recipe.
5. The dominant-reason precedence rule (`Paused > Sleeping > WaitingSignal`) — currently buried in `execution.go:504-507`.

No code change required; just pull the scattered docs into one canonical place.

**Minor: what about `SignalStore` being required?** Today `workflow.Wait` fails at runtime if no `SignalStore` was configured. After folding `Wait` into `Context` (§2.9), the error remains a runtime error. Validation (§2.3) should detect that the workflow uses `WaitSignalConfig` or declares a `signal-aware` activity and require `SignalStore` at `NewExecution` time. This is a judgment call — a `SignalStore` set that is never used is harmless, so warning rather than erroring may be preferable.

### 2.14 Child workflows: fix the inconsistencies

Multiple concrete bugs in the child-workflow story:

1. **`ChildWorkflowSpec.Sync bool` is redundant.** `ChildWorkflowExecutor` already has separate `ExecuteSync` and `ExecuteAsync` methods. The `Sync` field on the spec means the same thing twice. Delete it.
2. **`ChildWorkflowResult.Error error`** duplicates the `error` return from `ExecuteSync`. Pick one — I'd keep the return value and drop the struct field, or serialize as `*WorkflowError` to survive JSON.
3. **`activities/child_workflow_activity.go` uses `Timeout float64` seconds** while `ChildWorkflowSpec.Timeout` is `time.Duration`. Align on `time.Duration` everywhere.
4. **The `DefaultChildWorkflowExecutor` cleanup timeout is 5 minutes hardcoded** — arbitrary and broken for any child that runs longer. Make configurable or remove.
5. **Async semantics vs checkpointing are undefined.** If the parent checkpoints while an async child is running, what happens on resume? Document or fix.
6. **No end-to-end "wait for child completion" pattern.** Users have to glue `ExecuteAsync` + `workflow.Wait` + child-emits-signal manually. Document the pattern or provide a helper.

**Recommendation:** Either invest in child workflows properly (checkpoint integration, documented wait-for-child pattern, configurable cleanup) or demote to `experimental/`. The current state is "looks supported, isn't quite" which is the worst option.

### 2.15 Built-in activities: split into tiers

The `activities/` package currently mixes three kinds of thing:

- **Safe primitives:** `print`, `time`, `random`, `json`, `fail`
- **Stdlib-equivalent wrappers:** `http`, `file`
- **Environment-specific / risky:** `shell`
- **Confusing:** `wait` — `activities.NewWaitActivity()` is an in-process `time.After` sleep, confusingly named next to the durable `workflow.Wait` and `SleepConfig`.
- **Core plumbing wrapped as activity:** `workflow.child`

Problems:

- **`activities.NewWaitActivity` is a naming landmine.** A user seeing `activities.NewWaitActivity()` in the import will assume it's the durable wait. It isn't. Remove it, or rename to `NewInProcessSleepActivity` (ugly but honest), or steer users exclusively to `SleepConfig`.
- **`PrintActivity` writes to stdout directly** — fine for demos, bad for production. At minimum, let the caller inject a writer.
- **`HTTPActivity` and `ShellActivity` use `float64` seconds for timeouts** — should be `time.Duration` like everything else.
- **`ShellActivity` and `FileActivity` are broad side-effectful primitives.** Making them feel as blessed as the core orchestration concepts is misleading. Move to `contrib/` or a clearly-marked `experimental/` subpackage.

**Recommendation:**

```
activities/             # safe, in-process, side-effect-free primitives
    print, time, random, json, fail
activities/httpx/       # http client
activities/contrib/     # shell, file, anything environment-specific
```

Delete `NewWaitActivity`. Steer users to `SleepConfig` for durable sleep; for non-durable testing delays, `time.Sleep` inside the activity is fine.

### 2.16 Fix the `CompletionHook` contract bug

- **Docs** (completion_hook.go:13-15): partial follow-ups should still be inspectable on `result.FollowUps` even when the hook returns an error.
- **Code** (runner.go:138-148): `result.FollowUps = followUps` only runs when `hookErr == nil`.

Pick one. Preferred: **always attach any returned follow-ups, then log the hook error separately.** The docs describe useful behavior; the code is more conservative. The docs are right.

### 2.17 `ExecutionResult` helper methods

`ExecutionResult` has `Completed()` / `Failed()` / `Suspended()` / `Paused()` / `NeedsResume()`. Add:

- `OutputString(key string) (string, bool)` — type-safe output access
- `OutputInt(key string) (int, bool)`
- `OutputBool(key string) (bool, bool)`
- `OutputAs[T any](key string) (T, bool)` — generic variant
- `WaitReason() SuspensionReason` — quick access without nil-checking `result.Suspension`
- `Topics() []string` — shortcut for `result.Suspension.Topics`
- `NextWakeAt() (time.Time, bool)` — shortcut for `result.Suspension.WakeAt`

Minor ergonomic polish — no breaking changes — but saves real boilerplate in production consumers.

### 2.18 Runner as the blessed front door

`Runner` (runner.go:56) composes heartbeat, timeout, resume-or-fresh, and completion hook into a single clean `Run(ctx, exec, opts)` call. It is the **best-designed** entry point. Promote it:

1. README "Quick example" uses `Runner`. `Execution.Execute` is only shown in an "if you don't need a runner" sidebar.
2. llms.txt leads the Execution section with `Runner`, not the raw methods.
3. Godoc for `Execution` says: "Most production consumers should create an Execution and pass it to a Runner. Call Execute directly only for tests or custom lifecycle management."
4. The "production checklist" section (§2.22) names Runner as the first item.

### 2.19 Activity API naming

Current: `NewActivityFunction`, `NewTypedActivityFunction`, `NewTypedActivity`, plus the underlying `ActivityFunction` / `TypedActivityFunction` / `TypedActivityAdapter` types.

This is verbose. The Go idiom for "function wrapped as X" is `XFunc`:

**Recommendation:**

- `NewActivityFunction` → `ActivityFunc` (returning `Activity`)
- `NewTypedActivityFunction` → `TypedActivityFunc[T, R]`
- `NewTypedActivity` stays (it wraps a struct, not a function)
- `ActivityFunction` / `TypedActivityFunction` struct types: consider unexporting — they exist only to implement the interface

```go
reg := workflow.NewActivityRegistry()
reg.Register(workflow.ActivityFunc("fetch", fetchFn))
reg.Register(workflow.TypedActivityFunc("parse", parseFn))
```

Short, clear, matches `http.HandlerFunc` / `http.Handler` conventions.

### 2.20 Explicit start step

`workflow.New` uses `opts.Steps[0]` as the start step (workflow.go:83). Simple, but:

- Breaks silently if the order changes.
- Brittle for generated workflow specs.
- Surprising when reading JSON.

**Recommendation:** Add optional `Options.StartAt string`, defaulting to `Steps[0].Name` when omitted. Validate that `StartAt` references an existing step.

### 2.21 `Input.Type` is schema-theater

`Input.Type string` looks like schema but is only documentation — `NewExecution` never enforces it (execution.go:162-177). Users may infer runtime type guarantees that don't exist.

**Recommendation:** Either enforce it (pluggable `InputValidator` interface) or rename to make clear it is documentation metadata (`Input.TypeDoc string` or similar). Enforcement is probably not worth the complexity — call it metadata and move on.

### 2.22 Documentation: production checklist

Add a "Production Checklist" section to llms.txt and README listing the non-obvious-but-essential pieces:

- `Checkpointer` (durable backend, not `FileCheckpointer`)
- `ActivityLogger` (if you want audit)
- `SignalStore` (if you use any waits)
- `WithFencing` (if you have multiple workers per execution)
- `Runner.Heartbeat` (if you use distributed leases)
- `Runner.DefaultTimeout` (prevent runaway workflows)
- `StepProgressStore` (if you want a UI)
- `ExecutionCallbacks` or OTel adapter (for observability)
- "Validate workflows at startup, not at first execution"

One page. Checkbox format. Links to the detailed section for each item.

---

## Part 3 — Concept inventory and proposed stable surface

### 3.1 Keep / reshape / hide / delete

| Category | Keep as public | Reshape | Hide / internal | Delete |
|---|---|---|---|---|
| **Definition** | `Workflow`, `Step`, `Edge`, `Input`, `Output`, `Each`, `RetryConfig`, `CatchConfig`, `JitterStrategy` | `Options` (add `StartAt`), `Step` (add step-kind validation or `Kind`) | — | `Options.Path`, `Workflow.Path()` |
| **Activities** | `Activity`, `TypedActivity`, `ActivityFunc`, `TypedActivityFunc`, `NewTypedActivity`, `ActivityRegistry` (new) | `[]Activity` → `*ActivityRegistry` | `ActivityFunction`, `TypedActivityFunction`, `TypedActivityAdapter` (struct types) | `ActivityRegistry` (the `map[string]Activity` type alias) |
| **Execution** | `Execution`, `ExecutionResult`, `ExecutionStatus`, `ExecutionTiming`, `SuspensionInfo`, `SuspendedPath`, `SuspensionReason`, `FollowUpSpec`, `NewExecutionID` | `ExecutionOptions` (functional options), collapse run methods | `ExecutionState`, `ExecutionAdapter` | `Run`, `Resume`, `RunOrResume` (after deprecation window) |
| **Runner** | `Runner`, `RunnerConfig`, `RunOptions`, `HeartbeatConfig`, `HeartbeatFunc`, `CompletionHook` | Fix `CompletionHook` follow-up bug | — | — |
| **Checkpointing** | `Checkpoint`, `Checkpointer`, `FileCheckpointer`, `NullCheckpointer`, `WithFencing`, `FenceFunc`, `ErrFenceViolation` | `Checkpoint` (SchemaVersion, typed Status, `any`), add optional `AtomicUpdate` side interface | — | — |
| **Pause / Wait / Sleep / Signal** | `PauseConfig`, `SleepConfig`, `WaitSignalConfig`, `SignalStore`, `Signal`, `MemorySignalStore`, `ErrWaitTimeout`, `PauseBranch`, `UnpauseBranch`, `PauseBranchInCheckpoint`, `UnpauseBranchInCheckpoint`, `History`, `SuspensionInfo` | `PausePath*` → `PauseBranch*` rename | `WaitState`, `WaitKind`, `NewSignalWait`, `NewSleepWait`, `PauseRequest`, `SignalAware`, `ActivityHistoryAware`, `IsWaitUnwind` | — |
| **Context** | `Context`, `WithTimeout`, `WithCancel`, `InputsFromContext`, `VariablesFromContext` | Rename `Get*` → properties; fold in `Wait`, `History`, `ReportProgress` | `NewContext`, `ExecutionContextOptions`, `executionContext`, `PathLocalState`, `VariableContainer` (fold into `Context`?), `Patch`, `PatchOptions`, `NewPatch`, `GeneratePatches`, `ApplyPatches` | — |
| **Errors** | `WorkflowError`, `ErrorOutput`, `NewWorkflowError`, `ClassifyError`, `MatchesErrorType`, error type constants, sentinel errors | Type `Details` more narrowly; stop substring matching | — | — |
| **Observability** | `ExecutionCallbacks`, `BaseExecutionCallbacks`, `CallbackChain`, `WorkflowExecutionEvent`, `PathExecutionEvent`, `ActivityExecutionEvent`, `StepProgressStore`, `StepProgress`, `StepStatus`, `ProgressDetail`, `ActivityLogger`, `NullActivityLogger`, `FileActivityLogger`, `ActivityLogEntry` | `PathExecutionEvent` → `BranchExecutionEvent` rename; `ProgressReporter` side interface → fold into Context | `ProgressReporter` | `WorkflowFormatter` (unless documented) |
| **Validation** | `Workflow.Validate`, `ValidationError`, `ValidationProblem` | Add phase-2 validation at `NewExecution`; validate bindings, templates, step-kind combinations | — | — |
| **Child workflows** | `ChildWorkflowSpec` (drop `Sync`), `ChildWorkflowResult`, `ChildWorkflowHandle`, `ChildWorkflowExecutor`, `DefaultChildWorkflowExecutor`, `ChildWorkflowExecutorOptions`, `WorkflowRegistry`, `MemoryWorkflowRegistry` | Fix `Sync` redundancy, align `Timeout` types, document async-vs-checkpoint semantics | — | — |
| **Script** | `script.Compiler`, `script.Script`, `script.Value`, `script.EachValue`, `script.IsTruthyValue`, `DefaultScriptCompiler` | — | — | — |
| **Branching (internal)** | — | — | `Path`, `PathOptions`, `PathSpec`, `PathSnapshot`, `PathState`, `WaitRequest`, `JoinRequest` | — |

### 3.2 Proposed stable public surface (~50 names)

After the hide/rename pass, the root package should expose approximately:

**Definition:** `Workflow`, `Define` (or `New`), `Options`, `Step`, `Edge`, `Input`, `Output`, `Each`/`ForEach`, `RetryConfig`, `CatchConfig`, `JitterStrategy`, `JitterNone`, `JitterFull`, `EdgeMatchingStrategy`, `EdgeMatchingAll`, `EdgeMatchingFirst`

**Step kinds:** `WaitSignalConfig`, `SleepConfig`, `PauseConfig`, `JoinConfig`

**Activities:** `Activity`, `TypedActivity`, `ActivityFunc`, `TypedActivityFunc`, `NewTypedActivity`, `ActivityRegistry`, `NewActivityRegistry`, `ExecuteActivityFunc`

**Execution:** `Execution`, `NewExecution`, `ExecutionOption` (+ `With*` functions), `ExecutionResult`, `ExecutionStatus` (+ constants), `ExecutionTiming`, `SuspensionInfo`, `SuspensionReason` (+ constants), `SuspendedBranch`, `FollowUpSpec`, `NewExecutionID`

**Runner:** `Runner`, `NewRunner`, `RunnerConfig`, `RunOptions`, `HeartbeatConfig`, `HeartbeatFunc`, `CompletionHook`

**Checkpointing:** `Checkpoint`, `Checkpointer`, `FileCheckpointer`, `NewFileCheckpointer`, `NullCheckpointer`, `NewNullCheckpointer`, `WithFencing`, `FenceFunc`

**Pause / Wait / Signals:** `SignalStore`, `Signal`, `MemorySignalStore`, `NewMemorySignalStore`, `History`, `PauseBranch`/`UnpauseBranch` (methods on Execution), `PauseBranchInCheckpoint`/`UnpauseBranchInCheckpoint`

**Context:** `Context` (with `Wait`, `History`, `ReportProgress` as methods), `ProgressDetail`, `WithTimeout`, `WithCancel`

**Errors:** `WorkflowError`, `ErrorOutput`, `NewWorkflowError`, `ClassifyError`, `MatchesErrorType`, `ErrorType*` constants, `ErrNoCheckpoint`, `ErrAlreadyStarted`, `ErrBranchNotFound`, `ErrWaitTimeout`, `ErrFenceViolation`, `ErrDuplicateStepName` (new), `ErrDuplicateActivity` (new), `ErrNilExecution`, `ErrInvalidHeartbeatInterval`, `ErrNilHeartbeatFunc`

**Observability:** `ExecutionCallbacks`, `BaseExecutionCallbacks`, `NewCallbackChain`, `WorkflowExecutionEvent`, `BranchExecutionEvent`, `ActivityExecutionEvent`, `StepProgressStore`, `StepProgress`, `StepStatus` (+ constants), `ActivityLogger`, `ActivityLogEntry`, `NullActivityLogger`, `FileActivityLogger`

**Validation:** `ValidationError`, `ValidationProblem`

**Child workflows:** `ChildWorkflowSpec`, `ChildWorkflowResult`, `ChildWorkflowHandle`, `ChildWorkflowExecutor`, `DefaultChildWorkflowExecutor`, `ChildWorkflowExecutorOptions`, `WorkflowRegistry`, `MemoryWorkflowRegistry`

**Subpackages:**
- `script/` — `Compiler`, `Script`, `Value`, `EachValue`, `IsTruthyValue`, template helpers
- `activities/` — safe primitives only
- `activities/contrib/` — shell, file, etc.
- `workflowtest/` — `Run`, `RunWithOptions`, `MockActivity`, `MockActivityError`, `NewMemoryCheckpointer`, `FakeContext`, `TestOptions`

**Approximate count:** ~55 public names, down from ~75. The deleted/hidden 20+ are all orchestration plumbing that was never product surface.

---

## Part 4 — Phased implementation plan

The pre-v1 window is the cheap time to make breaking changes. Phase A is the breaking-changes window; Phase B is quality polish; Phase C is new features.

### Phase A — Breaking renames and surface shrink (target: `v0.X → v0.(X+1)`)

**Goal:** Every breaking change lands in one version so consumers migrate once.

1. Collapse `Run` / `Resume` / `RunOrResume` into `Execute` + `ExecuteOrResume` (or fold resume into options). Mark old methods `// Deprecated:`. [§2.2]
2. Rename the "Path" execution-thread concept to "Branch" throughout. [§2.5]
3. Delete `Workflow.Path()` and `Options.Path`. [§2.5]
4. Hide orchestration internals: `Path*`, `PathState`, `JoinState`, `ExecutionState`, `ExecutionAdapter`, `WaitState`, `WaitKind`, `NewContext`, `ExecutionContextOptions`, `PathLocalState`, `Patch*`, `SignalAware`, `ActivityHistoryAware`, `WaitRequest`, `JoinRequest`, etc. [§2.1]
5. Rename Context `Get*` methods to property style. [§2.9]
6. Fold `workflow.Wait`, `ActivityHistory(ctx)`, `ReportProgress(ctx, ...)` into `Context` methods. Delete the side interfaces. [§2.9]
7. Introduce `*ActivityRegistry` as the canonical way to register activities. Deprecate raw `[]Activity`. [§2.4]
8. Switch `NewExecution` to functional options (required positional args: workflow, registry). [§2.8]
9. Rename activity-function constructors: `NewActivityFunction` → `ActivityFunc`, `NewTypedActivityFunction` → `TypedActivityFunc`. [§2.19]
10. Rename `workflow.New` → `workflow.Define` (keep `New` as a one-release deprecation alias). [§2.19]
11. Delete `activities.NewWaitActivity`. Steer users to `SleepConfig`. [§2.15]
12. Align `time.Duration` everywhere (`HTTPActivity`, `ShellActivity`, `ChildWorkflowActivity`). [§2.14, §2.15]

### Phase B — Fail-fast, docs, and polish

13. Reject duplicate step names, activity names, workflow registrations. [§2.3]
14. Validate activity-name bindings and template/expression compilation at `NewExecution`. [§2.3]
15. Enforce step-kind mutual exclusivity at `workflow.Define`. [§2.6]
16. Add `Options.StartAt`. [§2.20]
17. Commit to `Checkpoint` as stable wire format: add `SchemaVersion`, type `Status`, document round-trip contract. [§2.7]
18. Add optional `Checkpointer.AtomicUpdate` side interface for Postgres-style backends. [§2.7]
19. Normalize `"state."` prefix handling; reject it on write-side config fields. [§2.10]
20. Fix `ClassifyError` substring matching. [§2.12]
21. Fix `CompletionHook` follow-ups-on-error inconsistency. [§2.16]
22. Split `activities/` into tiers (`activities/contrib/`). [§2.15]
23. Fix child-workflow inconsistencies: drop `Sync` from `ChildWorkflowSpec`, align timeout types, make cleanup configurable. [§2.14]
24. Add `ExecutionResult` helper methods (`OutputString`, `OutputInt`, `WaitReason`, `NextWakeAt`). [§2.17]
25. Add "Production Checklist" section to llms.txt and README. [§2.22]
26. Consolidate suspension docs into one place. [§2.13]
27. Promote `Runner` to the front door in all docs. [§2.18]
28. Single-syntax templating (or explicit primary/advanced framing if single-syntax is too magic). [§2.11]
29. Document or delete `WorkflowFormatter`. [Part 1, "hide list"]

### Phase C — New features (post-v1)

30. Workflow versioning — declare a version on `Workflow`, reject resume on mismatch.
31. Whole-workflow deadlines (`ExecuteBefore time.Time`).
32. Idempotency-key helper (`Context.IdempotencyKey(step string) string`).
33. Compensation / saga pattern for multi-branch rollback.
34. Rate limiting / concurrency caps per activity.
35. `workflow/otel` subpackage shipping an OTel `ExecutionCallbacks` adapter.
36. Step-level timeouts separate from retry timeouts.
37. Global (cross-branch) state with explicit synchronization, OR a documented pattern for it.
38. Expanded `cmd/workflow` reference worker with signal CLI, pause/unpause CLI, Postgres backend example. Or demote `cmd/workflow` to `examples/cli` and build a proper reference worker elsewhere.

---

## Part 5 — Disagreements among reviewers

Where the three reviews took different positions, here's my call:

**Fluent builder API.** One review proposes a chainable `workflow.Define("x").Step("a").Activity(...)...Build()` DSL. I am cold on this. The struct form is primary because JSON round-trip is a first-class goal. A Go-side builder adds a parallel definition path that diverges over time. Skip it.

**Enforcing input types (`Input.Type`).** One review suggests enforcement; another suggests making it clearly metadata. I'd make it metadata and move on. Enforcement adds a schema language to a library that shouldn't have one. Consumers who want validation can validate inputs before `NewExecution`.

**Delete Run/Resume entirely vs. keep as "lower-level."** One review keeps them; another deletes them. **Delete.** Having two entry points with the same grammar for different return semantics is a discovery cliff. A deprecation alias for one release is enough.

**Hide `NewContext` or move to `workflowtest`.** One review wants it gone entirely; another wants it moved to `workflowtest`. **Moved.** Tests legitimately need to construct a fake context, and having a test helper for it is cleaner than forcing every test to implement the full `Context` interface by hand.

**Rename `workflow.New` to `workflow.Define`.** Not unanimous. I mildly prefer `Define` because it reads better at call sites (`workflow.Define(Options{...})` vs `workflow.New(workflow.Options{...})`). Keep `New` as a deprecation alias if the rename happens; otherwise leave it alone — this is the least important rename in Phase A.

**Two-phase validation vs. one.** One review wants everything in `workflow.Validate()`; another wants binding validation in `NewExecution`. **Two-phase.** Structural validation at `Define` (no activity set needed) + binding validation at `NewExecution` (has the registry and compiler). This is the only shape that catches bad activity references before runtime.

**Step "Kind" field vs. precedence-based.** I prefer Option A (enforce exactly-one-kind in validation, preserve struct shape) for v1. Option B (explicit `Kind` tag) is cleaner but breaks every JSON definition. Revisit for v2.

---

## Part 6 — Bottom line

The library's core design is solid. The work to ship a real v1 is almost entirely editorial:

- **Reduce what's public.** (§2.1) Most of the surface is orchestration plumbing that leaked.
- **Pick one door for each concern.** (§2.2, §2.18) Six ways to run a workflow is five too many.
- **Fail earlier.** (§2.3, §2.6) Silent overwrites and runtime-only binding failures are production papercuts.
- **Commit to names.** (§2.5, §2.9, §2.19) "Path" means four things; fold it to one.
- **Stabilize the wire format.** (§2.7) `Checkpoint` is the on-disk contract whether we like it or not.
- **Document the suspension model in one place.** (§2.13) It's the most important feature and the hardest to learn.

Everything else — functional options, helper methods, `ActivityRegistry`, templating inference, error cleanup, child-workflow fixes, production checklist — is polish that compounds. Do it all in one breaking release, call it v1, and the library becomes genuinely recommendable.

---

## Appendix — Source references

**Files read for this review:**

- `README.md`, `llms.txt` (full)
- `workflow.go`, `step.go`, `execution.go` (partial), `runner.go`, `activity.go`, `activity_functions.go`, `context.go`, `errors.go`, `pause.go`, `checkpoint.go`, `variable_container.go`
- Summaries of `path.go`, `wait.go`, `wait_state.go`, `signal_store.go`, `activity_history.go`, `child_workflow.go`, `execution_callbacks.go`, `execution_result.go`, `validate.go`, `checkpointer*.go`
- `examples/` directory listing
- Sibling reviews: `planning/review/api_review.md`, `planning/review/public-api-review.md`, `planning/review/public_api_review_2026-04-11.md`

**Key file:line references cited above:**

- Five run methods: `execution.go:398, 407, 440, 446, 566`
- `ExecutionOptions` 12 fields: `execution.go:60-81`
- `Runner.Run` single method: `runner.go:92`
- `Workflow.Path` dead field: `workflow.go:88-90`
- `Checkpoint` fully-exported struct with `PathState` leakage: `checkpoint.go:5-21`
- Duplicate step overwrite: `workflow.go:61-68`
- Duplicate activity overwrite: `execution.go:180-183`
- Duplicate workflow registry overwrite: `child_workflow.go:73-83`
- Step-kind precedence (not validated): `path.go:383-405`
- `"state."` strip locations: `path.go:446-459, 533-537, 1167-1173`
- `ClassifyError` substring match: `errors.go:105`
- `CompletionHook` docs/behavior mismatch: `completion_hook.go:13-15` vs `runner.go:138-148`
- `ChildWorkflowSpec.Sync` redundancy: `child_workflow.go:13-20` and `:37-47`
- `ChildWorkflowActivity` float64 seconds: `activities/child_workflow_activity.go:12-18`
- `activities.NewWaitActivity` in-process sleep: `activities/wait_activity.go:25-35`
- Context `Get*` methods: `context.go:30-40`
- `VariableContainer` method names: `variable_container.go:8-21`
