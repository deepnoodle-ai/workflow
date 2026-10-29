# Worker shutdown: Shutdown, MayClaim and Active

**Status:** Accepted
**Author:** Curtis Myzie
**Date:** 2026-10-28
**Workflow:** Spec, then build in parallel. The PRD PR carries this spec;
the implementation stacks on it.
**PRD:** [prds/003-worker-drain.md](../prds/003-worker-drain.md) (#50, #51)

## Context

`postgres.Worker` has one stop signal, `Run`'s context, and it means "stop
claiming and cancel every handler now". The PRD adds a graceful stop
(`Shutdown`), a reversible claim gate (`MayClaim`) and a count of active
jobs (`Active`). The hard part is not the API. It is the states a worker
passes through while claims, handlers, heartbeats, settles and give-backs
are in progress, possibly across several `Run` calls on one `Worker`. This
spec fixes those states and the bound on each write, so the implementation
and its tests have one model.

## Goals

- `Shutdown(ctx)` returns nil when all active jobs settle before `ctx` is
  done, and `ctx.Err()` otherwise; in both cases only after every handler
  has returned and every claim is settled or given back (a failed hand-off
  write excepted, PRD R-5).
- No claim is kept after the claim gate closes, by `Shutdown` or by
  `MayClaim`, including one whose query was running at that moment.
- With any `Lease`, `Shutdown` with a deadline `d` returns within `d`, plus
  the handlers' stop time, plus 2 s (one 1 s write, then one 1 s
  give-back; jobs settle in parallel).
- A result or hand-off that arrives after the cut-off, or whose write is
  in progress at the cut-off, is still recorded when the database answers
  within 1 s.
- `MayClaim` false means no new claim query starts. A query that is
  already running finishes and its jobs are given back unrun; it is not
  cancelled. True resumes within one `Poll`.
- No change to the hard stop: every test in `shutdown_test.go` passes
  unchanged.
