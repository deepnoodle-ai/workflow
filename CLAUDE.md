# Workflow Library

Deterministic progression engine for step graphs, for event-driven hosts.
Module: `github.com/deepnoodle-ai/workflow`

Read `llms.txt` for the API reference, `README.md` for the tour,
`documentation/definitions.md` for the definition language and
`documentation/hosts.md` for the host contract (the one place it is
written in full; keep README, doc.go and llms.txt consistent with it).

## Scope

The engine is one pure function:
`Apply(def, state, event) -> (state, commands, error)`. It decides what
happens next; hosts do it.

**Does**: Parse and validate definitions (edges, conditions, joins, `each`,
retry, catch, timeouts, sleep, set, on_failure, finally, bounded loops). Evaluate
`when`, `each` and `${{ ... }}` templates with `github.com/deepnoodle-ai/expr`.
Advance a run by one event, deterministically, with idempotent handling of
duplicate and late events.

**Does not** (in the root package): I/O, goroutines, clocks, storage, queues,
leases. The `local` package runs commands in-process; the `postgres` module is
a durable host. Hosts with their own durable runtime use only the root.

## How progression works

- A run starts on path `main` at the first step. Each step visit is a
  `nodeState` (unexported, like paths and joins); each dispatchable piece of
  work is a `Unit` (one per `each` item). A `StepID` names one attempt at a
  unit; `StepID.Unit()` drops the attempt.
- When a node settles, its edges are evaluated. One match continues the path,
  several fork it into child paths (`main.<target>`), none ends it.
- Loops are found from the graph alone (validate.go `computeLoops`): each
  loop's heads are its entry steps, its back edges go from inside it to a
  head. A join fires once per pass of the loops it is in, in cohorts keyed
  by each waiting path's pass (head visits). A cohort fires when no live
  path can still arrive in that pass (`canStillArrive`, using the
  precomputed `arrive` sets); that predicate only goes from true to false,
  which is what makes arrivals independent of the order of successes. It
  continues on the arrivals' common ancestor only if they account for that
  ancestor's whole last fork (`covers`, no lineage shortcuts); otherwise on
  a fresh path `<ancestor>.<join>`. Scopes merge by visit number. A join's
  names are an assertion, not what it waits for: Load rejects a name a path
  can skip (`checkJoinsSettle` in joincheck.go; tests turn it off with
  `lenientJoins` to reach the run-time `join` failure behind it).
- Within one `Apply`, transitions run from a FIFO queue, then joins fire,
  then completion is checked, until quiescent. Every map iteration that
  affects output is sorted. So the same ordered events give the same
  result, and successful completions of steps in flight together may arrive
  in any order with the same final state and dispatches. Failures and
  timeouts are not order-independent: a failure can fail the run before a
  sibling's success dispatches anything.
- Outputs are JSON-normalized on the way in, so a reloaded state behaves
  exactly like the in-memory one.
- Lifecycle: `running` until the outcome is decided (`checkDone`,
  `failRun`, a cancel), then `conclude`: the `failure` path runs
  `on_failure` (status `failing`), then the `finally` path runs `finally`
  over the union of every path's scope (status `completing`, `failing` or
  `cancelling`, `RunStatus.Outcome()`). The terminal command is emitted
  when the last finally step settles (`emitFinished`). Finally failures go
  to `State.FinallyErrors`, never the outcome. A cancel decides the outcome
  once: a second cancel, or one during finally, is a no-op; only
  `Event.Force` stops or skips finally steps (`stopFinally`,
  `skipFinally`). `Abort` (abort.go) fails a run without the definition,
  so it runs neither on_failure nor finally.

## Commands

`make test` runs the root module tests. `make test-pg` runs the postgres
module (needs `WORKFLOW_PG_DSN`). `make test-all` runs both plus `go vet`.

## Packages and modules

- Root (`workflow`): definition, validation, expressions, `Apply`,
  `Outstanding`, state types. Stdlib + expr only.
- `local/`: in-process driver over `Apply`.
- `cmd/workflow/`: CLI (`validate`, `run`) with print, echo and shell kinds.
- `examples/`: runnable programs; must not add dependencies.
- `internal/require/`: stdlib-only testify shim.
- `postgres/` (separate `go.mod`, pgx): Store (definitions, runs, steps,
  events, jobs outbox) and Worker (SKIP LOCKED claims, leases, heartbeats,
  fencing by a token that only grows, versioned `Migrate`).
- `host.go`: the handler contract both hosts share (`Job`, `Handler`,
  `ErrDeferred`, `StepErrorOf`).

## Conventions

- **Tests**: `internal/require`. Internal tests in the root package. Core
  behavior is tested through `fakeHost` (host_test.go), which round-trips the
  state through JSON on every event and checks Apply never mutates its input.
  Order independence of successful completions is a property test over
  random graphs (property_test.go); keep it passing.
- **Determinism**: never read the clock, randomness, or unsorted map order in
  the root package. `Event.At` is the only time source.
- **Errors**: `Apply` returns errors only for host mistakes (sentinels with
  `errors.Is`). Run failures are `RunFailed` commands with a `RunError`.
- **Interfaces**: small. Hosts supply `Kinds`, and optionally
  `KindSchemas` on the same value; hosts call a `Handler` func.

## Things to Know

- The first step is the start step.
- Step names are expression globals, so they must be identifiers and cannot
  shadow globals or function names (`split`, `first`, `count`, ...).
- Definitions are statically typed (`typecheck.go`, using expr's
  `Program.Check`). The "settled" dataflow (`computeFacts`) decides which
  step reads are maybe-nil; runtime must agree with it (every step name is in
  the env as nil until settled, absent declared properties are filled with
  nil by `Schema.complete`). Change both sides together.
- Runtime validation (`Schema.validate`) backs the static types: the input
  at Start, params at dispatch, step outputs on completion, run outputs
  (long form with a schema) when the run completes. `Schema.complete`
  fills defaults and absent properties; a property with a default is typed
  non-nil, so the two must agree.
- A cycle must contain a step with `max_visits` or a `sleep`.
