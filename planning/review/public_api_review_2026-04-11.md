# Workflow — Public API Review

**Date:** 2026-04-11
**Scope:** `github.com/deepnoodle-ai/workflow` — root module, `script/`, `activities/`, `workflowtest/`, `cmd/workflow/`, `examples/`
**Goal:** Move to production-ready. Keep it simple, powerful, and Go-idiomatic.

This is an opinionated first-principles review. It is deliberately strong on prescription. Not every recommendation must land, but each one represents a real friction point an outside user will hit.

---

## TL;DR — What to fix first

1. **Collapse the five `Run`/`Execute` methods into one.** `Run`, `Resume`, `RunOrResume`, `Execute`, `ExecuteOrResume` is five doors into the same room. Keep one (`Execute`) and delete the rest, or make them one method with an option. This is the single biggest cliff for new users.
2. **Make `Checkpoint` opaque.** Today it is a fully exported struct with 12 JSON-tagged fields, including `PathStates`, `JoinStates`, `PathCounter`. Anyone writing a `Checkpointer` backend has to reach into these and round-trip them correctly. Either make the fields unexported and expose accessors, or commit explicitly that the shape is a stable wire format and document the migration rules.
3. **Stop overloading the word "path".** It means (a) an execution thread after branching, (b) a dot-notation state address (`state.results.a`), (c) the edge-level `Path` name on `Edge`, (d) a filesystem-ish `Path` field on `Workflow` and `Options` that appears to be dead code. Four meanings in the API is too many. Pick one for each concept and rename the others.
4. **Validate activity-name references at `workflow.New()`.** Today `Validate()` explicitly does not check that `Step.Activity` points to a registered activity — you learn at runtime. This is the #1 easy-to-fix reliability cliff.
5. **Hide `Path`, `PathState`, `PathSnapshot`, `ExecutionState`, `Patch` from the root public API.** These are orchestration internals. They leak because `Checkpoint` embeds `PathState` and because `Path` runs in its own goroutine — neither is a reason for them to be in the user-facing surface.
6. **Delete or de-scope `cmd/workflow`.** It is currently a toy loader that nobody would run in production. Either flesh it out as a real reference worker (with SignalStore, Checkpointer, signal CLI commands, pause/unpause commands) or move it to `examples/cli` so its status is clear.
7. **Kill `Workflow.Path()` and `Options.Path` fields.** They are near-dead, never referenced in examples, and guarantee confusion with the `Path` concept.
8. **Runner should be the one obvious entry point.** Promote it. Put it at the top of the README and llms.txt. The current "here's Execution, and by the way there's also this thing called a Runner for production" framing is backwards.

Everything below justifies these in more detail, then goes deeper.

---

## 1. The run/resume method problem

`execution.go` exports five methods that all do variations of "run this workflow":

| Method | Returns | Semantics |
| --- | --- | --- |
| `Run(ctx)` | `error` | Fresh run |
| `Resume(ctx, priorID)` | `error` | Resume, fail if no checkpoint |
| `RunOrResume(ctx, priorID)` | `error` | Resume, fall back to fresh if none |
| `Execute(ctx)` | `(*ExecutionResult, error)` | `Run` + structured result |
| `ExecuteOrResume(ctx, priorID)` | `(*ExecutionResult, error)` | `RunOrResume` + structured result |

The documented rule (llms.txt §**Run vs Execute**) is "the ones returning `error` are old/simple, the ones returning `*ExecutionResult` are newer and better." But they all exist in the same `Execution` type with the same name grammar. A new user picking from the godoc has no way to know `Run` is secretly deprecated.

Worse, the `error` variants are lossy in a genuinely confusing way: a failed workflow returns `nil` from `Run` only in some code paths. From `execution.go:440-498` (`buildResult`), the rule is:

- `error` is populated on "infrastructure" failures (couldn't start, context cancelled before `run()` began).
- Terminal workflow failures are reported on the result, not the error.

That's fine on `Execute`. On `Run`, there is no result — so how does a caller distinguish "infrastructure failure" from "workflow failed"? You can call `execution.Status()` afterward, but now the caller has to remember a two-step protocol for one API.

### Recommendation

Delete `Run`, `Resume`, and `RunOrResume`. Make `Execute` the only method:

```go
// Fresh run
result, err := exec.Execute(ctx)

// Resume (or fresh if no checkpoint)
result, err := exec.Execute(ctx, workflow.WithResumeFrom(priorID))
```

Or, if functional options feel heavy, fold it into `ExecutionOptions`:

```go
exec, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:         wf,
    Activities:       acts,
    ResumeFrom:       priorID,       // "" for fresh
    ResumeBehavior:   workflow.ResumeOrFresh, // or ResumeRequired
})
result, err := exec.Execute(ctx)
```

Either way the external surface shrinks from 5 methods to 1. The `error` variants have never been pulled out into their own binary compat story (the repo is not v1, per `go.mod`), so this break is cheap today and expensive later.

**CLAUDE.md flag:** CLAUDE.md says `Run`/`Resume` signatures are "frozen" and new functionality goes through `Execute`. I disagree with freezing these. Either mark them `// Deprecated:` with a direct pointer to `Execute` or delete them outright. Leaving them silently around teaches wrong habits.

---

## 2. Runner should be the front door

`Runner` (runner.go:56) is excellent. It composes heartbeat, timeout, completion hook, and resume-or-fresh into exactly the API a production worker wants. There is one `Run(ctx, exec, RunOptions)` method. It is the cleanest piece of the library.

But the README and llms.txt both lead with `execution.Run(ctx)`, and Runner is introduced on line 505 of llms.txt as an afterthought. In the published concept list it is ranked below `Path`. This is backwards.

### Recommendation

- Lead README and llms.txt with the Runner.
- In the "Quick example" at the top of both docs, use Runner. Direct `Execute` calls should be framed as "for tests and one-off scripts."
- Add a paragraph in godoc for `Execution` that says "most production code should use `Runner`; `Execution.Execute` is for tests and for consumers that want to manage lifecycle themselves."
- Consider renaming `Execution` → `execution` (unexport) and providing a factory that returns an opaque handle the Runner consumes. This is the heaviest option and probably not worth the churn, but it is the only way to actually guarantee users take the Runner path.

---

## 3. The "Path" overload

`Path` appears in four unrelated meanings across the API:

1. **Execution thread.** `*Path` in path.go. Created by branching. Owns its own state copy.
2. **Dot-notation state address.** `Step.Store` accepts `"state.results.a"` style addresses. The docs and `PathMappings` field (step.go:101) both use the word "path" for this.
3. **Named edge destination.** `Edge.Path string` (step.go:22). Used for joining. A name like `"a"` or `"final"`.
4. **Dead "workflow file path".** `Workflow.Path()` (workflow.go:88) returns an unused `opts.Path` field. Nothing in the examples or tests sets it. llms.txt does not document it.

These mean almost entirely different things. #1 is a concurrency primitive. #2 is a templating convention. #3 is a string identifier. #4 is an undocumented stub.

The failure mode is real: a user reading `PausePath(pathID, reason)` has to stop and remember that `pathID` here refers to concept #1, not #3 (the named-edge "path"), even though both are represented by strings and both appear on the same `Execution` object. And `PathState.ID` vs. an Edge's `Path` are not the same string — branching a workflow with `Edge.Path = "a"` creates a new internal ID that includes `"a"` as a suffix but is not equal to it.

### Recommendation

- **#1 (execution thread):** Rename to `Branch`. `BranchID`, `BranchState`, `BranchSnapshot`, `PausePath` → `PauseBranch`. This is the biggest rename but it pays off because "branch" is the word 90% of users would guess. Everything else downstream (PathMappings, join config) also gets clearer.
- **#2 (dot-notation):** Call these "state paths" in docs. Don't introduce a new type — just consistently use the word "address" or "state path" in prose, never bare "path".
- **#3 (named edge):** Rename `Edge.Path` → `Edge.BranchName`. Then `JoinConfig.Paths` becomes `JoinConfig.Branches` naturally.
- **#4 (filesystem Path):** **Delete.** Both `Workflow.Path()` and `Options.Path`. They are dead. If we want to carry a `SourceFile` at some point, add it later with a clear name.

This is a breaking rename, but it is the change that does the most to make the library learnable. It should happen once, now, before there are many users to migrate.

---

## 4. `Checkpoint` is a leaky struct

```go
type Checkpoint struct {
    ID           string
    ExecutionID  string
    WorkflowName string
    Status       string
    Inputs       map[string]interface{}
    Outputs      map[string]interface{}
    Variables    map[string]interface{}
    PathStates   map[string]*PathState  // leaks PathState
    JoinStates   map[string]*JoinState  // leaks JoinState
    PathCounter  int                    // an implementation counter
    Error        string
    StartTime    time.Time
    EndTime      time.Time
    CheckpointAt time.Time
}
```

(checkpoint.go:5-21)

This is the **on-disk format for every Checkpointer implementation**, and it is fully exported with every field public. Consequences:

1. **Every field is now part of the SemVer contract.** Renaming `PathCounter` to `BranchCounter` is a wire-format break, because a consumer's Postgres-backed Checkpointer will serialize this struct to JSON and round-trip it. So even cosmetic fixes are blocked.
2. **`PathState` and `JoinState` are public by inclusion.** Consumers implementing custom Checkpointers can see and mutate `PathState.PauseRequested`, `PathState.Wait`, etc. That's leakage, but it's also load-bearing: `PausePathInCheckpoint` (pause.go:181) relies on it, so a Postgres-backed Checkpointer that doesn't round-trip `PauseRequested` will silently drop pauses.
3. **`Status` is `string`, not `ExecutionStatus`.** Cosmetic but jarring — consumers who want to compare statuses have to cast.
4. **`map[string]interface{}`** instead of `map[string]any` — still compiles, but dates the code.

There are two defensible directions:

### Option A: commit to Checkpoint as a stable wire format

- Freeze the shape. Document the rules: "new fields can be added; existing fields never renamed or retyped." Publish a migration policy.
- Move `PathState`/`JoinState` into their own exported types with godoc that explicitly describes what backends must round-trip.
- Change `Status` to `ExecutionStatus`. Change `interface{}` to `any`.
- Add a versioning field (`SchemaVersion int`) so future format changes can migrate.

### Option B: make Checkpoint opaque

- Unexport fields. Provide a `CheckpointSerializer` interface for backends:
  ```go
  type CheckpointSerializer interface {
      Marshal(*Checkpoint) ([]byte, error)
      Unmarshal([]byte) (*Checkpoint, error)
  }
  ```
- Default implementation uses JSON. Backends that want their own columns use the fields (via a small "CheckpointFields" accessor) or delegate to the serializer and store bytes.

I'd pick **Option A** because Option B breaks the "easy to write a Postgres Checkpointer" story that is genuinely the library's biggest selling point. But if we go with A, we need to *mean it*: a versioning policy, a changelog section, and discipline about which fields are stable.

Either way, today's state — "public struct that's treated as internal" — is the worst of both worlds.

---

## 5. `ExecutionOptions` is overgrown

```go
type ExecutionOptions struct {
    Workflow           *Workflow
    Inputs             map[string]any
    ActivityLogger     ActivityLogger
    Checkpointer       Checkpointer
    Logger             *slog.Logger
    Formatter          WorkflowFormatter
    ExecutionID        string
    Activities         []Activity
    ScriptCompiler     script.Compiler
    ExecutionCallbacks ExecutionCallbacks
    StepProgressStore  StepProgressStore
    SignalStore        SignalStore
}
```

(execution.go:59-81)

12 fields, 2 required, 10 optional. The struct works, but:

- The **required** ones (`Workflow`, `Activities`) are in the middle of the struct and only discoverable by reading `NewExecution` and finding "is required" errors.
- `Formatter *WorkflowFormatter` is the only field I don't understand from the doc. It isn't mentioned in llms.txt at all. What is it? What's the zero value? Is it safe to leave nil? (From the source it appears it can be, but this is the kind of thing where a reader loses confidence in the whole API.)
- There is no path to discovering that `SignalStore` is "required if and only if you use `workflow.Wait` or a `WaitSignal` step." You find out at runtime.

### Recommendation

Split into **required** positional arguments and **optional** options:

```go
func NewExecution(wf *Workflow, activities []Activity, opts ...ExecutionOption) (*Execution, error)
```

With functional options:

```go
workflow.NewExecution(wf, activities,
    workflow.WithCheckpointer(cp),
    workflow.WithSignalStore(signals),
    workflow.WithInputs(map[string]any{"url": "..."}),
    workflow.WithLogger(logger),
)
```

Functional options are idiomatic in Go for exactly this pattern. The compile-time enforcement "you must pass a workflow and activities" is worth the rename. `WorkflowFormatter` can stay as an option but should be documented or deleted.

If functional options feel too heavy, at minimum move `Workflow` and `Activities` to the top of the struct and add a one-line doc comment for every optional field. The struct-as-options pattern is fine, it just isn't doing its job today.

---

## 6. `Workflow.New` name is awkward

`workflow.New(workflow.Options{...})` reads as "new options-as-struct". The convention in the standard library is `pkgname.New(*Config)` or `pkgname.NewX(...)`. Here, the natural call is:

```go
wf, err := workflow.New(workflow.Options{Name: "demo", Steps: ...})
```

which has three repetitions of "workflow" in one line. Minor but grating.

Two cleaner options:

1. **Rename to `workflow.Define`.** `workflow.Define(Options{...})`. Reads better, clearly says "this is building a definition, not starting an execution."
2. **Accept positional required args:** `workflow.New(name string, steps []*Step, opts ...DefineOption) (*Workflow, error)`.

I'd pick **Define** — one word, zero breaking churn beyond the rename.

---

## 7. `Store` is a silent hack

`Step.Store string` (step.go:108) receives the activity's return value. The convention is that `Store: "state.result"` saves under variable `result`; the `state.` prefix is optional and stripped. This is mentioned in a footnote in llms.txt:990 and the README does not explain it.

Problems:

- **Silent stripping.** Users who write `Store: "result"` and `Store: "state.result"` both work. One is not wrong, but there is no documentation of why the prefix exists.
- **Cross-concept with templating.** `${state.result}` is how you *read* a variable; `Store: "state.result"` is how you *write* one. These use the same prefix syntax for different reasons.
- **No type.** Just a `string`. No validation. Typos only surface at runtime.

### Recommendation

Either (a) drop the `state.` prefix entirely — make `Store` name the variable directly and adjust templating syntax to match; or (b) document the prefix prominently and validate that only `state.*` is accepted at `Validate()` time. I lean toward (a): the prefix adds nothing except consistency with the reader side, and the reader side is itself a templating quirk (`state.*` vs `inputs.*` vs future namespaces).

Going even further: `Store` could be replaced with a typed helper or a specific expression:

```go
Step{
    Activity: "fetch",
    StoreAs:  "result",   // just a variable name, no prefix
}
```

---

## 8. `Each` is under-specified

`Each` (step.go:26) has two fields: `Items any` and `As string`. The docs say it loops over a list. It's not clear from the type:

- Can `Items` be a template like `"${state.batch}"`? (Yes, but the type is `any`, not `string`.)
- What does `As` do? (Binds each item to a variable name for the step body. Not documented on the type.)
- Is the loop parallel or sequential? (Needs to be checked in source. My read says sequential.)
- What happens to state across iterations — does each iteration see the previous one's mutations?
- Is there an index variable?

For a step that might be one of the most-used features in real workflows, this type is under-documented and has the wrong shape: it should probably be either a `ForEach` step type or much better godoc.

### Recommendation

- Rename to `ForEach` (unambiguous, matches Step Functions).
- Add fields: `As string` (item var), `IndexAs string` (index var), `Mode` (`Sequential` or `Parallel`). Default sequential for backward compatibility.
- Godoc with an example. 10 lines of godoc fixes 80% of the confusion.

---

## 9. `Activity` vs `Step` vs "activity name"

Three entities, one name collision:

- `Step` — the graph node
- `Activity` — a Go interface with `Name()` and `Execute()`
- `Step.Activity string` — the name lookup key from step to activity

The string field is called `Activity` even though it is a *name*, not an activity. Writing `Step{Activity: someActivity}` does not compile. You write `Step{Activity: "my_op"}`. The right name for this field is `ActivityName` or just `Activity` renamed to `Do` or similar.

Minor, but an easy rename:

```go
type Step struct {
    Name         string
    ActivityName string    // was: Activity
    Parameters   map[string]any
    ...
}
```

Or flip the polarity and call the interface `Action` and the field `Action`:

```go
type Action interface {
    Name() string
    Execute(ctx Context, ...) (any, error)
}

type Step struct {
    Name   string
    Action string  // name of the Action to execute
    ...
}
```

I don't love `Action`, but `Activity` vs `Activity` on the same type is confusing enough that some rename is warranted.

---

## 10. Context interface is Java-style

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

(context.go:16-41)

Four `Get*` methods. Go's convention is `Logger()`, not `GetLogger()`. The stdlib uses `Get` only when the verb matters (e.g., `http.Request.Header.Get(key)`). These should all be property-style:

```go
type Context interface {
    context.Context
    VariableContainer

    Inputs() Inputs       // not ListInputs/GetInput — a real type with methods
    Logger() *slog.Logger
    Compiler() script.Compiler
    BranchID() string     // post-rename
    StepName() string
}
```

Also: `ListInputs` + `GetInput` is an awkward pair. Either use a small `Inputs` type:

```go
type Inputs interface {
    Get(key string) (any, bool)
    Keys() []string
    Len() int
}
```

or just return `map[string]any` (it's already a read-only snapshot in practice). The split-up methods serve no purpose.

The `VariableContainer` interface itself has the same `SetVariable`/`GetVariable`/`ListVariables` naming. In Go this is usually just `Set`/`Get`/`Keys` on a named type. Since the type is already named `VariableContainer`, the method names are redundant — `container.SetVariable(k, v)` could be `container.Set(k, v)` and read more naturally.

---

## 11. `Wait` + `SignalAware`: runtime type assertion is a smell

`workflow.Wait(ctx, topic, timeout)` (wait.go) looks at the context and type-asserts it to `SignalAware` (wait.go:64-73). If the assertion fails, it returns an error at runtime. This is fragile:

- An activity that calls `Wait` in a unit test where the test harness supplies a plain context will silently fail.
- There is no compile-time guarantee.
- The `SignalAware` side interface is public so that custom context implementations can satisfy it — but almost nobody will want a custom context.

### Recommendation

Make `Wait` a method on `Context` (or on a smaller `Waiter` interface). Users call `ctx.Wait(topic, timeout)`. Compile-time checked. No type assertion. If `SignalStore` was not configured on `ExecutionOptions`, the method returns an error — but the method exists.

Same treatment for `ActivityHistory(ctx)` and `ReportProgress(ctx, detail)`: make them methods on `Context`. These function-style helpers that type-assert are a Java pattern, not Go.

```go
type Context interface {
    context.Context
    VariableContainer

    // ...
    Wait(topic string, timeout time.Duration) (any, error)
    History() *History
    ReportProgress(detail ProgressDetail)
}
```

This tightens the contract and removes three public helpers (`Wait`, `ActivityHistory`, `ReportProgress`) from the root namespace.

---

## 12. `${...}` vs `$(...)` templating is clever but a trap

Two syntaxes: `${expr}` stringifies, `$(expr)` preserves type. Discovered in llms.txt §**String templating**.

The problem: users almost never read that paragraph. They see `${state.counter}` in examples, use it everywhere, and then wonder why a numeric compare fails with a type error. The distinction is real (you genuinely need both behaviors), but two syntaxes that differ by one character is not the way.

### Recommendation

Use a **single syntax** with type inference based on context:

- If the template is the entire value (`Parameters: {"n": "${state.counter}"}`), preserve type.
- If the template is interpolated into a larger string (`Parameters: {"msg": "Count: ${state.counter}"}`), stringify.

This matches how most templating engines behave (Pongo, Liquid, JSX). It removes a whole class of surprise. The machinery to detect "is this the entire value or interpolated" is straightforward: check whether the template tokens span the whole string after trim.

If inference is too magic for the author's taste, at minimum pick one primary syntax for the docs and quick example, and only mention the other in an advanced section.

---

## 13. Error model has sharp edges

The error system is one of the more mature parts of the library, but there are still issues:

- **`ClassifyError` string-matches on `"timeout"`** (errors.go:105). If an underlying library writes `"operation timed out"`, is that caught? Yes, because `strings.Contains(..., "timeout")`. Substring matching an error message is a code smell — two libraries with different error text can diverge in behavior.
- **`ErrorTypeActivityFailed` is the default classification.** This is documented (errors.go:39-44) but conflicts with what most users expect: they think "I wrote `return fmt.Errorf(...)`, my workflow failed, catch handler matches `all`" — and that's what happens, so fine. But if they then write a custom error with `type: "my-error"`, they're surprised catch handlers with `ErrorEquals: ["my-error"]` don't match unless they use `WorkflowError` explicitly. Document more prominently.
- **`WorkflowError.Details interface{}`** is fine but never typed. What can it be? Anything? Is it serialized through the Checkpointer? (Yes — `Error` is stored as a string in `Checkpoint`, not as a structured value.) A round trip through JSON loses structure. If Details is actually meant to be preserved, store the structured error.
- **`ErrorTypeFatal` classification is subtle.** From errors.go:136: "Fatal errors are only matched by the `ErrorTypeFatal` pattern." Good, but users often set `Catch: [{ErrorEquals: ["all"]}]` and expect it to catch literally everything — including fatal. This is a reasonable design decision, but it needs a big warning in the docs.

### Recommendation

- Stop substring-matching on `"timeout"`. Either use `errors.Is(context.DeadlineExceeded)` only, or require that timeouts be wrapped in `*WorkflowError{Type: "timeout"}` explicitly.
- Document the "all does not match fatal" rule on the `ErrorTypeAll` constant itself, not in a separate paragraph.
- Consider typing `Details` more narrowly — `map[string]any` at minimum, or a `Details` interface.

---

## 14. Pause + Sleep + Wait are a coherent story, but their docs live in three places

This is the best-designed feature in the library, per CLAUDE.md. It is also the feature with the highest cognitive load for a new user. There are:

- **Four states:** `Running`, `Waiting` (intra-run join), `Suspended` (durable wait, goroutine exited), `Paused` (operator hold)
- **Two trigger modes for pause:** external `PausePath`, declarative `Pause` step
- **Two trigger modes for waits:** imperative `workflow.Wait`, declarative `WaitSignal` step
- **Two out-of-process helpers:** `PausePathInCheckpoint`, `UnpausePathInCheckpoint`
- **Two suspension reasons besides pause:** `WaitingSignal`, `Sleeping`
- **Replay-safety contract:** activities may re-run from entry. Wrap non-idempotent work in `ActivityHistory.RecordOrReplay`.

This is genuinely the right feature set. The docs for it, however, are scattered across `pause.go` godoc, llms.txt §**Signals, waits, pausing, and durable sleep**, and `planning/prds/002-signals-waits-pausing.md`. A new user needs a one-page diagram.

### Recommendation

- Add a single dedicated `doc.go` or markdown file titled "Suspension, signals, and durable waits" that contains:
  - A table mapping the 3 suspension reasons to their triggers and resume mechanics.
  - A sequence diagram showing "worker crashes mid-wait → new worker resumes from checkpoint → wait completes".
  - The replay-safety contract (one paragraph, then an example with `ActivityHistory`).
  - "How to schedule a resume" recipe using `Suspension.WakeAt`.
- Collapse `SuspensionInfo.Reason` doc: "dominant reason precedence (Paused > Sleeping > WaitingSignal)" is surprising enough that it deserves a three-line comment in `execution_result.go` too, not just one in `execution.go:504-507`.

No code change needed here — just docs.

### Minor: the name `PausePath` conflicts with the rename

If we rename execution threads to "branch", `PausePath` becomes `PauseBranch`. Then the dot-notation state-path concept is free to be called "path" unambiguously. This is another reason the rename is worth it.

### Minor: `PausePathInCheckpoint` is a non-atomic load-modify-write

Documented (pause.go:162). Fine as a contract, but it deserves a ceiling on the Checkpointer interface — something like `AtomicUpdate(ctx, execID, fn func(*Checkpoint) error)` that production Postgres backends can implement more safely. Leave the current function as a shim that calls `AtomicUpdate` if the backend implements it, otherwise falls back to load-modify-write.

---

## 15. Validation is too shallow

`Workflow.Validate()` (validate.go) checks:
- Unreachable steps
- Invalid join path references
- Dangling catch handler references

It does NOT check:
- Activity name references (Step.Activity → registered activity)
- Edge condition syntax (a malformed `"state.x >> 10"` succeeds at Validate and explodes at runtime)
- Template parameter syntax (`"${state."` unclosed template)
- `WaitSignal` topic template syntax
- `JoinConfig.Paths` referring to branch names that exist in `Next` edges upstream
- Sleep.Duration being non-zero (currently enforced at runtime)
- `RetryConfig` fields (e.g., MaxDelay > BaseDelay)

Activity-name validation is the most impactful miss. It requires having the activity set at validation time, which means either:

1. Move validation to `NewExecution` (when `Activities` is known). Validate structure in `New()`, validate references in `NewExecution()`.
2. Add a two-phase `Validate(activities []Activity)` method.

### Recommendation

- Validate structure at `workflow.New`. It already does this.
- Add `ValidateAgainst(activities []Activity, compiler script.Compiler) error` as an optional second-phase check called by `NewExecution`. Fail early, with structured `ValidationError.Problems` so users see all issues at once.
- Compile templates in Validate when a compiler is supplied. `expr` compile errors are very cheap and catching them at startup is enormous value.

---

## 16. Concept inventory — what's essential, what's redundant

| Concept | Essential? | Well-named? | Notes |
|---|---|---|---|
| `Workflow` | ✅ | ✅ | Keep. Consider `Define` constructor. |
| `Step` | ✅ | ✅ | Keep. |
| `Edge` | ✅ | ✅ | Drop `Edge.Path`, replace with `BranchName`. |
| `Activity` (interface) | ✅ | 🟡 | Collides with `Step.Activity string`. |
| `TypedActivity` | ✅ | ✅ | Keep. |
| `ActivityFunction` / `TypedActivityFunction` | ✅ | 🟡 | Two constructors for the same thing; consider folding. |
| `Execution` | ✅ | ✅ | Shrink API surface (§1). |
| `ExecutionOptions` | ✅ | 🟡 | Split required/optional (§5). |
| `ExecutionResult` | ✅ | ✅ | Keep. Good shape. |
| `ExecutionStatus` | ✅ | ✅ | Keep. |
| `SuspensionInfo` | ✅ | ✅ | Keep. Excellent design. |
| `FollowUpSpec` | 🟡 | ✅ | Keep but document better. Currently only used by CompletionHook. |
| `Runner` | ✅ | ✅ | Promote to front door (§2). |
| `RunnerConfig` / `RunOptions` | ✅ | ✅ | Keep. |
| `HeartbeatConfig` / `HeartbeatFunc` | ✅ | ✅ | Keep. |
| `CompletionHook` | ✅ | ✅ | Keep. |
| `Checkpoint` | ✅ | ✅ | Stabilize or hide (§4). |
| `Checkpointer` | ✅ | ✅ | Keep. Consider `AtomicUpdate`. |
| `FileCheckpointer` / `NullCheckpointer` | ✅ | ✅ | Keep. |
| `WithFencing` / `FenceFunc` / `ErrFenceViolation` | ✅ | ✅ | Keep. Document prominently. |
| `Pause*` family | ✅ | ✅ (post-rename) | Keep. Rename `PausePath` → `PauseBranch`. |
| `Wait` function | 🟡 | 🟡 | Fold into Context method (§11). |
| `WaitSignalConfig` / `SleepConfig` / `PauseConfig` | ✅ | ✅ | Keep. |
| `WaitState` / `WaitKind` / `NewSignalWait` / `NewSleepWait` | ❌ | — | Make internal. No reason consumers need to see these. |
| `SignalStore` / `Signal` | ✅ | ✅ | Keep. |
| `MemorySignalStore` | ✅ | ✅ | Keep. |
| `SignalAware` | ❌ | — | Delete. Fold into Context. |
| `History` / `ActivityHistory(ctx)` / `ActivityHistoryAware` | ✅ / ✅ / ❌ | 🟡 | Keep History; fold ActivityHistory into Context method; delete the `Aware` interface. |
| `Context` | ✅ | ✅ | Rename Get* methods (§10). |
| `VariableContainer` | ✅ | 🟡 | Shorten method names (§10). |
| `Patch` / `PatchOptions` / `GeneratePatches` / `ApplyPatches` | ❌ | — | Hide. Internal diffing, not consumer-facing. |
| `PathLocalState` | ❌ | — | Hide. Consumers should never construct one. |
| `Path` / `PathState` / `PathSnapshot` / `PathOptions` | ❌ | — | Hide from root public API. Post-rename. |
| `ExecutionState` | ❌ | — | Hide. Explicitly internal per file comment. |
| `ExecutionAdapter` | ❌ | — | Hide. |
| `JoinConfig` / `JoinState` | ✅ (Config) / ❌ (State) | ✅ | Hide JoinState. |
| `Each` | ✅ | 🟡 | Rename `ForEach`. |
| `Retry*` / `Catch*` / `JitterStrategy` | ✅ | ✅ | Keep. |
| `WorkflowError` / `ErrorOutput` / `ClassifyError` / `MatchesErrorType` | ✅ | ✅ | Keep. Stop substring-matching timeouts. |
| Error sentinels (`ErrNoCheckpoint`, `ErrAlreadyStarted`, `ErrPathNotFound`, `ErrWaitTimeout`, `ErrNilExecution`, `ErrInvalidHeartbeatInterval`, `ErrNilHeartbeatFunc`, `ErrFenceViolation`) | ✅ | ✅ | Keep. |
| `ExecutionCallbacks` / `BaseExecutionCallbacks` / `CallbackChain` | ✅ | ✅ | Keep. |
| `ExecutionEvent` structs | ✅ | ✅ | Keep. |
| `ActivityLogger` / `NullActivityLogger` / `FileActivityLogger` / `ActivityLogEntry` | 🟡 | ✅ | Keep but reconsider whether this overlaps with `StepProgressStore`. |
| `StepProgressStore` / `StepProgress` / `StepStatus` / `ProgressDetail` / `ProgressReporter` | ✅ | ✅ | Keep. Fold `ReportProgress` helper into Context method. |
| `ChildWorkflowSpec` / `ChildWorkflowResult` / `ChildWorkflowHandle` / `ChildWorkflowExecutor` | ✅ | ✅ | Keep but see §18. |
| `DefaultChildWorkflowExecutor` / `ChildWorkflowExecutorOptions` | ✅ | ✅ | Keep. |
| `WorkflowRegistry` / `MemoryWorkflowRegistry` | ✅ | ✅ | Keep. |
| `WorkflowFormatter` | ❓ | 🟡 | Undocumented; either document or remove. |
| `script.Compiler` / `script.Script` / `script.Value` | ✅ | ✅ | Keep. |
| `script.EachValue` / `script.IsTruthyValue` | ✅ | ✅ | Keep. |
| `DefaultScriptCompiler` | ✅ | ✅ | Keep. |
| `NewExecutionID` | ✅ | ✅ | Keep. |
| `WithTimeout` / `WithCancel` (Context flavors) | 🟡 | ✅ | Keep but consider whether these should be on Context directly. |
| `InputsFromContext` / `VariablesFromContext` | 🟡 | 🟡 | Could be `Context.InputsSnapshot()` / `VariablesSnapshot()`. |
| `ActivityRegistry` type | ❓ | — | Exported but barely used. Delete? |

**Rough tally**: ~75 exported types and functions. After the hide/rename pass, we should be able to cut this to ~50. The remaining ~50 is genuinely what a production user needs.

---

## 17. What's missing

Concepts I would expect in a production workflow engine that aren't here (or are half-implemented):

1. **Whole-workflow deadlines.** `Runner.DefaultTimeout` is per-execution wall-clock. There is no "this workflow must finish by `time.X` or be aborted and marked `DeadlineExceeded`". For SLA-driven use cases this is the single most requested feature in similar libraries.
2. **Rate limiting / concurrency caps per activity.** Nothing stops a workflow from launching 1000 parallel branches that each hit `http_call`. A Runner-level or Activity-level semaphore would help.
3. **Idempotency keys as a first-class concept.** Today `ActivityHistory.RecordOrReplay` covers this, but idempotency keys at the HTTP/LLM level are more common. A `Context.IdempotencyKey(step string)` that returns a stable key across replays would save the user a lot of boilerplate.
4. **Compensation / saga pattern.** No declarative support for "on failure of path B, run rollback for path A". Consumers implement by hand in catch handlers, and it's awkward.
5. **Workflow versioning.** What happens when you restart a worker with a new workflow definition and load a checkpoint from the old version? Today: undefined. Workflows should declare a version and resume should fail loudly on mismatch.
6. **Global state.** Every path gets its own copy. Sometimes you want a shared counter or a shared batch ID across branches. Today you'd have to serialize through a join. A `Workflow.SharedState` with explicit synchronization would be useful — or at least a documented pattern.
7. **Schedules.** No cron / recurring trigger. Out of scope for a pure execution engine, per CLAUDE.md — but worth a one-line docs note telling users to use `cron` + `Runner` and pointing to an example.
8. **Introspection during resume planning.** A consumer scheduling a resume from a suspended checkpoint has `Suspension.WakeAt` and `Suspension.Topics` — good. But they don't have easy access to "what variables does this path carry?" without loading the whole checkpoint and reaching into `PathStates[...].Variables`. A `Checkpointer.Snapshot(execID) (*ExecutionSnapshot, error)` read-side helper with a curated shape would be cleaner.
9. **OpenTelemetry integration.** `ExecutionCallbacks` is the escape hatch, but every real user will end up writing the same OTel adapter. Ship a default in a subpackage (`workflow/otel`).

---

## 18. Child workflows: half-finished

From `child_workflow.go`, the story is:

- `ChildWorkflowSpec` describes what to launch.
- `ChildWorkflowExecutor` has `ExecuteSync`, `ExecuteAsync`, `GetResult`.
- `DefaultChildWorkflowExecutor` wraps an in-memory registry.
- `ChildWorkflowHandle` is a reference to an async run.

Problems:

- **Async is opaque.** `ExecuteAsync` returns a handle; you poll with `GetResult`. There is no way to wait for completion inside the parent — you'd have to use `workflow.Wait` on a signal the child emits. That's workable but undocumented.
- **The Default executor has a 5-minute cleanup timeout** (reported by the search agent). That's a bug-waiting-to-happen for any child workflow that runs longer.
- **No integration with checkpointing.** If a parent checkpoints while a child is running, what happens? Unclear.
- **No example end-to-end.** `examples/child_workflows` exists; worth auditing for how closely it matches real usage.

### Recommendation

- Either invest in child workflows properly (checkpoint integration, documented lifecycle, wait-for-child-signal pattern) or demote to an "experimental" subpackage (`workflow/experimental/children`). The current in-between state is the worst option because users will try to use it and hit friction.
- At minimum, document in llms.txt exactly how sync vs async works and what "ParentID" buys you.

---

## 19. Small nits (keep for a polish pass)

1. **`map[string]interface{}`** in `Checkpoint`. Should be `map[string]any`. (checkpoint.go:11)
2. **`Options.State` vs `Workflow.InitialState()`** — two spellings for the same concept. Use one.
3. **`WorkflowFormatter`** — not documented in llms.txt. Either document or remove.
4. **`ActivityRegistry` type alias** (activity.go:9) — barely used. Delete or expand.
5. **`Input.Default interface{}`** — should be `any`.
6. **`EdgeMatchingStrategy`** — fine name, but the constants `EdgeMatchingAll` / `EdgeMatchingFirst` are string-valued (`"all"` / `"first"`). No reason for string values — use `iota`. Only reason to use strings is JSON round-trip, which is real but can be handled with `UnmarshalJSON`.
7. **`JitterStrategy`** uppercase values (`"NONE"` / `"FULL"`). Inconsistent with `EdgeMatchingStrategy`'s lowercase. Pick one convention.
8. **Error strings not prefixed.** Many errors (e.g., `fmt.Errorf("workflow is required")` at execution.go:138) lack the standard Go-style `workflow: ` prefix. `ErrPathNotFound` does have it. Be consistent.
9. **`Step.Store` has two magic prefixes.** `"state."` on `Step.Store` and on `CatchConfig.Store`. Consistent, but undocumented unless you read llms.txt §**State management** closely.
10. **`executionContext` is not `Context` ergonomically.** The concrete type is named `executionContext` (lowercase), implements `Context`. Fine, but `NewContext` returns `*executionContext` (exported constructor returning unexported type — a lint warning). Either the type is public or the constructor is internal. Pick.
11. **`Formatter` field on ExecutionOptions** and **`WorkflowFormatter` interface** — needs a doc comment or deletion.
12. **`Workflow.Path()` method** returns the unused `path` field. Delete.
13. **`Options.Path` field** in definition. Delete.
14. **Pointers vs values.** Many fields are `*Input`, `*Output`, `*Step` — pointers to avoid copy. But `Each`, `Join`, `WaitSignal`, `Sleep`, `Pause` are also all pointers because "optional means nil." Fine, but `RetryConfig` and `CatchConfig` are `[]*RetryConfig` — arrays of pointers. That's a slightly odd shape; `[]RetryConfig` would work and eliminate one layer of indirection. Not a hill to die on.
15. **`Heartbeat` is a pointer** on `RunOptions` but could be `HeartbeatConfig` (zero-value means disabled if `Interval == 0`). More idiomatic.

---

## 20. Recommended phased plan

Since the library is pre-v1, there's still time to make breaking changes cheaply. Three phases:

### Phase A — the breaking renames (one version, tagged `v0.X → v0.Y`)
1. Collapse Run/Execute methods (§1).
2. Rename `Path` → `Branch` across the orchestration side (§3). Keep `Edge.BranchName`.
3. Delete `Workflow.Path()` / `Options.Path` (§3, §19 #12-13).
4. Rename `workflow.New` → `workflow.Define` (§6). Keep `New` as a deprecated alias for one release.
5. Hide `Path`, `PathState`, `PathSnapshot`, `ExecutionState`, `ExecutionAdapter`, `PathLocalState`, `Patch`, `JoinState`, `WaitState`, `WaitKind`, `SignalAware`, `ActivityHistoryAware` (§16).
6. Rename Context `Get*` → property style (§10).
7. Fold `workflow.Wait` + `ActivityHistory(ctx)` + `ReportProgress` into Context methods (§11).

### Phase B — the quality pass
8. Validate activity names at `NewExecution` (§15).
9. Add functional options for `NewExecution` (§5).
10. Decide Checkpoint is stable-wire-format; document versioning (§4).
11. Fix `ClassifyError` substring matching (§13).
12. Document Suspension in one dedicated file (§14).
13. Promote Runner in docs (§2).
14. Delete dead fields (`WorkflowFormatter` or document it, `ActivityRegistry`, etc.).

### Phase C — the feature pass (post-v1)
15. Workflow versioning (§17 #5).
16. OpenTelemetry subpackage (§17 #9).
17. Idempotency key helper (§17 #3).
18. Compensation / saga support (§17 #4).
19. Either invest in child workflows or demote (§18).

Phase A is where the library earns its v1. Phase B polishes. Phase C competes with Temporal-for-Go.

---

## Appendix A — verification notes

The following were verified against source rather than taken from docs:

- 5 run/execute methods on `*Execution`: confirmed (execution.go:398, 407, 440, 446, 566).
- `Checkpoint` fully exported struct with `PathStates map[string]*PathState`: confirmed (checkpoint.go:14).
- `Workflow.Path()` returns `w.path`, which is set from `opts.Path` and never read elsewhere in examples: confirmed (workflow.go:88-90).
- `Validate()` does not check activity names: confirmed (llms.txt:402).
- `ClassifyError` substring-matches `"timeout"`: confirmed (errors.go:105).
- `ExecutionOptions` has 12 fields: confirmed (execution.go:60-81).
- `Runner.Run` is the only method on Runner and composes heartbeat+timeout+resume+hook cleanly: confirmed (runner.go:92).
- `PausePathInCheckpoint` is a non-atomic load-modify-write: confirmed (pause.go:181-213).
- `Context` has four `Get*` methods: confirmed (context.go:30-40).
- `VariableContainer` uses `SetVariable`/`GetVariable`/`ListVariables`/`DeleteVariable`: confirmed (variable_container.go:8-21).
- `workflow.New` exists and requires `Options.Name`, `Options.Steps`: confirmed (workflow.go:53).

Everything else in this review comes from reading file content listed above plus the upstream search summary.