- `RunOnce` (#55) can reuse the gate and the counters without changing
  them.

## Non-goals

- Revision detection, signal handling, or any platform-specific code.
- A shutdown-timeout field on `Worker`, or restarting a worker after
  `Shutdown`.
- Changing `Concurrency` semantics: it stays per `Run` call.

## Proposal

### API

```go
type Worker struct {
    // ... existing fields ...

    // MayClaim, when set, is called before each claim and when a claim
    // returns. While it returns false the worker claims nothing, timers
    // included, and gives back a claim that returns; jobs it holds keep
    // running and settle. It must be fast and safe for concurrent use.
    // Nil means always.
    MayClaim func() bool

    mu       sync.Mutex
    runs     int                // claim loops in progress (Run, later RunOnce)
    active   int                // claimed jobs not yet settled or given back
    stopped  chan struct{}      // closed by the first Shutdown: claim nothing
    finished chan struct{}      // closed once stopped and runs == 0
    cut      context.Context    // done when a Shutdown deadline passes
    cutOff   context.CancelFunc
}

func (w *Worker) Shutdown(ctx context.Context) error
func (w *Worker) Active() int
```

`defaults()` also creates the unexported state, so a `Worker` built as a
struct literal keeps working, and tests that call `defaults()` and then
internal methods get it too. `Run` and `Shutdown` call `defaults()` under
`mu`, which also removes the race between two `Run` calls setting
defaults. Adding a `sync.Mutex` makes `Worker` non-copyable; it is always
used by pointer, and `go vet` flags a copy.

### States

```
            MayClaim() false
 claiming <------------------> gated            (reversible; per claim)
     |                           |
     +---------- Shutdown -------+
                    v
                 stopping   no claims; handlers and heartbeats run;
                    |       retried writes stop at lease end or cut-off
       all done     |     ctx done
     +--------------+--------------+
     v                             v
  finished (nil)                cut off   handler contexts cancelled;
                                   |      each write: one attempt, 1 s;
                                   |      unrecorded results given back
                                   v
                           finished (ctx.Err())

 Run's ctx cancelled, from any state: hard stop, as today.
```

`finished` means `stopped` and `runs == 0`. A claim loop holds `runs` from
its start until its own goroutines have returned, so `runs == 0` implies
`active == 0` and no claim query in progress. Waiting on `runs` rather than
`active` closes the race where `Shutdown` sees no active jobs while a claim
query is about to return rows.

### The claim gate (shared path)

```go
// mayClaim reports whether a claim loop may claim now.
func (w *Worker) mayClaim() bool  // !stopped && (MayClaim == nil || MayClaim())

// admit takes ownership of claimed jobs. If the gate closed while the
// claim query ran, it gives them back unrun and returns none.
func (w *Worker) admit(jobs []claimedJob) []claimedJob

// settled marks one admitted job as settled or given back.
func (w *Worker) settled()
```

`Run`'s loop calls `mayClaim` before each claim. When it is false, the loop
skips the claim and goes to its `select`, which also waits on `stopped`
(once; the loop then drops that case, so a closed channel does not spin
it). `admit` calls `mayClaim` again. If the gate is open, it adds the jobs
to `active` under `mu`. If it closed, it gives each job back. So for both
`Shutdown` and `MayClaim` the rule is: after the gate closes, no new job
starts, and `Active() == 0` means the worker holds nothing.

The claim query is never cancelled by the gate: cancelling it can lose the
`RETURNING` rows after the `UPDATE` committed, and those jobs would wait a
lease and count a claim.

`MayClaim` is called on the claim loop's goroutine, never under `mu`, and a
panic in it is not recovered (it is the application's bug, like a panic in
`Logger`).

### Run

1. Under `mu`: `defaults()`. If `stopped` is closed, log a warning ("Run
   called after Shutdown") and return nil. Else `runs++`. On return,
   after all of its goroutines: `runs--`, and close `finished` if `stopped`
   and `runs == 0`.
2. `hctx` is `ctx` also cancelled by `w.cut`:
   `stop := context.AfterFunc(w.cut, cancel)`, and `Run` calls `stop` when
   it returns, so a `Run` that ends before any `Shutdown` leaves no
   callback on `w.cut`. Handlers, heartbeats and the
   shutdown checks in `process` use `hctx`, so a cut-off takes the same
   path as a hard stop: interrupted handlers are given back; results,
   hand-offs and permanent failures are recorded.
3. The claim query keeps using `ctx`. `listen` runs on a child of `ctx`
   that `Run` cancels when it returns, so a stopped loop can exit.
4. Loop: if `stopped` and this `Run` has nothing running, return nil. If
   `mayClaim()`, claim and `admit`. Wait on `ctx`, `wake`, `Poll` or
   `stopped`. Each job goroutine runs `process`, then `settled()`, then
   decrements the local count and calls `notify()`.
5. When `ctx` ends while `stopped` is closed, log a warning: `Run`'s
   context ended during `Shutdown`, so active jobs are cancelled (a hard
   stop); give `Run` a context that you cancel only for a hard stop.

### Shutdown

```go
func (w *Worker) Shutdown(ctx context.Context) error {
    // under mu: defaults(); close stopped once; close finished if runs == 0
    // first call: log Info "shutting down" with the active count
    select {
    case <-finished:
        return nil
    case <-ctx.Done():
    }
    // log Warn "shutdown deadline passed; cancelling active jobs"
    w.cutOff() // idempotent
    <-finished
    return ctx.Err()
}
```

Several `Shutdown` calls may run at once. Each waits for `finished`; the
first context to end cuts off all of them. `Shutdown` before any `Run`
returns nil at once.

### Bounds on writes

Two timeouts, as constants: `giveBackTimeout = 5 * time.Second` (today's
value, used by `release` and the postpone in `unloadable`) and
`cutWriteTimeout = time.Second`.

| Write | Hard stop (today, unchanged) | Stopping, before cut-off | After cut-off |
|---|---|---|---|
| Record a result (`settle`) | retries until lease end | retries until lease end or cut-off | one attempt, 1 s; if not recorded, given back |
| Fire a timer (`settle`) | given back on first failure | retries until lease end or cut-off | one attempt, 1 s; then given back |
| Finish a hand-off (`finishDeferred`) | retries until lease end | retries until lease end or cut-off | one attempt, 1 s; on failure left to its lease |
| Fail a run (`abort`) | one transaction, until lease end | until lease end or cut-off | one attempt, 1 s |
| Postpone an unloadable job | one attempt, 5 s | one attempt, 5 s or cut-off | one attempt, 1 s |
| Give back (`release`) | one attempt, 5 s | one attempt, 5 s | one attempt, 1 s |

In the last column, the 1 s starts at the cut-off for a write that is in
progress at that moment, and at the write's start for a write that starts
later (a handler that ignores its context and returns after the cut-off).
A write in progress at the cut-off is not cancelled at once.

One helper derives the context for each retried write:

```go
// bound limits a write to deadline. Once a Shutdown deadline passes, it
// also limits the write to cutWriteTimeout from that moment, or from the
// write's start if the write starts later. It never inherits parent's
// cancellation.
func (w *Worker) bound(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc)
```

- If `w.cut` is not done: `WithDeadline(WithoutCancel(parent), deadline)`,
  plus `context.AfterFunc(w.cut, f)`, where `f` starts a `cutWriteTimeout`
  timer that cancels the write. The returned cancel func stops the
  `AfterFunc` and the timer.
- If `w.cut` is done: `WithTimeout(WithDeadline(WithoutCancel(parent),
  deadline), cutWriteTimeout)`, with no `AfterFunc`. `AfterFunc` on a
  context that is already done runs `f` at once, so registering one here
  would cancel the write before its query starts. Then a result or a
  hand-off that arrives after the cut-off would never be recorded.

`settle`, `finishDeferred`, `abort` and the postpone use it. `release`
keeps its own background context and takes `cutWriteTimeout` once `w.cut`
is done, else `giveBackTimeout`.

In the retry loops, "after the cut-off" is checked with `w.cut.Err()`, not
`parent.Err()`, because `hctx` is cancelled by an `AfterFunc` that may run
a moment later. The wait between attempts also ends when `w.cut` is done.
After the cut-off, a loop makes one attempt and no more: an attempt in
progress at the cut-off is that attempt, and a loop that was waiting
between attempts makes one attempt on the same context, which ends 1 s
after the cut-off. When that attempt fails, the loop stops.

When `settle` fails and `w.cut` is done, it gives the job back: the step
runs again either way, and a give-back does not count. A hand-off is not
given back, because that would run its handler again at once; left to its
lease, the other system has time to deliver first, which deletes the job.

So after the cut-off, each job makes at most one 1 s write and then one
1 s give-back. Jobs settle on their own goroutines, in parallel, and the
pool wait is inside each timeout. `Shutdown` therefore returns within its
deadline, plus the time handlers take to return after their context is
cancelled, plus 2 s. This sum sets the deadline advice in "Docs".

Heartbeats need no change: they run on `hctx`, which stays live while
stopping and ends at the cut-off.

### Active

`Active()` locks `mu` and returns `active`. It counts across `Run` calls.
It includes jobs whose handler returned and whose result is being
recorded, because those still hold a claim.

### Reuse by RunOnce (#55)

`RunOnce` is another claim loop: it registers in `runs`, calls `mayClaim`
and `admit`, uses `hctx`, and calls `settled`. `Shutdown` and `Active`
then cover it with no further change. To keep that easy, register and
unregister (`enter`, `leave`) are helpers, not inline code in `Run`.

### Words

Logs and docs say "shut down" and "active job", never "drain" (the #55/#56
track uses "drain" for "run all due work now") and never "in-flight".

### Docs

- `documentation/hosts.md` "Workers": add `Shutdown`, `MayClaim` and
  `Active`. Replace the Cloud Run sentence: `Lease` is independent of the
  grace period; give `Run` its own context; on `SIGTERM` call `Shutdown`
  with a deadline of about 7 s. After the deadline, the final writes can
  take 2 s ("Bounds on writes"), which leaves 1 s before `SIGKILL` for
  handlers to return and the process to exit. The `hosts.md` code sample
  uses `7*time.Second`.
  Say that `Shutdown` waits without limit for a handler that ignores its
  context.
- `llms.txt`: add the three names to the `Worker{...}` line with one
  sentence each.
- `README.md` and `doc.go`: no change. Neither lists `Worker` fields or
  stop behavior. `README.md`'s `Run(ctx)` snippet stays; it does not show
  a shutdown.

### Tests (`postgres/graceful_test.go`)

Each acceptance criterion in the PRD gets a test. Timing uses short leases
and deadlines (100 ms to 1 s) and channels, not sleeps, where possible:

- recorded before the deadline; cancelled and given back at the deadline
  (`claims` back to its prior value, `lease_owner` NULL);
- a result not recorded by the deadline (run row locked, `Lease` 1 minute,
  deadline 300 ms): `Shutdown` returns within 3 s and the job is given
  back;
- a result that arrives after the cut-off is recorded: the handler ignores
  its context and returns a result 200 ms after a 100 ms deadline; the
  step is recorded, not given back. The same for a hand-off: the job is
  deleted, not left to its lease;
- a write in progress at the cut-off is not cancelled at once:
  `settleHook` holds the write until 300 ms after the cut-off, then lets
  it through; the result is recorded;
- nothing claimed after `Shutdown`: a run started after the call keeps an
  unclaimed job while an active job finishes;
- a claim batch that returns after the gate closed is given back unrun,
  for `Shutdown` and for `MayClaim` (a test hook in `claim`, like the
  existing `settleHook`);
- heartbeats while stopping: handler longer than `Lease`, result recorded;
- `Shutdown` before `Run`, `Run` after `Shutdown`, two concurrent
  `Shutdown` calls;
- `MayClaim` false over several polls: zero claims of steps and timers,
  active job settles; flipped true: claimed without restarting `Run`;
- `Active` before, during and after; run with `-race`.

### Example

`ExampleWorker_Shutdown` in `postgres/example_test.go` (package
`postgres_test`). The body: `Run` on its own context,
`signal.NotifyContext(ctx, syscall.SIGTERM)`, two runs (one step ends
quickly, one waits on its context), the process sends itself `SIGTERM`
(`os.FindProcess(os.Getpid())` and `Signal`, which compiles everywhere),
`Shutdown` with a short deadline, and a second worker that runs the
given-back step at once, without waiting for the 30 s lease. Setup and
polling are helpers, so the body stays near 20 lines.

Examples cannot skip themselves, and `postgres/` has no `TestMain` today.
The implementation PR adds `postgres/main_test.go` with a `TestMain` that
sets `-test.skip ^Example` when `WORKFLOW_PG_DSN` is unset. It does not
wait for another PR. Other open work that adds a Postgres example adds the
same file, byte for byte, so whichever merges second has no conflict. CI
sets the DSN, so the example runs there.

## Alternatives considered

- **Make `Run`'s context a soft stop with a timeout field**, as River does
  with `SoftStopTimeout`. One signal for the application to wire.
  Rejected: it changes the meaning of every existing cancellation, and the
  deadline is known at the `SIGTERM` call site, not at construction.
  `http.Server` keeps the same split. The cost, that one shared context
  defeats `Shutdown`, is met with docs, the example and a warning log.
- **Track only `active`, no `runs`.** Simpler state. Rejected: a claim
  query in progress is invisible to `active`, so `Shutdown` could return
  before `admit` gives those jobs back, and the process would exit holding
  claims that count toward `MaxClaims`.
- **Cancel the running claim query when the gate closes.** Stops claiming
  sooner by one query. Rejected: the rows can be lost after commit (see
  "The claim gate").
- **Return from `Shutdown` at the deadline, like `http.Server`.**
  Rejected: the process would exit before cancelled handlers return and
  their claims are given back, which is the failure #50 is about.
- **Leave an unrecorded result to its lease after the cut-off, as the hard
  stop does.** Rejected: it counts a claim and delays the rerun by a
  lease; a give-back does neither.

## Tradeoffs and consequences

- `Worker` gains a mutex and lazily built state. It can no longer be
  copied by value after use; `go vet` catches a copy.
- `Shutdown` waits for handlers that ignore their context, without limit,
  as `Run` does today. A process that must exit on time relies on the
  platform's kill, and that claim counts.
- The 1 s bound after the cut-off is fixed. A database slower than that
  during a shutdown leaves the claim to its lease, which counts, as today.
- `MayClaim` is checked before each claim and once more for each batch, so
  it runs on every wake-up. A slow predicate slows claiming. The doc
  comment says it must be fast.

## Rollout

Additive API; no schema change, no migration. Existing callers that only
cancel `Run`'s context see no change. Ships in the next 0.x minor release;
the release notes point Cloud Run users to the new `hosts.md` advice.
