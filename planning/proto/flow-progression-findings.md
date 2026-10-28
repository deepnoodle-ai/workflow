# Spike findings: a progression API for event-driven hosts

_2026-10-28. Branch `proto/flow-progression`. Brief:
`mc-proto-workflows/docs/research/technical/2026-10-28-workflow-library-progression-api.md`._

## What the spike did

The brief asked for a new `flow/` package beside the existing runtime. The
spike went further, because backwards compatibility was out of scope: the
pure progression core **is** the library now.

- Root package `workflow`: `Parse`, `ParseMap`, `Validate`, `Apply`,
  `Outstanding`, and the `State`, `Event`, `Command` types. Stdlib + expr
  v1.2.0. About 2,700 lines with doc comments, 87% statement coverage.
- `local`: the old `Execution`/`Runner`, rebuilt as a ~200-line driver over
  `Apply`. Its checkpoint is the `State`, so resume never repeats a settled
  step.
- `postgres` (separate module): a complete durable host. Definitions by hash,
  runs with state in jsonb, a step record per StepID, an event log, and an
  outbox of tasks. A `Worker` claims tasks with `SKIP LOCKED`, heartbeats its
  lease, fences late results by claim count, and supports steps completed
  later by another system (`ErrDeferred`).
- Deleted: the goroutine-per-branch engine, checkpointers, activity
  registry, script-compiler plug-in, `workflowtest`, the old examples and
  docs, and `experimental/worker` and `experimental/store/*`.

The three engine-gate bugs disappear rather than get fixed: there is no
branch replay (state records settled units), the Postgres host fences by
claim count instead of mapping errors, and step records are keyed by StepID.

## Evidence

- PR review from the proposal, end to end, with a fake host that
  round-trips the state through JSON on every event and checks Apply never
  mutates its input. Also the approve path and the failure path.
- Order independence: 200 shuffled completion orders over PR review, and 40
  orders over each of 60 random graphs (nested forks, conditional edges,
  named and unnamed joins, `each` with concurrency caps). Same final state
  and same dispatch set every time.
- Duplicate events, cancel mid-fan-out, retry with backoff (and the three
  ways retry must not happen), `each` with a failing item (with and without
  `continue_on_error`), `on_failure` in order and failing, timeouts,
  sleep, catch, loops with `max_visits`, output limits.
- Postgres (real Postgres 18, `-race`): 12 runs × 8 sibling completions
  released at once from separate transactions, 15 times over: every join
  fired exactly once and saw all eight. Concurrent duplicate deliveries.
  A worker that claims a step and dies: the lease expires, another worker
  runs the step once, the dead worker's late result is fenced out. Cancel
  stops a running handler through the heartbeat. Sleep and timeout timers.
  A deferred step completed by `Deliver` days later. A second process with
  a cold cache advancing a run.

An independent review then found four join bugs, each with a reproduction:
a join could revive a path that had already finished and dispatch a StepID
twice; merged scopes compared node keys as strings, so an older visit could
win; two joins inside a loop blocked each other through the back edge; and a
join naming a step merged by an earlier join always failed. All four are
fixed with regression tests (`join_test.go`), and the random-graph property
test now generates partial joins, which reproduces the first bug's
order-dependence on the old code. It also found that a graceful worker
shutdown counted toward `MaxClaims`; fixed.

## What in the brief turned out wrong or awkward

1. **`Parse` cannot read YAML.** The module is stdlib + expr, and the stdlib
   has no YAML. `Parse` reads JSON; `ParseMap` takes a document the host
   decoded with its own YAML library. Mobius already has one.
2. **Parse cannot resolve kinds.** A step's kind is "the one key among the
   host's known kinds", so it is unknowable without `Kinds`. `Validate` binds
   kinds and compiles expressions, and `Apply` refuses an unvalidated
   definition. A single `Load(data, kinds)` might be the better API.
3. **The brief's join rule fires early.** "Waits until those steps have
   settled" lets a join fire while another path can still arrive, and fire
   again when it does. The spike's rule: a join fires when no live path can
   still reach it (reachability is computed at validation), merges the
   arrivals into their common ancestor path, and fails the run if a named
   step did not settle on any arrival. `join: []` waits for every path that
   can reach it. This is what makes completion order irrelevant.
4. **The example's edges need `match: first`.** `next: [{step: report, when:
   ...}, {step: skip}]` fires both edges when the condition holds, because
   all matching edges fire. (And `skip` is not a step.)
5. **`concat` is not an expr builtin.** The library adds it. expr's string,
   math and collection groups (`split`, `first`, `sum`, ...) are not in
   `WithBuiltins` either; the library enables them all, since all are pure.
6. **`error.type` did not parse.** expr could not select a field whose name
   is a Go keyword (`type`, `func`, `range`, `default`, `select`, ...).
   GitHub payloads are full of `type` fields (`event.sender.type`). Fixed in
   expr (branch `typecheck`); see `typed-definitions.md`.
7. **Step names collide with functions.** Steps are bare globals, so a step
   cannot be named `first`, `count`, `sort`, `split`, `join`, `last`, `sum`,
   `min`, `max`. Validation rejects them. A `steps.` namespace would avoid
   that at the cost of longer expressions.
8. **`RunFailed` needs its own error type.** "Both errors" does not fit a
   `*StepError`. Commands carry a `*RunError` with `Step`, `StepID`, `Type`,
   `Message` and `OnFailure`.
9. **`Index` is ambiguous for plain steps.** It is -1 when the step has no
   `each`, so item 0 is distinguishable.
10. **Nothing in the brief used `ScheduleTimer`.** The spike gives it two
    users: a `sleep` control kind and a per-step `timeout`. A timeout emits
    `CancelStep` and fails the attempt with type `timeout`, which is
    retryable only if the step declares retry.
11. **Order independence has a limit.** With an `each` concurrency cap and
    a failing item, which items were dispatched before the failure depends
    on order. The property holds for successful completions; failures are
    deterministic per order.
12. **No jitter.** `Apply` cannot use randomness, so backoff is exact. A host
    may add jitter when it schedules (later is always safe).
13. **"Retry bound" is not a loop bound.** Retry repeats an attempt, not a
    cycle. The spike requires every cycle to contain a step with
    `max_visits` or a `sleep`.
14. **Static unsettled-read checks were partial.** Now replaced by typed
    definitions: a step not certain to have run reads as maybe-nil, and
    `coalesce(report, approve)` reads whichever ran. See
    `typed-definitions.md`.
15. **Numbers come back as float64.** Outputs, inputs and params are
    JSON-normalized so a reloaded state behaves the same. Hosts see
    `pr_number` as `float64(42)`.
16. **Leaving `experimental/*` alone was not compatible with "no backwards
    compatibility".** The spike deleted them. NoodleSpy imports
    `experimental/worker` and `experimental/store/postgres`, so adopting this
    branch means porting NoodleSpy to the `postgres` module.

## Not done

- Pause and resume. They fit as `Pause`/`Resume` events: a paused run
  dispatches nothing new and holds what it would have dispatched.
- Signals, waits and child workflows from the old library. In this design
  they are host kinds whose result arrives later (`ErrDeferred` in the
  Postgres host). No built-in signal table yet.
- A supersede rule ("a newer run for the same PR cancels the older one").
  The Postgres `runs.metadata` jsonb column has a GIN index for finding the
  older run; the rule itself is host policy.
- Postgres run listing and retention. `GetRun`, `Steps` and `Events` exist;
  no `ListRuns` yet.
- A per-step history of attempts in `State`. Only the latest attempt is kept;
  the Postgres `steps` table holds every attempt.
