# Stop a worker without cutting off its steps

Status: Draft
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
in flight.

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

Secondary user: the same developer's process-wide drain code (readiness
probe, shutdown sequence), which must know whether the worker still holds
work.

## Concepts

- **Shutdown**: a one-way, graceful stop. The worker stops claiming at
  once, lets running steps finish until a deadline, then cancels the rest
  and gives their claims back. Same model as `http.Server.Shutdown`.
  Cancelling `Run`'s context stays the hard stop.
- **Claim gate (`MayClaim`)**: a function the application sets. While it
  returns false, the worker claims nothing and keeps working on what it
  holds. It is reversible. Shutdown closes the same gate for good.
- **In-flight job**: a job this worker has claimed and not yet settled or
  given back. `Active()` returns how many there are.
- **Give back** (existing term): release a claim so another worker can take
  it at once, without counting toward `MaxClaims`.

## Use cases

### UC-1: Deploy on Cloud Run without cutting off steps

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
defer stop()
w := &postgres.Worker{Store: store, Handlers: handlers}
go w.Run(context.Background())
<-ctx.Done()
sctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
defer cancel()
if err := w.Shutdown(sctx); err != nil {
    log.Printf("drain cut off: %v", err)
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
- [ ] Nothing is claimed after `Shutdown` is called: a step or timer that
      becomes due after the call stays unclaimed. A claim query that was
      already running when `Shutdown` was called gives its jobs back
      without running them.
- [ ] While `Shutdown` waits, heartbeats go on: a handler that runs longer
      than `Lease` before the deadline keeps its claim, and its result is
      recorded.
- [ ] `Shutdown` returns nil when every in-flight job finished before the
      deadline. It returns `ctx.Err()` when the deadline cut some off. In
      both cases it returns only after every handler has returned and each
      claim is settled or given back.
- [ ] After `Shutdown`, `Run` returns nil.
- [ ] With a 1-minute `Lease`, a `Shutdown` with a 300 ms deadline
      returns within 2 s, even when a result cannot be delivered (run row
      locked). The lease does not bound it.
- [ ] `Shutdown` with no `Run` in progress returns nil at once. `Run`
      called after `Shutdown` returns nil without claiming.
- [ ] An `Example` test shows `SIGTERM` handling with `Shutdown`, and its
      output shows one step recorded and one given back.

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

When `superseded` flips, the worker stops claiming, finishes its in-flight
steps, and keeps running idle until the platform stops it.

Acceptance:
- [ ] While `MayClaim` returns false, no step and no timer is claimed, over
      several `Poll` intervals.
- [ ] Jobs claimed before it returned false keep heartbeating, finish and
      settle normally.
- [ ] When it returns true again, claiming resumes within one `Poll`,
      without restarting `Run`.
- [ ] A nil `MayClaim` claims as today.

### UC-3: Include the worker in a process-wide drain

```go
if n := w.Active(); n > 0 {
    log.Printf("waiting for %d workflow jobs", n)
}
```

Acceptance:
- [ ] `Active()` is 0 before `Run`, equals the number of claimed jobs while
      they run or settle, and is 0 again once each is settled or given
      back.
- [ ] `Active()` is safe to call from any goroutine (the race detector is
      clean).

## Requirements

- R-1: `func (w *Worker) Shutdown(ctx context.Context) error` stops all
  claiming (steps and timers) at once, for every `Run` call on the worker.
- R-2: Until `ctx` is done, in-flight handlers run with their heartbeats.
  Results that arrive are recorded as today.
- R-3: When `ctx` is done, the worker cancels the remaining handlers. A
  handler that then returns the context's error or a retryable error is
  given back, without counting toward `MaxClaims`. A result, a hand-off or
  a permanent failure is recorded, as on a hard stop today.
- R-4: During `Shutdown`, retries to record a result stop when `ctx` is
  done, not when the lease runs out. After that, each remaining write
  (record or give back) makes one bounded attempt.
- R-5: `Shutdown` returns when every handler has returned and every claim
  is settled or given back. `Run` then returns nil.
- R-6: `Worker.MayClaim func() bool`: when set and false, the worker skips
  the claim and waits for the next wake-up or poll. It is checked before
  every claim. In-flight jobs are not affected.
- R-7: `MayClaim` and `Shutdown` use one internal "may claim" check, so a
  new claim path (such as `RunOnce` in #55) gets both.
- R-8: `func (w *Worker) Active() int` returns the in-flight job count
  across all `Run` calls of the worker.
- R-9: Cancelling `Run`'s context keeps its meaning: a hard stop, with
  settling bounded by the lease.
- R-10: `documentation/hosts.md` says that `Lease` is independent of the
  platform's grace period, and that on `SIGTERM` the application calls
  `Shutdown` with a deadline of about 8 s on Cloud Run. `llms.txt` lists
  `MayClaim`, `Shutdown` and `Active`.

## Not in scope

- Revision or platform detection (Cloud Run `K_REVISION`, Kubernetes
  labels). The application owns detection; `MayClaim` is the hook.
- Signal handling inside the library. The application wires `SIGTERM`, as
  with `http.Server`.
- A drain-timeout field on `Worker`. The deadline is the `Shutdown` context.
- `RunOnce` and `Execute` (#55, #56). That track stacks on this one and
  reuses the claim gate and the in-flight count.
- Restarting a worker after `Shutdown`. Create a new `Worker`.
- Metrics and a "draining" status method. `Active()` and the application's
  own knowledge that it called `Shutdown` cover the use cases.

## Decisions

- **Cancelling `Run`'s context stays a hard stop.** Existing callers and
  `shutdown_test.go` depend on it, and `http.Server` has the same split
  (`Close` vs `Shutdown`). Rejected: make it a soft stop with a
  `DrainTimeout` field, as River does. That silently turns every existing
  hard stop into a wait, and the deadline belongs where the application
  knows the platform's budget: at the `Shutdown` call.
- **`Shutdown` returns `ctx.Err()` when the deadline cut work off, and nil
  otherwise.** The application can log a cut-off drain. It still waits for
  the cancelled handlers to return and for their claims to be given back,
  because returning earlier lets the process exit with claims that then
  count toward `MaxClaims`. Rejected: return at the deadline like
  `http.Server.Shutdown`. Rejected: always nil, which hides a cut-off.
- **After the deadline, each remaining write makes one attempt, bounded by
  the 5 s give-back timeout the worker already uses.** Retrying until the
  lease runs out would outlive `SIGKILL`. A write normally takes
  milliseconds, so the 8 s advice leaves 2 s of the 10 s grace for it.
  Rejected: a new `Worker` field for this bound. No user asked for it.
- **A claim query that is running when `Shutdown` is called finishes, and
  its jobs are given back unrun.** Cancelling the query can lose the
  `RETURNING` rows after the claim committed, and those jobs would then
  wait a lease and count a claim. `MayClaim` has no such re-check: it is
  advisory, and one batch in flight when it flips runs normally.
- **The name is `Active() int`.** It is short and reads well at the call
  site (`w.Active() > 0`). It counts claimed jobs, which is what holds the
  process. Rejected: `InFlight()`, `Running()` (it also counts jobs that
  are settling, not only running handlers).
- **`MayClaim` is a field, `func() bool`, called from the claim loop
  before each claim.** The application already has the answer as a flag;
  a predicate reads it at the moment it matters, with no extra goroutine.
  It must be fast and safe for concurrent use. Rejected: `Pause()` and
  `Resume()` methods, which need the application to forward each change.
  Rejected: `func(kind string) bool`, which no use case needs.
- **`Shutdown` covers every `Run` call on the worker, and `Run` after
  `Shutdown` returns nil at once.** The worker is one unit for the
  application's drain. Rejected: an `ErrWorkerClosed` sentinel; the brief
  wants `Run` to return nil after `Shutdown`, and a new error adds surface
  for no use case.
- **A deferred hand-off whose final write fails after the deadline is not
  given back.** Giving it back runs the handler again at once (a second
  hand-off). Leaving it to its lease gives the other system time to
  deliver first. Step results and timers that fail are handled as today.
- **New tests go in `postgres/drain_test.go`, next to `shutdown_test.go`.**
  Other tracks change worker files at the same time; a new file avoids
  conflicts.

## Success

A developer on Cloud Run keeps the default 30 s `Lease`, calls `Shutdown`
with an 8 s deadline on `SIGTERM`, and sees no `abandoned` steps from
deploys. Steps that finish within the deadline are never run twice
because of a deploy.

Must not get worse: the hard stop on `Run`'s context (all tests in
`shutdown_test.go` pass unchanged), claim throughput (no new query per
claim), and fencing (a given-back claim stays fenced out).

## Risks and open questions

- A handler that ignores its context blocks `Shutdown` after the deadline,
  as it blocks `Run` today. The platform's `SIGKILL` ends it; that claim
  counts. The docs say handlers must honor their context. Not blocking.
- `MayClaim` false on every worker stops all timers and steps. That is the
  application's choice; the docs say so. Not blocking.
- The `Example` needs Postgres, and the module has no `Example` test yet.
  The tech spec says how it skips without `WORKFLOW_PG_DSN`. Not blocking.
