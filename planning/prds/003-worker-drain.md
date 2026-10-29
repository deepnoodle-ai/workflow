# Stop a worker without cutting off its steps

Status: Approved
PRD PR: #TBD  Implementation PR: #TBD  Updated: 2026-10-28
Issues: #50, #51
Tech spec: [specs/003-worker-drain.md](../specs/003-worker-drain.md)

## Problem

A Go developer runs `postgres.Worker` on Cloud Run or Kubernetes and deploys
often. Each deploy stops the old instances. The worker gives that developer
one way to stop: cancel the context passed to `Run`. That stops claiming and
cancels every running handler at the same moment. A step that is one second
from its result is interrupted and runs again on another worker.

Cloud Run sends `SIGTERM` and then `SIGKILL` 10 seconds later, and the
developer cannot extend that period. Today `Run` returns within about one
`Lease` of cancellation, because settling is bounded by the lease. So
`documentation/hosts.md` tells the developer to keep `Lease` under the
grace period. That gives a lease of about 8 s, a heartbeat every 2.7 s, and
any database interruption longer than 8 s cancels every running step.

A claim that `SIGKILL` cuts off is not given back, so it counts toward
`MaxClaims` (default 5). A long step that five deploys interrupt fails as
`abandoned`, although no handler crashed.

During a deploy, old Cloud Run revisions can keep running for up to about
15 minutes. With CPU always allocated, their workers keep claiming, so old
and new handler code take turns on steps. An application that can detect
that it was superseded cannot tell the worker "stop claiming, but finish
what you hold". Its only tool, cancelling the context, also stops the work
it holds.

If we do nothing, each developer picks between fragile leases and steps cut
off at every deploy, and long steps fail as `abandoned` on busy deploy days.

## Context

What the worker does today (`postgres/worker.go`):

- `Run(ctx)` claims up to `Concurrency` jobs with `FOR UPDATE SKIP LOCKED`,
  wakes on `LISTEN` or every `Poll`, and runs each job in a goroutine with
  a heartbeat every `Lease/3`. The count of running jobs is a local
  variable of `Run`. Nothing outside `Run` can read it.
- When `ctx` is done, every handler context is cancelled. A handler that
  returns the context's error or a retryable error is given back with
  `release`, which refunds the claim (`claims = claims - 1`). A result, a
  hand-off or a permanent failure is still recorded, bounded by the lease.
  `postgres/shutdown_test.go` pins this down.
- A claim that the process loses without `release` (a kill, a crash) waits
  for its lease and counts toward `MaxClaims`.
- There is no way to pause claiming. The only gate is `ctx`.

How comparable Go and job-queue libraries do it:

| Library | Stop claiming, keep working | Deadline, then cancel | What happens to the cut-off job |
|---|---|---|---|
| `net/http` `Server` | `Shutdown(ctx)` closes listeners at once | `ctx`; `Close` is the hard stop | Connections are closed |
| River (Go, Postgres) | `Stop(ctx)`; cancelling the `Start` context is a soft stop | `SoftStopTimeout`, `StopAndCancel` | The handler must return an error; it is retried |
| asynq (Go, Redis) | `Stop()`: stop pulling tasks (one-way) | `Shutdown()` with `ShutdownTimeout`, default 8 s | Pushed back to Redis |
| Sidekiq (Ruby) | `TSTP` "quiet" (one-way) | `TERM` with `-t`, default 25 s | Pushed back to Redis |
| Temporal Go SDK | `Worker.Stop()` | `WorkerStopTimeout` | Activity context cancelled; retried by the server |

Each library separates "stop claiming" from "cancel running work", and puts
a deadline between them. asynq picked 8 s as its default for the same
10-second platforms. Sidekiq's "quiet" and asynq's `Stop` are the one-way
form of the claim gate that #51 asks for. None of them makes the gate
reversible, and none detects platform revisions itself.

What developers do now: set `Lease` to about 8 s and accept the fragile
heartbeats, or keep the default lease and lose results to `SIGKILL`.

## Users and job

