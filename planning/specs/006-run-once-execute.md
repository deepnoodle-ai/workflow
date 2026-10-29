# RunOnce and Execute: claim paths for on-demand work

**Status:** Draft
**Author:** Curtis Myzie
**Date:** 2026-10-28
**Workflow:** Spec, then wait for feedback, then build. The build stacks on
the worker-drain implementation branch (#50, #51), which restructures the
claim loop.
**PRD:** [006-run-once-execute.md](../prds/006-run-once-execute.md)

## Context

The PRD adds `Worker.RunOnce` (#55) and `Worker.Execute` (#56). Both touch
the one thing that keeps a step from running twice: the claim. `RunOnce`
adds a cutoff to the batch claim query. `Execute` adds a second claim
path, by key. The brief asks for a spec when the claim query changes. This
spec covers the two queries, why they stay exclusive under concurrency,
how a pass terminates, and where the code goes so that the four other
tracks that edit `worker.go` and `store.go` rebase cleanly.

## Goals

- At most one live claim per job, whatever mix of `Run`, `RunOnce` and
  `Execute` runs against it. Proven by a concurrent test under `-race`.
- `RunOnce` returns in finite time with no `ctx` deadline, under steady
  inflow and with a `sleep` loop that never ends.
- `Run`'s claim plan is unchanged: an index scan on `jobs_claim` per queue
  that stops at the limit (checked with `EXPLAIN` in a test or by hand).
- No migration.

## Non-goals

- A dispatch hook or relay. The PRD records why.
- Any change to heartbeats, settle, release or `MaxClaims` rules.

## Proposal

### 1. Cutoff on the batch claim

`claimSQL` changes one predicate:

```sql
WHERE queue = q.queue AND run_at <= least(now(), $5::timestamptz)
```

`Run` passes NULL. Postgres's `least` ignores NULLs, so `Run` claims
exactly as today. `RunOnce` passes T0, the database's `now()` read once
at the start of the pass (`SELECT now()`), because `persist` stamps due
jobs with the database clock too. The bound is a stable expression, so
the scan on `jobs_claim (queue, run_at, id)` keeps its range condition.

### 2. Claim by key

```sql
UPDATE {{schema}}.jobs t
SET lease_owner = $4, lease_expires = now() + make_interval(secs => $5),
    claims = t.claims + 1, fence = t.fence + 1
FROM {{schema}}.runs r
WHERE t.run_id = $1 AND t.step_id = $2 AND t.kind = $3 AND r.id = t.run_id
  AND t.queue = ANY($6::text[]) AND t.run_at <= now()
  AND (t.lease_expires IS NULL OR t.lease_expires < now())
RETURNING t.id, t.run_id, t.command, t.claims, t.fence, r.definition_hash
```

It finds the row through the `UNIQUE (run_id, step_id, kind)` index and
returns the same columns as `claimSQL`, so the result feeds `process`
unchanged.

Why it is exclusive. The `UPDATE` takes the row lock. A concurrent
`claimSQL` uses `SKIP LOCKED` and passes over the row. A concurrent claim
by key blocks on the lock. When the lock is released, READ COMMITTED
re-checks the `WHERE` clause against the new row version: the lease is
now live, so it updates zero rows. A settle that deletes the row while
the claim waits also leaves zero rows. Every claim adds 1 to the fence,
so the older holder of an expired lease is fenced out of heartbeat,
settle and release, as today.

When zero rows update, `Execute` reads the row once, without a lock, to
choose its answer: no row is "nothing to do" (nil error); otherwise
`ErrNotClaimable` with the reason (lease held until T, due at T, or no
handler for the kind). The read can race with another worker. Either
answer is safe: "nothing to do" only follows a row that is gone, and a
spurious `ErrNotClaimable` only makes Cloud Tasks retry.

### 3. What `process` returns

`process(ctx, job)` today returns nothing. It changes to return an
`outcome`: whether the job settled, the `Result` of the settle (for its
commands), and an error for "retry later". `settle` and `settleOnce`
return the `*Result` that `deliverLocked` already builds; today the
worker drops it. The paths map as follows.

| Path in `process` | `Execute` returns |
| --- | --- |
| Result or failure settled; abandoned step settled | `Settled: true`, follow-up jobs |
| `ErrDeferred` and the job deleted | `Settled: true`, no jobs |
| Run aborted (`MaxClaims`, definition unavailable) | `Settled: true`, no jobs |
| Lease lost, or `errLeaseLost` / `ErrRunNotFound` at settle | `Settled: false`, nil |
| Interrupted by `ctx`, claim given back | error matching `ctx.Err()` |
| Definition unavailable, job postponed | error wrapping `ErrDefinitionUnavailable` |
| Settle did not commit before the lease ran out | the last settle error |

`Run` ignores the outcome. `RunOnce` reads its follow-up jobs.

### 4. The pass

```text
T0 := SELECT now()
loop:
  if ctx done or the drain gate is closed: stop claiming
  fill free slots: first follow-ups by key, then claimSQL(limit = free, cutoff = T0)
  each finished job adds to follow-ups: its result's steps with no
    NotBefore or one already past; never timers
  if nothing running and nothing left to claim: return
  wait until a job finishes or ctx is done
```

`RunOnce` does not `LISTEN` and does not poll. It waits only on its own
jobs. A follow-up that `ErrNotClaimable` or "nothing to do" answers is
dropped: another worker has it. The first claim error stops claiming;
`RunOnce` waits for in-flight jobs and returns it.

Why it terminates. (1) Jobs with `run_at <= T0` are finite. A due job
takes `now()`, the start time of the transaction that writes it. So a job
that commits after T0 can have `run_at <= T0` only if its transaction was
already open at T0, or if a writer with a slow clock set its `NotBefore`.
Both sets are finite: no new transaction starts before T0, and the slow
clock passes T0 in bounded time. (2) Re-claims of one job are bounded
by `MaxClaims`, and a postponed job moves past T0. (3) Each follow-up
chain is finite: every cycle in a definition has `max_visits` or a
`sleep`; a `sleep` must be positive and is a timer; a pass never follows
a timer; retries are bounded by `max_attempts`.

### 5. Concurrency of calls

`Execute` runs on many request goroutines at once. Today `defaults()`
writes the `Worker`'s fields on every `Run`, which is a data race under
concurrent calls. It moves behind a `sync.Once` field. `go vet`'s
copylocks check then flags a `Worker` copied after use. The PRD says the
fields must not change after the first call.

### 6. Where the code goes

- `postgres/jobref.go` (new): `JobRef`, `Result.Jobs()`, the
  command-to-reference mapping.
- `postgres/once.go` (new): `RunOnce`, `Execute`, `Execution`,
  `ErrNotClaimable`, the claim-by-key query.
- `postgres/worker.go`: the cutoff parameter, `process` and `settle`
  returning an outcome, `sync.Once`. Nothing else moves.
- `postgres/store.go`: no change.
- Tests: `postgres/once_test.go`, `postgres/execute_test.go`,
  `postgres/example_test.go`.

Example tests need Postgres and cannot skip. When `WORKFLOW_PG_DSN` is
unset, a `TestMain` sets `-test.run` to exclude `^Example`, unless the
caller set `-run`. If worker-drain adds such a `TestMain` first, reuse it.
CI sets the DSN, so the examples always run there.

## Alternatives considered

- **Claim by `jobs.id`.** One integer, the primary key. Rejected: the
  host cannot learn it without `INSERT ... RETURNING` through `persist`,
  which other tracks change, and `StartTx` callers would get it only
  through new `Result` plumbing. The natural key comes from the commands
  we already return.
- **`SELECT ... FOR UPDATE NOWAIT`, then `UPDATE`, in a transaction.** It
  tells "busy" from "gone" in one round trip. Rejected: two statements and
  a transaction on the hot path, to save a read on the cold path.
- **Chase by run instead of by job:** claim `run_id = ANY(touched runs)`
  with no cutoff. It also catches child runs and outside deliveries for
  those runs. Rejected: a run that loops over a short `sleep` keeps the
  pass alive while any other job runs, so it may not terminate.
- **`Run` with a "return when idle" option.** Rejected: `Run` wakes on
  `LISTEN` and `Poll`, so "idle" is a race with inflow, and the loop would
  still not terminate under load.

## Tradeoffs and consequences

- `claimSQL` gains a parameter that `Run` always passes as NULL. That is
  one more thing to keep in the SQLite storage contract (#53).
- Work that arrives from outside during a pass, and child runs started by
  a handler, wait for the next pass. With a one-minute schedule, that is
  up to a minute per hop.
- In push mode, a lost push is recovered only by the next scheduled pass.
  The library cannot do better without a relay.
- `Execute`'s "not claimed" path costs a second query. It is the rare path.

## Rollout

Additive API, no migration, no change to `Run`'s behavior. The
implementation branch `feat/run-once-execute` starts from worker-drain's
implementation branch and merges the PRD branch. If the observer track
(#54) lands first, the claim-by-key path fires the same "claimed" event as
the batch claim.

## Open questions

- The shape of worker-drain's "stop claiming" gate. `RunOnce` and
  `Execute` check it before each claim. The answer changes only where the
  check sits, not the design. Resolve when the branch exists.
