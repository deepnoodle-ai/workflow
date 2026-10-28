# Workflow Library

Deterministic progression engine for step graphs, for event-driven hosts.
Module: `github.com/deepnoodle-ai/workflow`

Read `llms.txt` for the API reference, `README.md` for the tour,
`documentation/definitions.md` for the definition language and
`documentation/hosts.md` for the host contract.

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

- A run starts on path `main` at the first step. Each step visit is a `Node`;
  each dispatchable piece of work is a `Unit` (one per `each` item).
- When a node settles, its edges are evaluated. One match continues the path,
  several fork it into child paths (`main.<target>`), none ends it.
- Loops are found from the graph alone (validate.go `computeLoops`): each
  loop's heads are its entry steps, its back edges go from inside it to a
  head. A join fires once per pass of the loops it is in, in cohorts keyed
  by each waiting path's pass (head visits). A cohort fires when no live
  path can still arrive in that pass (`canStillArrive`, using the
  precomputed `arrive` sets); that predicate only goes from true to false,
  which is what makes arrivals independent of event order. It continues on
  the arrivals' common ancestor only if they account for that ancestor's
  whole last fork (`covers`, no lineage shortcuts); otherwise on a fresh
  path `<ancestor>.<join>`. Scopes merge by visit number.
- Within one `Apply`, transitions run from a FIFO queue, then joins fire,
  then completion is checked, until quiescent. Every map iteration that
  affects output is sorted. This is what makes completion order irrelevant.
- Outputs are JSON-normalized on the way in, so a reloaded state behaves
  exactly like the in-memory one.
- Lifecycle: `running` until the outcome is decided (`checkDone`,
  `failRun`, a cancel), then `conclude`: the `failure` path runs
  `on_failure` (status `failing`), then the `finally` path runs `finally`
  over the union of every path's scope (status `completing`, `failing` or
  `cancelling`, `RunStatus.Outcome()`). The terminal command is emitted
  when the last finally step settles (`emitFinished`). Finally failures go
  to `State.FinallyErrors`, never the outcome; a cancel during finally
  stops it (`stopFinally`).

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
  events, tasks outbox) and Worker (SKIP LOCKED claims, leases, heartbeats,
  fencing by a token that only grows, `ErrDeferred`, versioned `Migrate`).

## Conventions

- **Tests**: `internal/require`. Internal tests in the root package. Core
  behavior is tested through `fakeHost` (host_test.go), which round-trips the
  state through JSON on every event and checks Apply never mutates its input.
  Order independence is a property test over random graphs
  (property_test.go); keep it passing.
- **Determinism**: never read the clock, randomness, or unsorted map order in
  the root package. `Event.At` is the only time source.
- **Errors**: `Apply` returns errors only for host mistakes (sentinels with
  `errors.Is`). Run failures are `RunFailed` commands with a `RunError`.
- **Interfaces**: small. `Kinds` is the only host-supplied interface in the
  root.

## Things to Know

- The first step is the start step.
- Step names are expression globals, so they must be identifiers and cannot
  shadow globals or function names (`split`, `first`, `count`, ...).
- Definitions are statically typed (`typecheck.go`, using expr's
  `Program.Check`). The "settled" dataflow (`computeFacts`) decides which
  step reads are maybe-nil; runtime must agree with it (every step name is in
  the env as nil until settled, absent declared properties are filled with
  nil by `Schema.complete`). Change both sides together.
- Runtime validation (`Schema.validate`) backs the static types: inputs and
  trigger at Start, params at dispatch, step outputs on completion, run
  outputs (long form with a schema) when the run completes.
- A cycle must contain a step with `max_visits` or a `sleep`.
