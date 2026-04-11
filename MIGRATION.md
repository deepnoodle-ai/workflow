# Migration to v1

This document lists every breaking change between the pre-v1 API and
v1, with before/after snippets. Pre-v1 had no compatibility promise,
so the changes are listed by area rather than by deprecation tier —
update everything in one pass.

## 1. `Path` → `Branch` rename (PR1)

Every public identifier that referred to a "path" through the step
graph is now "branch."

| Before                          | After                              |
| ------------------------------- | ---------------------------------- |
| `PathState`                     | `BranchState`                      |
| `PathSnapshot`                  | `BranchSnapshot`                   |
| `JoinConfig.Paths`              | `JoinConfig.Branches`              |
| `JoinConfig.PathMappings`       | `JoinConfig.BranchMappings`        |
| `Output.Path`                   | `Output.Branch`                    |
| `Edge.PathName`                 | `Edge.BranchName`                  |
| `Checkpoint.PathStates`         | `Checkpoint.BranchStates`          |
| `Context.PathID()`              | `Context.BranchID()`               |
| `PauseBranch` / `UnpauseBranch` | unchanged (already used "branch")  |

Checkpoints written by older versions cannot be loaded — the JSON
field name `path_states` does not match `branch_states`. Re-run any
in-flight executions from scratch, or convert by hand.

## 2. Surface shrink (PR2)

The following types and methods were removed because they had a
single internal caller, no test coverage, or duplicated functionality
already exposed elsewhere. Most consumers won't notice.

If you were calling any of these directly, see the v1 source for the
replacement; the engine still does the work, it's just no longer
exposed.

## 3. Validation phase 1 + step kinds + StartAt (PR3)

`workflow.New` now performs structural validation up front and
returns a `*ValidationError` containing every problem it finds.
Previously, structural problems surfaced as runtime errors during
execution.

- Steps must have exactly one *kind* (Activity, Join, WaitSignal,
  Sleep, or Pause). Mixing kinds returns `ErrInvalidStepKind`.
- Modifier fields (Retry, Catch) are valid only on Activity-kind
  steps. Attaching them to a Sleep or Join now returns
  `ErrInvalidModifier`.
- `Options.StartAt` lets you pin the start step explicitly. Without
  it, the first step in `Options.Steps` is still the start.

```go
// Before — runtime failure if Sleep step had Retry attached
wf, _ := workflow.New(workflow.Options{Steps: steps})
exec.Run(ctx) // panics or fails mid-flight

// After — workflow.New returns a ValidationError describing all
// problems before any execution starts.
wf, err := workflow.New(workflow.Options{Steps: steps})
if err != nil {
    var ve *workflow.ValidationError
    if errors.As(err, &ve) {
        for _, p := range ve.Problems { /* report */ }
    }
}
```

## 4. ActivityRegistry, functional options, single Execute (PR4)

`NewExecution` now takes a workflow, an `ActivityRegistry`, and
functional options. The `ExecutionOptions` struct is gone. The
multiple Run/Execute method variants collapsed into a single
`Execute(ctx, ...ExecuteOption)`.

```go
// Before
exec, _ := workflow.NewExecution(workflow.ExecutionOptions{
    Workflow:   wf,
    Activities: []workflow.Activity{a, b, c},
    Logger:     logger,
})
exec.Run(ctx)

// After
reg := workflow.NewActivityRegistry()
reg.MustRegister(a, b, c)
exec, _ := workflow.NewExecution(wf, reg, workflow.WithLogger(logger))
result, _ := exec.Execute(ctx)
```

`Run` is gone — `Execute` returns `(*ExecutionResult, error)` and is
the single entry point. Resume is a functional option:
`exec.Execute(ctx, workflow.ResumeFrom(priorID))`.

`NewActivityFunction` and `NewTypedActivityFunction` are renamed to
`ActivityFunc` and `TypedActivityFunc`. The internal struct types
were unexported.

## 5. Binding validation in NewExecution (PR5)

`NewExecution` now binds the workflow against the registry and the
script compiler, surfacing missing activities, bad templates, and
bad expressions as `ValidationProblem`s on a `*ValidationError`
before any execution begins.

```go
// Before — missing activity surfaced at runtime
exec, _ := workflow.NewExecution(workflow.ExecutionOptions{Workflow: wf, Activities: nil})
exec.Run(ctx) // fails on first step that needed an activity

// After — caught at construction
_, err := workflow.NewExecution(wf, workflow.NewActivityRegistry())
// err is *ValidationError with ErrUnknownActivity problems for
// every step that referenced an unregistered activity
```

## 6. Context becomes idiomatic Go (PR6)

`workflow.Context` now embeds `context.Context`. Pass it directly to
any stdlib API that takes a context — no `.Std()` method, no manual
unwrapping. Custom implementations of `Context` should embed
`context.Context` too.

