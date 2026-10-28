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
retry, catch, timeouts, sleep, set, on_failure, bounded loops). Evaluate
`when`, `each` and `${...}` templates with `github.com/deepnoodle-ai/expr`.
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
- A join waits until no live path can reach it over forward edges (loop back
  edges removed; precomputed at validation). It continues on the arrivals'
  common ancestor only if they cover every child of that ancestor's last
  fork; otherwise on a fresh path `<ancestor>.<join>`. Scopes merge by visit
  number. Both rules depend only on the arrival set, never on event order.
- Within one `Apply`, transitions run from a FIFO queue, then joins fire,
  then completion is checked, until quiescent. Every map iteration that
  affects output is sorted. This is what makes completion order irrelevant.
- Outputs are JSON-normalized on the way in, so a reloaded state behaves
  exactly like the in-memory one.

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
  fencing by claim count, `ErrDeferred`).

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
- `expr` cannot select Go-keyword fields with a dot: `error["type"]`.
- A cycle must contain a step with `max_visits` or a `sleep`.
