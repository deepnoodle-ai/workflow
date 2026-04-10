# Workflow Library

Go library for defining and executing multi-step processes as directed graphs.
Module: `github.com/deepnoodle-ai/workflow`

Read `llms.txt` for the full API reference.

## Scope

This is a **pure execution engine**. It runs workflows in-process and provides
interfaces for the things it doesn't own. What it does and doesn't do:

**Does**: Define workflows as step graphs. Execute steps (activities). Branch
and join execution paths. Retry with backoff. Catch and route errors. Checkpoint
execution state. Resume from checkpoints. Track step progress. Template
parameters with a pluggable scripting engine (Risor or expr-lang).

**Does not**: Store workflows, checkpoints, or progress. Queue or schedule work.
Manage distributed workers or leases. Provide a database, API, or UI.

Storage is the consumer's responsibility. The library defines interfaces
(`Checkpointer`, `StepProgressStore`, `ActivityLogger`) and the consumer
provides implementations backed by their own infrastructure (Postgres, Redis,
S3, etc.). The built-in `FileCheckpointer` and `MemoryCheckpointer` exist for
development and testing only.

## How Path Execution Works

A workflow starts with a single "main" path executing from the start step.
Each path runs in its own goroutine and progresses step-by-step through the
graph. When a step has multiple matching edges, the path **branches**: the
engine creates new child paths, each running in its own goroutine. When paths
branch, each child receives a **deep copy** of the parent's state — after that
point, paths are fully independent with no shared mutable state.

The orchestrator (`execution.go`) coordinates paths through a channel-based
snapshot loop:
1. Paths send `PathSnapshot` messages to a shared channel as they complete steps
2. The orchestrator processes snapshots sequentially on the main goroutine
3. This handles branching (spawn new paths), joining (wait for paths to converge),
   checkpointing, and failure propagation
4. The loop exits when no active paths remain

Join steps (`JoinConfig`) block a path until specified paths complete, then
merge state from the completed paths into the waiting path via `PathMappings`.

## Concurrency Model

- **No shared mutable state between paths.** Copy-on-branch eliminates races.
- **Single orchestrator goroutine** processes all path snapshots sequentially.
  This avoids concurrent mutation of `ExecutionState` from the orchestration side.
- **`ExecutionState` is mutex-protected** (`sync.RWMutex`) because both the
  orchestrator and activity execution goroutines access it (e.g., checkpointing
  after activity completion happens under lock in `executeActivity`).
- **Step progress dispatch is fire-and-forget.** Store calls run in detached
  goroutines. Errors are logged, never block the workflow.
- **Heartbeat runs in a separate goroutine** and cancels the execution context
  on failure — cooperative shutdown via standard `context.Context` cancellation.

## Commands

`make test` runs tests for the main module plus the risor and expr engine
modules. `make test-all` also runs `go vet` on the `cmd` and `examples`
nested modules. `make cover` produces a coverage report for the main module.
`go vet ./...` from any module directory for per-module static analysis.

## Packages and modules

This repo is a Go workspace with **four modules**, so the core stays light
and engine deps only land for consumers that opt into them:

- Root (`workflow`) — the engine: definition, execution, checkpointing, errors.
  No scripting dependencies. If a workflow hits a condition, template, or
  `$(…)` expression without a compiler configured, the default
  `script.NoopCompiler` returns `ErrNoScriptCompiler` pointing to the
  engine sub-modules.
- `activities/` — built-in activities (print, http, shell, etc.). The
  `script` activity lives in the Risor sub-module, not here.
- `script/` — engine-neutral interfaces (`Compiler`, `Script`, `Value`),
  the `${…}` template parser, `NoopCompiler`, and shared helpers
  (`IsTruthyValue`, `EachValue`) that both engines use for their
  Value implementations.
- `scripts/risor/` — nested module, Risor v2 implementation.
  `NewEngine(DefaultGlobals(...))`, `NewScriptActivity()`, and
  `ExecuteScript()` for state-mutating scripts.
- `scripts/expr/` — nested module, expr-lang implementation.
  `NewEngine(opts...)` with `WithFunctions(...)` and `WithExprOptions(...)`.
  No `script` activity — expr is expression-only.
- `examples/` — nested module, so examples can import any scripting engine
  without polluting the main module's dep tree.
- `cmd/workflow/` — nested module, the CLI. Uses Risor by default.
- `workflowtest/` — test helpers (Run, MockActivity, MemoryCheckpointer).

## Conventions

- **Tests**: `testify/require`. Internal tests (`package workflow`), except
  `workflowtest/` which uses `package workflowtest_test`.
- **Interfaces**: Small (one method when possible). Never modify exported
  interfaces — use optional side interfaces (see `ProgressReporter` pattern).
- **Errors**: Sentinels with `errors.Is`. Structured errors via `WorkflowError`.
- **New features**: Additive only. Existing `Run()`/`Resume()` signatures are frozen.
  New functionality goes through `Execute()`/`ExecuteOrResume()` or the `Runner`.
- **Compose, don't inherit.** Each piece works standalone. Runner isn't required.

## Things to Know

- The first step in the Steps slice is the start step.
- `ErrFenceViolation` bypasses retry and catch — non-retryable by design.
- `buildResult` classifies interrupted executions (context canceled mid-run)
  as failed, even if `SetFinished()` was never called.
