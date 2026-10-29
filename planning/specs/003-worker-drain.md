# Worker drain: Shutdown, MayClaim and Active

**Status:** Draft
**Author:** Curtis Myzie
**Date:** 2026-10-28
**Workflow:** Spec, then build in parallel. The PRD PR carries this spec;
the implementation stacks on it.
**PRD:** [prds/003-worker-drain.md](../prds/003-worker-drain.md) (#50, #51)

## Context

`postgres.Worker` has one stop signal, `Run`'s context, and it means "stop
claiming and cancel every handler now". The PRD adds a graceful stop
(`Shutdown`), a reversible claim gate (`MayClaim`) and an in-flight count
(`Active`). The hard part is not the API. It is the states a worker passes
through while claims, handlers, heartbeats, settles and give-backs are in
flight, possibly across several `Run` calls on one `Worker`. This spec fixes
those states and the bound on each write, so the implementation and its
tests have one model.

## Goals

- `Shutdown(ctx)` returns nil when all in-flight jobs settle before `ctx` is
  done, and `ctx.Err()` otherwise; in both cases only after every handler
  has returned and every claim is settled or given back.
- No claim is kept after `Shutdown` is called, including one whose query
  was in flight at the call.
- With the default 30 s `Lease`, `Shutdown` with a deadline `d` returns
  within `d` plus the handlers' stop time plus at most one give-back
  timeout (5 s), and in the normal case within milliseconds of that.
- `MayClaim` false means zero claim queries; true resumes within one `Poll`.
- No change to the hard stop: every test in `shutdown_test.go` passes
  unchanged.
- `RunOnce` (#55) can reuse the gate and the counters without changing
  them.

## Non-goals

- Revision detection, signal handling, or any platform-specific code.
- A drain-timeout field on `Worker`, or restarting a worker after
  `Shutdown`.
- Changing `Concurrency` semantics: it stays per `Run` call.

## Proposal

### API

```go
type Worker struct {
    // ... existing fields ...

    // MayClaim, when set, is called before each claim. While it returns
    // false the worker claims nothing, timers included, and waits for the
    // next wake-up or poll; jobs it holds keep running and settle. It must
    // be fast and safe for concurrent use. Nil means always.
    MayClaim func() bool

    mu      sync.Mutex
    runs    int                // claim loops in progress (Run, later RunOnce)
    active  int                // claimed jobs not yet settled or given back
    stopped chan struct{}      // closed by the first Shutdown: claim nothing
    drained chan struct{}      // closed once stopped and runs == 0
    cut     context.Context    // done when a Shutdown deadline passes
    cutOff  context.CancelFunc
}

func (w *Worker) Shutdown(ctx context.Context) error
func (w *Worker) Active() int
```

The unexported state is created lazily under `mu` (`w.init()`), so a
`Worker` built as a struct literal keeps working. Adding a `sync.Mutex`
makes `Worker` non-copyable; it is always used by pointer, and `go vet`
flags a copy.

### States

```
            MayClaim() false
 claiming <------------------> gated            (reversible; per claim)
     |                           |
     +---------- Shutdown -------+
                    v
                 stopping   no claims; handlers and heartbeats run;
                    |       settles retry until lease end or cut-off
       all done     |     ctx done
     +--------------+--------------+
     v                             v
  drained (nil)                 cut off   handler contexts cancelled;
                                   |      each later write: one attempt,
                                   |      bounded by giveBackTimeout
                                   v
                           drained (ctx.Err())

 Run's ctx cancelled, from any state: hard stop, as today.
```

`drained` means `stopped` and `runs == 0`. A claim loop holds `runs` from
its start until its own goroutines have returned, so `runs == 0` implies
`active == 0` and no claim query in flight. Waiting on `runs` rather than
`active` closes the race where `Shutdown` sees no active jobs while a claim
query is about to return rows.

### The claim gate (shared path)

```go
// mayClaim reports whether a claim loop may claim now.
func (w *Worker) mayClaim() bool  // !stopped && (MayClaim == nil || MayClaim())

// admit takes ownership of claimed jobs. If Shutdown was called while the
// claim query ran, it gives them back unrun and returns none.
func (w *Worker) admit(jobs []claimedJob) []claimedJob

// settled marks one admitted job as settled or given back.
func (w *Worker) settled()
```

`Run`'s loop calls `mayClaim` before each claim. When it is false, the loop
skips the claim and goes to its `select`, which now also waits on
`stopped`. `admit` increments `active` under `mu`, or calls `release` on
each job when `stopped` is closed. The claim query is never cancelled by
`Shutdown`: cancelling it can lose the `RETURNING` rows after the `UPDATE`
committed, and those jobs would wait a lease and count a claim. `MayClaim`
is not checked again in `admit`; one batch in flight when it flips runs.

`MayClaim` is called on the claim loop's goroutine, never under `mu`, and a
panic in it is not recovered (it is the application's bug, like a panic in
`Logger`).

### Run

1. Under `mu`: `init()`. If `stopped` is closed, return nil. Else
   `runs++`; on return `runs--` and close `drained` if `stopped` and
   `runs == 0`.
2. `hctx` is `ctx` that is also cancelled by `w.cut`
   (`context.AfterFunc(w.cut, cancel)`). Handlers, heartbeats and the
   shutdown checks in `process` and `settle` use `hctx`, so a cut-off takes
   the same path as a hard stop: interrupted handlers are given back, and
   results, hand-offs and permanent failures are recorded.
3. The claim query and `listen` keep using `ctx`, with `listen` on a child
   context cancelled when `Run` returns, so the drained loop can exit.
4. Loop: if `stopped` and this `Run` has nothing running, return nil. If
   `mayClaim()`, claim and `admit`. Wait on `ctx`, `wake`, `Poll` or
   `stopped`. Each finished job calls `settled()` and `notify()`.

### Shutdown

```go
func (w *Worker) Shutdown(ctx context.Context) error {
    // init; close stopped once; log Info with Active(); maybe close drained
    select {
    case <-drained:
        return nil
    case <-ctx.Done():
    }
    w.cutOff() // log Warn with Active(); idempotent
    <-drained
    return ctx.Err()
}
```

Several `Shutdown` calls may run at once. Each waits for `drained`; the
first context to end cuts off all of them. `Shutdown` before any `Run`
returns nil at once.

### Bounds on writes

| Write | Hard stop (today, unchanged) | Stopping, before cut-off | After cut-off |
|---|---|---|---|
| Record a result (`settle`) | retries until lease end | retries until lease end or cut-off | one attempt, `giveBackTimeout` |
| Fire a timer (`settle`) | given back on first failure | retries until lease end or cut-off, then given back | one attempt, then given back |
| Finish a hand-off (`finishDeferred`) | retries until lease end | retries until lease end or cut-off | one attempt; on failure left to the lease |
| Give back (`release`) | one attempt, 5 s | one attempt, 5 s | one attempt, 5 s |

`giveBackTimeout` is the 5 s that `release` already uses, named as a
constant and shared. `settle` and `finishDeferred` derive their context as
today (`WithoutCancel(parent)` with the lease deadline), then add
`context.AfterFunc(w.cut, cancel)`; when `w.cut` is already done at entry,
they use `giveBackTimeout` and do not retry. A hand-off is not given back
because that would run its handler again at once; left to its lease, the
other system has time to deliver first, which deletes the job.

Heartbeats need no change: they run on `hctx`, which stays live while
stopping and ends at cut-off.

### Active

`Active()` locks `mu` and returns `active`. It counts across `Run` calls.
It includes jobs whose handler returned and whose result is being
recorded, because those still hold a claim.

### Reuse by RunOnce (#55)

`RunOnce` is another claim loop: it registers in `runs`, calls `mayClaim`
and `admit`, uses `hctx`, and calls `settled`. `Shutdown` and `Active`
then cover it with no further change. To keep that easy, the per-job
goroutine body (`process`, then `settled`, then `notify`) is one helper
that both loops call.

### Docs

- `documentation/hosts.md` "Workers": add `Shutdown`, `MayClaim` and
  `Active`; replace the Cloud Run sentence: `Lease` is independent of the
  grace period; on `SIGTERM` call `Shutdown` with a deadline of about 8 s,
  which leaves about 2 s for the final writes.
- `llms.txt`: add the three names to the `Worker{...}` line with one
  sentence each.
- `README.md` and `doc.go`: no change. Neither lists `Worker` fields or
  stop behavior.

### Tests (`postgres/drain_test.go`)

Each acceptance criterion in the PRD gets a test. Timing uses short leases
and deadlines (100 ms to 1 s) and channels, not sleeps, where possible:

- recorded before the deadline; cancelled and given back at the deadline
  (`claims` back to its prior value, `lease_owner` NULL);
- nothing claimed after `Shutdown`: a run started after the call keeps an
  unclaimed job while an in-flight job drains;
- a claim in flight at the call is given back unrun (use a test hook in
  `claim`, like the existing `settleHook`);
- heartbeats during stopping: handler longer than `Lease`, result recorded;
- bounded by the deadline, not the lease: the run row locked, `Lease`
  1 minute, deadline 300 ms, `Shutdown` returns within 2 s;
- `Shutdown` before `Run`, `Run` after `Shutdown`, two concurrent
  `Shutdown` calls;
- `MayClaim` false over several polls: zero claims of steps and timers,
  in-flight job settles; flipped true: claimed without restarting `Run`;
- `Active` before, during and after; run with `-race`.

### Example

`ExampleWorker_Shutdown` in `postgres/example_test.go`, 10 to 20 lines in
the body: wire `signal.NotifyContext(ctx, syscall.SIGTERM)`, start two
steps (one ends quickly, one waits on its context), send the process
`SIGTERM` with `os.FindProcess(os.Getpid())` and `Signal` (portable to
compile), call `Shutdown` with a short deadline, and print each step's
outcome:

```
fast: completed
slow: given back (claims 0)
shutdown: context deadline exceeded
```

Examples cannot skip, so a `TestMain` in the module sets `-test.skip
^Example` when `WORKFLOW_PG_DSN` is unset. CI sets the DSN, so the example
runs there.

## Alternatives considered

- **Make `Run`'s context a soft stop with a `DrainTimeout` field**, as
  River does with `SoftStopTimeout`. One signal for the application to
  wire. Rejected: it changes the meaning of every existing cancellation,
  and the deadline is known at the `SIGTERM` call site, not at
  construction. `http.Server` keeps the same split.
- **Track only `active`, no `runs`.** Simpler state. Rejected: a claim
  query in flight is invisible to `active`, so `Shutdown` could return
  before `admit` gives those jobs back, and the process would exit holding
  claims that count toward `MaxClaims`.
- **Cancel the in-flight claim query on `Shutdown`.** Stops claiming
  sooner by one query. Rejected: the rows can be lost after commit (see
  "The claim gate").
- **Return from `Shutdown` at the deadline, like `http.Server`.**
  Rejected: the process would exit before cancelled handlers return and
  their claims are given back, which is the failure #50 is about.

## Tradeoffs and consequences

- `Worker` gains a mutex and lazily built state. It can no longer be
  copied by value after use; `go vet` catches a copy.
- `Shutdown` waits for handlers that ignore their context, without limit,
  as `Run` does today. A process that must exit on time relies on the
  platform's kill, and that claim counts.
- The final writes after the cut-off use a fixed 5 s bound that the
  application cannot tune. On Cloud Run, a slow database can outlast the
  remaining 2 s; the claim then counts, as it does today.
- `MayClaim` is checked per claim, so it runs on every wake-up. A slow
  predicate slows claiming. The doc comment says it must be fast.
- A `TestMain` that skips examples is new to the module. Another track
  that adds a `TestMain` must merge with it.

## Rollout

Additive API; no schema change, no migration. Existing callers that only
cancel `Run`'s context see no change. Ships in the next 0.x minor release;
the release notes point Cloud Run users to the new `hosts.md` advice.

## Open questions

- Should `giveBackTimeout` shrink from 5 s to about 1 s so that the worst
  case fits Cloud Run's remaining 2 s? Leaning no: the write is one
  indexed `UPDATE`, and a database that slow fails either way. Revisit if
  a test or a user shows otherwise.