```go
// Before
func myActivity(ctx workflow.Context, p Params) (any, error) {
    req, _ := http.NewRequestWithContext(ctx.Std(), "GET", url, nil)
    return http.DefaultClient.Do(req)
}

// After
func myActivity(ctx workflow.Context, p Params) (any, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    return http.DefaultClient.Do(req)
}
```

## 7. Checkpoint stable wire format (PR7)

`Checkpoint` is now versioned. `CheckpointSchemaVersion` is `1` and
is set on every saved checkpoint. Readers must reject any checkpoint
with a higher schema version than they understand. The JSON tag on
every field is part of the stable format.

See `Checkpoint`'s godoc for the round-trip contract and the
load-bearing fields list.

## 8. Single template syntax (PR8)

`${...}` is the only template form. The `$(...)` "type-preserving"
syntax is gone. When a parameter value is a single `${...}` covering
the whole trimmed string, the result preserves its native Go type
automatically; otherwise the template is interpolated into a string.

```go
// Before
Parameters: map[string]any{
    "message": "Counter is ${state.counter}",       // string
    "value":   "$(state.counter)",                  // typed
}

// After — the difference is automatic from context
Parameters: map[string]any{
    "message": "Counter is ${state.counter}",       // string (interpolated)
    "value":   "${state.counter}",                  // typed (whole-value)
}
```

Edge conditions and `each.Items` are raw expressions — no `${...}`
or `$(...)` wrapper:

```go
Condition: "state.counter <= inputs.max_count"
Each: &workflow.Each{Items: "state.users"}
```

## 9. Activities tier split + naming (PR9)

The `activities/` tree is now split by guarantee level:

| Activity              | Was                  | Now                       |
| --------------------- | -------------------- | ------------------------- |
| `print` / `time` / `json` / `random` / `fail` | `activities/` | `activities/` (unchanged) |
| `http`                | `activities/`        | `activities/httpx/`       |
| `shell` / `file`      | `activities/`        | `activities/contrib/`     |
| `wait`                | `activities/`        | **deleted**               |

```go
// Before
import "github.com/deepnoodle-ai/workflow/activities"

reg.MustRegister(activities.NewHTTPActivity(), activities.NewShellActivity())

// After
import (
    "github.com/deepnoodle-ai/workflow/activities/contrib"
    "github.com/deepnoodle-ai/workflow/activities/httpx"
)

reg.MustRegister(httpx.NewHTTPActivity(), contrib.NewShellActivity())
```

`activities.NewWaitActivity` is gone. Use a `Sleep` step for durable
waits (survives restarts), or write a one-line activity that calls
`time.Sleep` for in-process delays.

`PrintActivity` now wraps an `io.Writer`. `NewPrintActivity()`
defaults to `os.Stdout`; `NewPrintActivityTo(w)` injects a custom
writer.

All `float64`-second timeouts are now `time.Duration`. Affects
`httpx.HTTPInput.Timeout`, `contrib.ShellInput.Timeout`, and
`activities.ChildWorkflowInput.Timeout`.

## 10. Child workflow fixes (PR10)

- `ChildWorkflowSpec.Sync` deleted. Choose sync vs async at the
  call site by invoking `ExecuteSync` or `ExecuteAsync`.
- `ChildWorkflowResult.Error` dropped. The execution error is the
  second return value; consult that instead.
- `ChildWorkflowExecutorOptions.CleanupTimeout` added. Default `1h`
  (was a hardcoded 5 minutes). Negative disables eviction entirely.
- The bundled `workflow.child` activity is sync-only. Drop the
  `"sync": true` parameter from existing workflows. Consumers
  needing fire-and-forget should write a custom activity that calls
  `executor.ExecuteAsync` directly.

`ExecuteAsync`'s godoc now spells out the in-process / non-durable
contract: async children die with the parent process.

## 11. Error model + completion hook (PR11)

- `ClassifyError` no longer substring-matches `"timeout"`. Real
  timeouts must wrap `context.DeadlineExceeded` or
  `workflow.ErrWaitTimeout`.
- `context.Canceled` is no longer classified as a timeout — it
  represents caller-initiated cancellation, not deadline expiry.
- `WorkflowError.Error()` now prefixes `workflow:`.
- All root-package error sentinels are prefixed with `workflow:`.
- `WorkflowError.Details` is documented as non-roundtrip across
  Checkpoint persistence; wrap your own error type if you need
  structured details to survive resume.

If you were comparing error strings, they now contain the
`workflow:` prefix. Switch to `errors.Is` / `errors.As` for sentinel
checks.

## 12. ExecutionResult helpers (PR12)

Additive only — no breaking change. New convenience accessors on
`*ExecutionResult`:

- `Output(key)`, `OutputString(key)`, `OutputInt(key)`,
  `OutputBool(key)`
- `WaitReason()`, `Topics()`, `NextWakeAt()`
- Generic helper `workflow.OutputAs[T](r, key)`

All accessors are nil-safe on the receiver.