Primary user: a Go developer who runs `postgres.Worker` in a service that
deploys often on Cloud Run or Kubernetes. The job: "Let me deploy at any
time without cutting off steps that are about to finish, without tuning the
lease to the platform, and without long steps failing as abandoned."

Secondary user: the same developer's process-wide shutdown code (readiness
probe, shutdown sequence), which must know whether the worker still holds
work.

## Concepts

- **Shutdown**: a one-way, graceful stop. The worker stops claiming at
  once, lets running steps finish until a deadline, then cancels the rest
  and gives their claims back. Same model as `http.Server.Shutdown`.
  Cancelling `Run`'s context stays the hard stop, so `Run` gets its own
  context, which the application cancels only for a hard stop.
- **Claim gate (`MayClaim`)**: a function the application sets. While it
  returns false, the worker claims nothing and keeps working on what it
  holds. It is reversible. Shutdown closes the same gate for good.
- **Active job**: a job this worker has claimed and not yet settled or
  given back. `Active()` returns how many there are.
- **Give back** (existing term): release a claim so another worker can take
  it at once, without counting toward `MaxClaims`.

## Use cases

### UC-1: Deploy on Cloud Run without cutting off steps

```go
w := &postgres.Worker{Store: store, Handlers: handlers}
go w.Run(context.Background()) // cancel this context only for a hard stop

sigs, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
defer stop()
<-sigs.Done()
ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
defer cancel()
if err := w.Shutdown(ctx); err != nil {
    log.Printf("shutdown cut off active jobs: %v", err)
}
```

A step that ends 2 s after `SIGTERM` is recorded. A step that needs 60 s is
cancelled at 8 s and given back. Another instance claims it, and that claim
is its first, not its second.

Acceptance:
- [ ] A handler that returns before the shutdown deadline has its result
      recorded, and the run advances.
- [ ] A handler still running at the deadline sees its context cancelled.
      Its claim is given back: `claims` is back to its value before the
      claim, and `lease_owner` is NULL.
- [ ] A step result that is not recorded by the deadline (the run row is
      locked) is given back. It does not wait for its lease.
- [ ] Nothing is claimed after `Shutdown` is called: a step or timer that
      becomes due after the call stays unclaimed. A claim query that was
      already running when `Shutdown` was called gives its jobs back
      without running them.
- [ ] While `Shutdown` waits, heartbeats go on: a handler that runs longer
      than `Lease` before the deadline keeps its claim, and its result is
      recorded.
- [ ] `Shutdown` returns nil when every active job finished before the
      deadline. It returns `ctx.Err()` when the deadline cut some off. In
      both cases it returns only after every handler has returned and each
      claim is settled or given back (except the hand-off case in R-5).
- [ ] After `Shutdown`, `Run` returns nil.
- [ ] With a 1-minute `Lease`, a `Shutdown` with a 300 ms deadline
      returns within 2 s, even when a result cannot be delivered (run row
      locked). The lease does not bound it.
- [ ] When `Run`'s context ends while a `Shutdown` is in progress, the
      worker logs a warning that says the hard stop overrode the shutdown
      and that `Run` needs its own context.
- [ ] `Shutdown` with no `Run` in progress returns nil at once. `Run`
      called after `Shutdown` returns nil without claiming, and logs a
      warning.
- [ ] An `Example` test shows `SIGTERM` handling with `Shutdown`, with
      `Run` on its own context. Its output shows one step recorded and one
      given back.

### UC-2: Stop an old revision from claiming

```go
var superseded atomic.Bool // the app sets it when a newer revision is live
w := &postgres.Worker{
    Store:    store,
    Handlers: handlers,
    MayClaim: func() bool { return !superseded.Load() },
}
go w.Run(ctx)
```

When `superseded` flips, the worker stops claiming, finishes its active
jobs, and keeps running idle until the platform stops it.

Acceptance:
- [ ] While `MayClaim` returns false, no step and no timer is claimed, over
      several `Poll` intervals.
- [ ] Jobs claimed before it returned false keep heartbeating, finish and
      settle normally.
- [ ] A claim query that is running when `MayClaim` turns false gives its
      jobs back without running them. After the gate closes, no new job
      starts.
