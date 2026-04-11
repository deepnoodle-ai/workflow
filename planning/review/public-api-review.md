# Workflow Public API Review

Date: 2026-04-11

Baseline: `go test ./...` passes on the current tree. The implementation is in decent shape, but the public API is not ready to freeze yet. The biggest issue is not correctness; it is that the exported surface is larger, less opinionated, and less internally consistent than it should be for a production-facing Go library.

## Executive Summary

The core idea is good:

- A workflow is a graph of `Step`s.
- Work happens in `Activity`s.
- An `Execution` runs the graph and can suspend, resume, retry, and checkpoint.
- `workflowtest` is the right direction for making workflows easy to test.

What is not ready yet:

- The root package exports too many engine internals.
- Several naming collisions are silently accepted instead of rejected.
- The step DSL has implicit precedence rules instead of explicit step kinds.
- There are too many lifecycle entry points for consumers to learn.
- The built-in `activities` package mixes production primitives, demo helpers, and risky side-effectful tools.

If I were moving this toward a stable v1, I would keep the concepts above and aggressively narrow and sharpen everything around them.

## What I Would Keep

- `Workflow`, `Step`, `Edge`, `RetryConfig`, `CatchConfig`, `Execution`, and `ExecutionResult` are the right center of gravity.
- The split between untyped activities (`Activity`) and generic helpers (`NewTypedActivity`, `NewTypedActivityFunction`) is useful.
- Durable suspension concepts are worth keeping: `Wait`, `WaitSignalConfig`, `SleepConfig`, `PauseConfig`, `SignalStore`.
- `workflowtest` is exactly the kind of separate package this library should have.
- Structured execution results are a better consumer API than only returning an error.

## Highest-Priority Changes Before Freezing the API

### 1. Shrink the exported surface dramatically

The root package currently exports many types that read like implementation detail, not stable product surface:

- `Path`, `PathOptions`, `PathSpec`, `PathSnapshot`, `WaitRequest`, `JoinRequest` in `path.go:27-112`
- `PathState`, `JoinState`, `ExecutionState` in `execution_state.go:11-98`
- `WaitState`, `WaitKind`, `NewSignalWait`, `NewSleepWait` in `wait_state.go:9-107`
- `ExecutionAdapter` in `execution_adapter.go:5-10`
- `NewContext` and `ExecutionContextOptions` in `context.go:61-94`

That creates a large compatibility burden and makes package discovery noisy. A user reading `go doc` should mostly see the DSL, runtime entry points, interfaces, and results. Today they also see orchestration plumbing.

Recommendation:

- In the next major version, unexport these engine-internal types or move them under `internal`.
- If some must remain for advanced integrations, move them behind an explicit `unstable` or `experimental` package rather than leaving them in the root.
- For the current version, document clearly which exported types are not part of the intended stable API.

### 2. Fail fast on duplicate names and binding errors

There are several places where invalid user configuration is silently accepted:

- Duplicate step names overwrite earlier entries in `workflow.New` because `stepsByName[step.Name] = step` has no duplicate check in `workflow.go:61-68`.
- Duplicate activity names overwrite earlier entries in `NewExecution` because `activities[activity.Name()] = activity` has no duplicate check in `execution.go:180-183`.
- Duplicate workflow names overwrite earlier registrations in `MemoryWorkflowRegistry.Register` at `child_workflow.go:73-83`.

This is exactly the kind of bug that becomes painful in production because it fails by behavior drift, not by a clear startup error.

Recommendation:

- Reject duplicate step names in `workflow.New`.
- Reject duplicate activity names in `NewExecution`.
- Reject duplicate workflow names in `WorkflowRegistry.Register`, or at least make overwrite opt-in.
- Add binding validation in `NewExecution` so missing activity references fail at construction time, not only when the step is reached.

### 3. Make step kinds explicit instead of relying on precedence

`Step` currently allows multiple mutually exclusive fields to be set at once:

- `Activity`
- `Join`
- `WaitSignal`
- `Sleep`
- `Pause`
- `Each`

The engine resolves this by precedence in `path.go:383-405`:

- `Join` wins first
- then `WaitSignal`
- then `Sleep`
- then `Pause`
- then `Activity`

`Workflow.Validate` does not enforce “exactly one step kind” or even “compatible combinations only”; it only checks a subset of structural rules in `validate.go:39-149`.

That means a user can accidentally write a step with both `Activity` and `WaitSignal`, get no validation error, and silently have the activity ignored.

Recommendation:

- Define a clear invariant: each step is exactly one of `activity`, `join`, `wait_signal`, `sleep`, `pause`.
- Enforce that invariant in validation.
- Keep `Each`, `Retry`, `Catch`, `Store`, and `Next` as modifiers, but validate which modifiers are legal for each step kind.
- If you want to keep the current struct layout, at least add strong validation and document the allowed combinations in one authoritative place.

### 4. Pick one primary execution API