- [ ] When it returns true again, claiming resumes within one `Poll`,
      without restarting `Run`.
- [ ] A nil `MayClaim` claims as today.

### UC-3: Include the worker in a process-wide shutdown

```go
if n := w.Active(); n > 0 {
    log.Printf("waiting for %d active workflow jobs", n)
}
```

Acceptance:
- [ ] `Active()` is 0 before `Run`, equals the number of claimed jobs while
      they run or settle, and is 0 again once each is settled or given
      back.
- [ ] With the gate closed (`MayClaim` false), `Active() == 0` means the
      worker holds no claim: a late claim batch is given back before it
      would be counted.
- [ ] `Active()` is safe to call from any goroutine (the race detector is
      clean).

## Requirements

- R-1: `func (w *Worker) Shutdown(ctx context.Context) error` stops all
  claiming (steps and timers) at once, for every `Run` call on the worker.
- R-2: Until `ctx` is done, active handlers run with their heartbeats.
  Results that arrive are recorded as today.
- R-3: When `ctx` is done (the cut-off), the worker cancels the remaining
  handlers. A handler that then returns the context's error or a retryable
  error is given back, without counting toward `MaxClaims`. A result, a
  hand-off or a permanent failure is recorded, as on a hard stop today.
- R-4: During `Shutdown`, every write the worker retries stops retrying at
  the cut-off: recording a result, firing a timer, finishing a hand-off,
  failing a run (`abort`) and postponing a job whose definition does not
  load. After the cut-off, each remaining write makes one attempt, bounded
  by 1 s. A step result or a timer that is still not recorded is given
  back.
- R-5: `Shutdown` returns when every handler has returned and every claim
  is settled or given back. One exception: a hand-off whose final write
  fails after the cut-off is left to its lease, not given back, because a
  give-back runs its handler again at once. `Run` then returns nil.
- R-6: `Worker.MayClaim func() bool`: when set and false, the worker skips
  the claim and waits for the next wake-up or poll. It is checked before
  every claim, and again when a claim query returns; a batch that returns
  with the gate closed is given back unrun. Active jobs are not affected.
- R-7: `MayClaim` and `Shutdown` use one internal claim gate, so a new
  claim path (such as `RunOnce` in #55) gets both.
- R-8: `func (w *Worker) Active() int` returns the number of active jobs
  across all `Run` calls of the worker.
- R-9: Cancelling `Run`'s context keeps its meaning: a hard stop, with
  settling bounded by the lease and a give-back bounded by 5 s. When it
  happens during a `Shutdown`, the worker logs a warning.
- R-10: `documentation/hosts.md` says that `Lease` is independent of the
  platform's grace period, that `Run` gets its own context, and that on
  `SIGTERM` the application calls `Shutdown` with a deadline of about 8 s
  on Cloud Run. It says `Shutdown` waits without limit for a handler that
  ignores its context. `llms.txt` lists `MayClaim`, `Shutdown` and
  `Active`. Logs and docs say "shut down", never "drain".

## Not in scope

- Revision or platform detection (Cloud Run `K_REVISION`, Kubernetes
  labels). The application owns detection; `MayClaim` is the hook.
- Signal handling inside the library. The application wires `SIGTERM`, as
  with `http.Server`.
- A shutdown-timeout field on `Worker`. The deadline is the `Shutdown`
  context.
- `RunOnce` and `Execute` (#55, #56). That track stacks on this one and
  reuses the claim gate and the active-job count.
- Restarting a worker after `Shutdown`. Create a new `Worker`.
- Metrics and a "shutting down" status method. `Active()` and the
  application's own knowledge that it called `Shutdown` cover the use
  cases.

## Decisions

- **Cancelling `Run`'s context stays a hard stop.** Existing callers and
  `shutdown_test.go` depend on it, and `http.Server` has the same split
  (`Close` vs `Shutdown`). Rejected: make it a soft stop with a timeout
  field, as River does. That silently turns every existing hard stop into
  a wait, and the deadline belongs where the application knows the
  platform's budget: at the `Shutdown` call. The cost is that the usual
  "one root context for everything" wiring defeats `Shutdown`; the docs,
  the `Example` and a warning log address that.
- **`Shutdown` returns `ctx.Err()` when the deadline cut work off, and nil
  otherwise.** The application can log a cut-off. It still waits for the
  cancelled handlers to return and for their claims to be given back,
  because returning earlier lets the process exit with claims that then
  count toward `MaxClaims`. Rejected: return at the deadline like
  `http.Server.Shutdown`. Rejected: always nil, which hides a cut-off.
- **After the cut-off, each write makes one attempt, bounded by 1 s. The
  hard stop keeps its 5 s give-back bound.** 8 s of shutdown plus a 5 s
  write is more than Cloud Run's 10 s. One indexed `UPDATE` takes
  milliseconds, so 1 s is ample, and the 8 s advice leaves room for it.
  Rejected: a new `Worker` field for this bound. No user asked for it.
- **A step result that is not recorded by the cut-off is given back.** The
  step runs again either way; a give-back lets another worker run it at
  once and does not count toward `MaxClaims`. Rejected: leave it to its
  lease, as the hard stop does, which counts a claim.
- **A hand-off whose final write fails is left to its lease.** A give-back
  runs its handler again at once (a second hand-off). Left to its lease,
  the other system has time to deliver first, which deletes the job.
- **The claim gate is checked before a claim and again when it returns,
  for `Shutdown` and `MayClaim` alike.** A batch that returns with the gate
  closed is given back unrun. Then `Active() == 0` with the gate closed
  means the worker holds nothing. The cost is one extra call to `MayClaim`
  for each batch. The claim query itself is never cancelled: cancelling
  it can lose the `RETURNING` rows after the claim committed.
- **The name is `Active() int`, and the concept is "active job"
  everywhere.** It is short and reads well at the call site
  (`w.Active() > 0`). It counts claimed jobs, which is what holds the
  process. Rejected: `InFlight()`, `Running()` (it also counts jobs that
  are settling, not only running handlers).
- **"Shut down", not "drain", in this track's logs and docs.** The #55/#56
  track uses "drain" for "run all due work now". One word, one meaning.
- **`MayClaim` is a field, `func() bool`, called from the claim loop.** The
  application already has the answer as a flag; a predicate reads it at
  the moment it matters, with no extra goroutine. It must be fast and safe
  for concurrent use. Rejected: `Pause()` and `Resume()` methods, which
  need the application to forward each change. Rejected:
  `func(kind string) bool`, which no use case needs.
- **`Shutdown` covers every `Run` call on the worker, and `Run` after
  `Shutdown` returns nil at once with a warning log.** The worker is one
  unit for the application's shutdown. The warning makes a reused worker
  visible. Rejected: an `ErrWorkerClosed` sentinel; the brief wants `Run`
  to return nil after `Shutdown`, and a new error adds surface for no use
  case.
- **`Shutdown` waits without limit for a handler that ignores its
  context.** `Run` does the same today, and a platform with a long grace
  period (Kubernetes) gets the most from waiting. The platform's kill ends
  such a handler on a short-grace platform, and that claim counts. The
  docs say so.
- **New tests go in `postgres/graceful_test.go`, next to
  `shutdown_test.go`.** Other tracks change worker files at the same time;
  a new file avoids conflicts.

## Success

A developer on Cloud Run keeps the default 30 s `Lease`, calls `Shutdown`
with an 8 s deadline on `SIGTERM`, and sees no `abandoned` steps from
deploys. Steps that finish within the deadline are never run twice
because of a deploy.

Must not get worse: the hard stop on `Run`'s context (all tests in
`shutdown_test.go` pass unchanged), claim throughput (no new query per
claim), and fencing (a given-back claim stays fenced out).

## Risks and open questions

- An application that passes its `SIGTERM` context to `Run` gets a hard
  stop, not a shutdown. Reduced by the docs, the `Example` and the warning
  log. Not blocking.
- `MayClaim` false on every worker stops all timers and steps. That is the
  application's choice; the docs say so. Not blocking.
- The `Example` needs Postgres. `postgres/main_test.go` (shared with other
  tracks) skips examples without `WORKFLOW_PG_DSN`. Not blocking.