Right now consumers have to sort through:

- `Run`
- `Resume`
- `RunOrResume`
- `Execute`
- `ExecuteOrResume`
- `Runner.Run`

See `execution.go:397-448` and `runner.go:78-149`.

This is too much surface for a library that wants to feel simple. The distinction between infrastructure failures and workflow failures is good, but it should be expressed through one blessed path, not a family of nearly-overlapping entry points.

Recommendation:

- Make `Execute` / `ExecuteOrResume` the primary execution API because `ExecutionResult` is the most useful consumer-facing shape.
- Keep `Run` / `Resume` as lower-level compatibility helpers, but de-emphasize them in docs.
- Decide whether `Runner` is truly primary. If it is, let it own more of execution creation. If it is not, keep it clearly optional.

My preference:

- `workflow.New(...)`
- `workflow.NewExecution(...)`
- `exec.Execute(ctx)`
- optional `runner.Run(...)` only when heartbeats / completion hooks / resume policy matter

### 5. Normalize state, variable, and path references

The API currently mixes several ways of referring to state:

- `Store: "state.counter"` in examples
- `Store: "counter"` also works because `"state."` is stripped in `path.go:446-459`
- `WaitSignal.Store` also strips `"state."` in `path.go:533-537`
- `CatchConfig.Store` strips `"state."` in `path.go:1167-1173`
- `Output.Variable` expects a variable path, while `Output.Path` selects a workflow path in `execution.go:723-749`

This is powerful, but the mental model is muddy. Users should not need to remember where `"state."` is optional, where dot paths mean nested fields, and where dot paths mean something else.

Recommendation:

- Pick one canonical variable reference style for public APIs.
- I would make `Store` and `Output.Variable` refer to workflow variables directly, without accepting `"state."` prefixes.
- Continue exposing `state` and `inputs` inside expressions/templates, but keep the configuration fields themselves simpler.
- If nested paths are important, define them explicitly as “field paths” and document them once.

## Detailed Review

### Construction and Validation

`workflow.New` does only partial validation and then leaves deeper validation to a separate `Validate` call:

- `workflow.New` does name existence and edge destination checks in `workflow.go:52-85`
- richer validation lives separately in `validate.go:39-149`

That split is easy to miss, especially because the README and examples do not establish a strong “always call `Validate()`” habit.

Recommendation:

- Either make `workflow.New` perform the full stable validation set, or
- add an explicit `NewValidated` / `MustValidate` path and make that the documented default.

Also, the start step is implicitly `opts.Steps[0]` in `workflow.go:83`. That is simple, but brittle for large workflows or generated specs.

Recommendation:

- Consider an optional `StartAt string` on `Options`, defaulting to the first step when omitted.

### Inputs and Outputs

`Input.Type` looks like schema, but it is only documentation today. `NewExecution` checks presence and unknown names, not types, in `execution.go:162-177`.

Recommendation:

- Either make `Input.Type` enforceable, or
- rename/reframe it as documentation metadata so users do not infer runtime guarantees that do not exist.

For production, I would lean toward one of these:

- Keep `Type string`, but add a pluggable input validator interface.
- Remove the implied schema language from core and let consumers validate inputs before `NewExecution`.

### Activities

The high-level activity model is good, but the typed adapter is only “typed” at the API boundary. Under the hood it marshals and unmarshals JSON in `activity.go:45-56`.

That has consequences:

- parameter decoding follows JSON semantics, not plain Go assignment semantics
- field tags matter
- conversion edge cases can surprise users
- the extra serialization step adds overhead

Recommendation:

- Document this very explicitly, or
- replace it with a non-JSON decoder path in a future version

I would also add an explicit registry helper instead of accepting a raw `[]Activity` everywhere. Something like an `ActivitySet` or `Registry` would make duplicate detection and discoverability much cleaner.

### Context API

`workflow.Context` itself is fine. The problem is that the construction surface leaks internals:

- `ExecutionContextOptions` exposes `PendingWait`, `ActivityHistory`, `SignalStore`, and execution IDs in `context.go:61-79`
- `NewContext` returns the internal execution context in `context.go:81-94`

This feels like test/support plumbing that escaped into the main package.

Recommendation:

- Keep `Context` as the consumer interface.
- Move `NewContext` and `ExecutionContextOptions` into `workflowtest` or another clearly non-runtime package.
- If external wrappers need to preserve features like waits or history, keep the tiny side interfaces (`SignalAware`, `ActivityHistoryAware`) and avoid exposing the concrete context constructor.

### Execution Results and Error Model

`ExecutionResult` is the right abstraction. Keep building around it.

What feels rough:

- `ExecutionStatus` mixes terminal results with internal runtime states.
- `ExecutionStatusWaiting` is mostly orchestration detail, not something most consumers should branch on.
- `WorkflowError` uses string types, which is flexible, but the timeout classification also falls back to string matching in `errors.go:91-104`.

Recommendation:

- Keep `ExecutionResult` as the consumer contract.
- Separate internal runtime statuses from public terminal outcomes if possible.
- Keep `WorkflowError.Type` as a string if you want Step Functions-like matching, but reduce “guessing” in `ClassifyError`.

### Completion Hooks

There is a contract inconsistency today:

- `CompletionHook` docs say partial follow-ups should still be inspectable on `result.FollowUps` even when the hook returns an error in `completion_hook.go:13-15`
- `Runner.Run` only assigns `result.FollowUps = followUps` when `hookErr == nil` in `runner.go:138-148`

Recommendation:

- Fix the behavior or fix the docs, but do not leave this ambiguous.
- My preference is to always attach any returned follow-ups, then log the hook error separately.

### Child Workflows

The child workflow concept is good, but the API shape needs tightening.

Current rough spots:

- `ChildWorkflowSpec` includes `Sync bool` even though the executor already has separate `ExecuteSync` and `ExecuteAsync` methods in `child_workflow.go:13-20` and `37-47`
- `ChildWorkflowResult` contains `Error error` and `ExecuteSync` also returns `error`, duplicating the failure channel in `child_workflow.go:22-29` and `145-199`
- the built-in child workflow activity uses `Timeout float64` seconds in `activities/child_workflow_activity.go:12-18`, while the core spec uses `time.Duration` in `child_workflow.go:15-19`

Recommendation:

- Remove `Sync` from `ChildWorkflowSpec`; the method call already defines sync vs async.
- Make `ChildWorkflowResult.Error` a structured field or drop it and use the returned error only.
- Align the activity wrapper with the core API types.

## Built-In Activities: Keep, Split, or Demote

The `activities` package is useful, but it is not cohesive enough to present as a single “standard library” yet.

The biggest conceptual problem is naming overlap:

- `activities.NewWaitActivity()` is an in-process sleep using `time.After` in `activities/wait_activity.go:25-35`
- `workflow.Wait(...)` is a durable signal wait in `wait.go:75-139`
- `SleepConfig` is a durable wall-clock sleep

That is too much “wait” for one library.

Other rough spots:

- `PrintActivity` writes directly to stdout in `activities/print_activity.go:26-29`
- `HTTPActivity` and `ShellActivity` use `float64` seconds for timeouts in `activities/http_activity.go:16-24` and `activities/shell_activity.go:15-22`
- `ShellActivity` and `FileActivity` are broad side-effectful primitives and should not look as blessed as core orchestration concepts

Recommendation:

- Split `activities` into support tiers:
- Keep only small, obviously-safe primitives in the main package or a clear stdlib subpackage.
- Move risky or environment-specific activities (`shell`, `file`) into `contrib` or mark them experimental.
- Remove or rename the non-durable `wait` activity. `sleep` would at least be less confusing, but I would rather steer users to `SleepConfig`.

## Concepts to Add

- An activity registry type with duplicate detection and optional upfront binding validation.
- Stronger workflow validation that checks legal step-kind combinations.
- An optional explicit start-step field.
- A “blessed path” in the docs for production usage.
- More package examples that show the intended default way to build workflows in 2026, not several equally-valid-looking styles.

## Concepts to Remove or De-Emphasize

- Engine internals from the root export surface.
- Silent overwrite behavior for named objects.
- The non-durable `wait` activity as a first-class primitive.
- `Sync` inside `ChildWorkflowSpec`.
- The idea that `"state."` prefixes are part of the stable configuration API.

## Suggested Stable Surface

If I were defining the production-ready public surface, I would aim for this:

- Root package:
  `Workflow`, `Step`, `Edge`, `Input`, `Output`, `Activity`, `NewActivityFunction`, `NewTypedActivity`, `Execution`, `ExecutionOptions`, `ExecutionResult`, `Runner`, `WorkflowError`, `Checkpointer`, `SignalStore`, `Wait`, `RetryConfig`, `CatchConfig`, `WaitSignalConfig`, `SleepConfig`, `PauseConfig`
- Testing package:
  `workflowtest`
- Everything else:
  internal, experimental, or contrib

More specifically, I would not want consumers depending on these as “stable API”:

- `Path`
- `PathOptions`
- `PathSnapshot`
- `PathState`
- `JoinState`
- `ExecutionState`
- `ExecutionAdapter`
- `WaitState`
- `NewContext`

## Recommended Order of Work

1. Tighten validation and duplicate detection.
2. Decide and document the small stable surface.
3. Move or de-emphasize leaked internals.
4. Simplify lifecycle docs around one primary execution path.
5. Split or relabel the built-in activities package.
6. Rework child workflow API consistency.

## Bottom Line

This library already has a solid core. The work to make it production-ready is mostly API editorial work:

- reduce what is public
- reject ambiguity earlier
- make the DSL rules explicit
- stop exposing implementation detail as product surface

If you do that, the library can stay simple while still supporting the advanced features that make it interesting.
