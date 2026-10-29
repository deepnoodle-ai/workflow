# Run due work on demand: RunOnce and Execute

Status: Approved
PRD PR: #61
Implementation PR: #<n>
Issues: #55, #56
Updated: 2026-10-28
Tech spec: [006-run-once-execute.md](../specs/006-run-once-execute.md)

## Problem

A Go developer deploys the postgres host on a platform that scales to zero
or gives CPU only while a request is in flight, such as Cloud Run. Today
the only way to run jobs is `Worker.Run`, a loop that must keep running.
On such a platform the loop does not run:

- With zero minimum instances there is often no instance. A `sleep: 24h`
  fires only when the next instance happens to start for some other
  request. A step dispatched by `Deliver` waits the same way.
- With request-based billing, Cloud Run throttles the CPU of an instance
  that has no request in flight. A `go w.Run(ctx)` in such a service runs
  steps at a crawl between requests.
- An instance with no request in flight looks idle. Cloud Run can stop it
  with 10 seconds' notice while a step runs. Background work is invisible
  to the platform's request draining.

The developer wants two things. First, a "drain now" call that Cloud
Scheduler or a Cloud Run job can make: run what is due, then return
(#55). Second, a way to push each job as its own HTTP request, through
Cloud Tasks, so that the platform protects the step for the length of the
request (#56).

If we do nothing, the developer must pay for a warm instance with CPU
always allocated, or write their own claim and settle code against our
tables. That code would skip the fence, and a duplicate delivery would
run a step twice and apply a stale result.

## Context

**Our code.** `Worker.Run` (postgres/worker.go) loops: it claims up to
`Concurrency` due jobs with one query (`claimSQL`, `FOR UPDATE SKIP
LOCKED`), runs each in a goroutine with a heartbeat, and settles it. A
settle delivers the result and deletes the job in one transaction, fenced
by the claim's token. It wakes on `LISTEN` or every `Poll`. Each claim
adds 1 to `jobs.fence` and `jobs.claims`. Today a claim that `Run`'s
context interrupts is given back and does not count toward `MaxClaims`.
Jobs are rows keyed by `id` (bigserial) and also unique on (run ID,
StepID, kind), where kind is `step` or `timer`. `Result.Commands` already
carries every `DispatchStep` and `ScheduleTimer` that an event wrote to
the outbox, with its StepID and `NotBefore`. Nothing in the public API
exposes a job's identity, and no caller can run one job.

The worker-drain track (#50, #51, PRD 003) adds `Shutdown`, `MayClaim`
and `Active`. It defines one lifecycle for every claim loop: the loop
registers with the worker, claims only through a shared gate, and runs
handlers on a context that `Shutdown`'s deadline cuts off. This track
builds on that branch.

`documentation/hosts.md` says nothing about scale to zero.

**Comparable products.**

- Graphile Worker (Postgres, Node): `runOnce()` and the CLI `--once` flag
  "run until there are no runnable jobs left, and then resolve". The docs
  do not say what happens to jobs that become runnable during the call.
  Under steady inflow that call need not return.
- Oban (Postgres, Elixir): `drain_queue/2` runs available jobs and
  returns. Scheduled jobs run only with `with_scheduled`. Jobs that the
  drained jobs insert run only with `with_recursion`. So Oban makes "what
  counts as due" an explicit choice.
- Inngest: the service calls the app's HTTP endpoint once per step. Each
  step is its own request, and the service holds the state. Our design
  is the same shape, with Postgres in the role of the service and Cloud
  Tasks in the role of the caller.
- Cloud Tasks: an HTTP target task succeeds on a 2xx response and is
  retried with backoff otherwise. A handler can run up to 30 minutes. A
  task can carry a schedule time, so a timer can be pushed ahead of time.

**Platform limits that bound one request.** Cloud Scheduler's attempt
deadline for an HTTP target is 3 minutes by default and 30 minutes at
most. A Cloud Tasks HTTP task gets 10 minutes by default and 30 minutes
at most. A Cloud Run service's request timeout is 5 minutes by default and
60 minutes at most. A Cloud Run job's task can run for hours. Cloud Run
sends up to 80 concurrent requests to one instance by default.

**What developers do now.** They keep one warm instance with CPU always
allocated and run `Worker.Run` in it.

## Users and job

Primary user: a Go developer who runs the postgres host on Cloud Run, or
on another platform that scales to zero or bills by request. The job:
"make due work run, and make a running step survive the platform, without
a process that runs all the time." We optimize for this user.

Secondary user: a developer who runs `Worker.Run` today. Nothing changes
for them, and they can add a scheduled `RunOnce` as a backstop.

## Concepts

- **Due now** (for `RunOnce`): the jobs that were due when the call
  started, and the steps that those jobs dispatch with no delay. A
  `RunOnce` call runs these, then returns. A timer that comes due during
  the call, and work that other callers add during it, wait for the next
  call.
- **Job reference** (`JobRef`): the name of one job in the outbox: run ID,
  StepID, and whether it is a timer. It also carries the step kind (to
  route the job to a service that handles it) and the time the job is due.
  A host pushes job references to a task queue. `Execute` takes one.

The docs say "one `RunOnce` call", never "pass". In this repo a pass is
one cycle of a loop.

## Use cases

### UC-1: Cloud Scheduler drains due work every minute

```go
http.HandleFunc("POST /tasks/drain", func(rw http.ResponseWriter, r *http.Request) {
    // Stop before the scheduler's attempt deadline, raised to 10 minutes here.
    ctx, cancel := context.WithTimeout(r.Context(), 9*time.Minute)
    defer cancel()
    if err := worker.RunOnce(ctx); err != nil && ctx.Err() == nil {
        http.Error(rw, err.Error(), http.StatusInternalServerError) // a claim or settle error
        return
    }
    rw.WriteHeader(http.StatusNoContent) // drained, or stopped at the deadline
})
```

The 9-minute timeout assumes that the developer raised the Scheduler job's
attempt deadline from its 3-minute default, and the Cloud Run service's
request timeout from its 5-minute default, to 10 minutes. With the
defaults, the timeout must be under 3 minutes.

Cloud Scheduler calls the endpoint every minute. A `sleep: 24h` fires
within a minute of its time. A three-step run started by an API request
finishes in one call. A backlog that one call cannot finish before its
deadline is normal progress; the next call goes on. A new run waits up to
one interval before its first step runs; push mode (UC-3) is the
low-latency option.

Acceptance:
- [ ] A job due when `RunOnce` starts runs and settles in that call,
      unless another worker holds a live lease on it at the call's last
      claim. The next call takes that job.
- [ ] A step that a job of the call dispatches with no delay runs in the
      same call: a run of three sequential steps, started before the call,
      completes in one `RunOnce`.
- [ ] A timer that is not due when the call starts, or that the call
      writes, does not fire in that call. The next call after its time
      fires it. This assumes that the app clock does not lag Postgres by
      more than the sleep (tech spec, section 1).
- [ ] A run started by another caller during the call is not run by it.
- [ ] A run that loops forever over a `sleep` does not keep `RunOnce`
      from returning.
- [ ] At most `Concurrency` handlers run at once in one call. Two
      overlapping calls each have their own limit.
- [ ] `RunOnce` returns nil when no due job is left and every job it ran
      settled.
- [ ] If a job's settle fails, or the job is postponed because its
      definition cannot be loaded, `RunOnce` still runs the other due
      jobs. It returns the first such error after in-flight jobs settle.
      A claim error stops claiming and is returned the same way. A lease
      lost to another worker is not an error.
- [ ] If `ctx` ends first, `RunOnce` cancels its handlers, gives their
      claims back so the next call can take them at once, and returns
      `ctx.Err()`. Those claims count toward `MaxClaims`.
- [ ] A handler that always outlives the call's deadline fails as
      `abandoned` after `MaxClaims` calls.
- [ ] `RunOnce` opens no `LISTEN` connection.

### UC-2: A Cloud Run job drains and exits

```go
func main() {
    ctx := context.Background()
    // ... pool, store, worker; on SIGTERM call worker.Shutdown with ~8 s
    if err := worker.RunOnce(ctx); err != nil {
        log.Fatal(err)
    }
}
```

This is the shape for steps longer than a request deadline: a job's task
can run for hours.

Acceptance:
- [ ] The same as UC-1. A non-nil error makes the job's execution fail,
      so the platform's retry and alerting apply.
- [ ] `Shutdown` during `RunOnce` works as for `Run`: no new claims,
      handlers run until the deadline, cut-off claims are given back
      without counting, and `RunOnce` returns nil.

### UC-3: Cloud Tasks pushes each job as its own request

Every call that writes jobs returns their references. The caller pushes
them after its commit:

| Call | Jobs it writes | Where the references come from |
| --- | --- | --- |
| `Start`, `StartTx` | the first steps | `Result.Jobs()` |
| `Deliver`, `DeliverTx` | the next steps, after a hand-off or an approval | `Result.Jobs()` |
| `Cancel`, `CancelTx` | the `finally` steps | `Result.Jobs()` |
| `Execute` | the next steps and timers of the job's run | `Execution.Jobs` |
| `RunOnce` | the same, for each job it runs | none; it runs the steps itself and leaves timers for the next call |
| A `DeliverTx` inside `WithOnFinished` | the parent's next steps when a child run ends | none; see below |

A `WithOnFinished` hook runs inside the worker's settle transaction, so it
cannot push after that commit, and `Execution.Jobs` does not contain its
jobs. So a parent that waits on a child run resumes at the next scheduled
`RunOnce`, not at once. A scheduled `RunOnce` is also the safety net for a
push that was lost.

```go
res, err := store.Start(ctx, postgres.NewRun{ID: id, Definition: def, Input: in})
// after commit (StartTx: after your own commit):
for _, ref := range res.Jobs() {
    enqueue(ctx, ref) // a Cloud Tasks task, body = JSON of ref, schedule time = ref.RunAt
}

http.HandleFunc("POST /tasks/job", func(rw http.ResponseWriter, r *http.Request) {
    var ref postgres.JobRef
    if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
        http.Error(rw, err.Error(), http.StatusBadRequest)
        return
    }
    ex, err := worker.Execute(r.Context(), ref)
    if err != nil {
        http.Error(rw, err.Error(), http.StatusServiceUnavailable) // Cloud Tasks retries
        return
    }
    for _, next := range ex.Jobs {
        enqueue(r.Context(), next)
    }
    rw.WriteHeader(http.StatusOK)
})
```

Acceptance:
- [ ] `Execute` on a due job claims it with a new fence, runs the handler
      once with a heartbeat, settles it, and returns `Settled: true` and
      the jobs the result wrote.
- [ ] Two concurrent `Execute` calls with the same reference run the
      handler once. One settles; the other returns `ErrNotClaimable` or
      `Settled: false`.
- [ ] `Execute` on a job that is gone (settled, cancelled, run finished
      or deleted) returns `Settled: false`, a nil error, and does not call
      the handler.
- [ ] `Execute` on a job whose live lease another worker holds returns
      `ErrNotClaimable`. It does not claim the job, so `claims` and
      `fence` do not change.
- [ ] `Execute` on a job whose lease has expired claims it. The old
      holder's late result is fenced out.
- [ ] `Execute` on a job that is not due yet, or of a step kind this worker
      has no handler for, returns `ErrNotClaimable` and claims nothing.
- [ ] If `ctx` ends while the handler runs, `Execute` gives the claim back
      and returns an error that matches `ctx.Err()`. The claim counts
      toward `MaxClaims`. A handler that always outlives the request fails
      as `abandoned` after `MaxClaims` tasks.
- [ ] `MaxClaims`, `ErrDeferred`, unloadable definitions and abandoned
      steps behave as in `Run`.
- [ ] `Active()` counts an `Execute` whose job is claimed. `Shutdown`
      waits for it. On a worker that only calls `Execute`, `Shutdown` does
      not return while an `Execute` is in flight.
- [ ] After `Shutdown` is called, or while `MayClaim` returns false,
      `Execute` claims nothing and returns `ErrNotClaimable`. A handler
      that `Shutdown`'s deadline cuts off is given back without counting,
      and `Execute` returns an error.
- [ ] Concurrent `Execute` calls on one `Worker` pass the race detector.
- [ ] `Result.Jobs()` of `Start`, `Deliver` and `Cancel` lists one
      reference per `DispatchStep` and `ScheduleTimer` in `Commands`. A
      timer's `RunAt` is its fire time. A duplicate `Start` lists none.

## Requirements

- R-1: `Worker.RunOnce(ctx) error` runs the jobs that are due now, as
  defined in Concepts, with the same heartbeat, settle and fence rules as
  `Run`. `Concurrency` applies to each call.
- R-2: `Worker.Execute(ctx, ref JobRef) (Execution, error)` runs one job,
  as in UC-3. A nil error means "done or nothing to do": answer 2xx. A
  non-nil error means "retry later": answer 5xx. `Execution.Settled`
  reports whether this call settled the job. `Execution.Jobs` lists the
  jobs its result wrote.
- R-3: `ErrNotClaimable` is the error for a job that exists but that
  `Execute` must not claim now. Its message says why: another worker
  holds the lease, it is due at a later time, this worker has no handler
  for its kind, or the worker is not claiming.
- R-4: `JobRef` has the fields `RunID`, `StepID`, `Timer`, `StepKind` and
  `RunAt`, with JSON tags, so a task body round-trips. A zero `RunAt`
  means due at once.
- R-5: `Result.Jobs()` returns the job references of the result's
  commands.
- R-6: `Run`, `RunOnce` and `Execute` may run at the same time, on one
  `Worker` value or on many. They converge on the fence.
- R-7: `RunOnce` and `Execute` join the worker-drain lifecycle exactly as
  `Run` does: each registers with the worker, claims only through the
  shared gate, and runs handlers on the context that `Shutdown`'s deadline
  cuts off. So `Shutdown` waits for them and `Active()` counts their jobs.
  After `Shutdown`, `RunOnce` returns nil and `Execute` returns
  `ErrNotClaimable`.
- R-8: When the caller's context ends, `RunOnce` and `Execute` give the
  claim back and count it toward `MaxClaims`. Only a `Shutdown` cut-off
  gives a claim back without counting. `Run` keeps today's rule.
- R-9: `documentation/hosts.md` adds a "Scale to zero" part. It states the
  caveat and the three options: one warm instance with CPU always
  allocated running `Run`; a scheduled `RunOnce`; or Cloud Tasks with
  `Execute`, plus a scheduled `RunOnce` as the safety net. It also states:
  - the request limits above, next to the longest step each option can
    run, and that a step longer than the request deadline needs a warm
    worker running `Run`, a Cloud Run job (UC-2), or `ErrDeferred`;
  - every call that writes jobs (the UC-3 table), and the child-run delay;
  - that each timeout timer becomes a task that usually finds nothing to
    do and answers 200;
  - that a new run waits up to one interval in scheduled mode;
  - that Cloud Run's concurrency must be set to the number of handlers one
    instance can run, and the pool sized for that number plus the
    application's own queries, because `Execute` is not limited by
    `Concurrency`.
- R-10: README, llms.txt and the package docs list the new API.
- R-11: Example tests `ExampleWorker_RunOnce` and `ExampleWorker_Execute`,
  10 to 20 lines each, with output.

## Not in scope

- An after-commit dispatch hook in the store. See Decisions. The caller
  pushes the references it gets back.
- Returning the jobs that a `WithOnFinished` hook writes. See Decisions.
- A Cloud Tasks client or any Google SDK. The library stays free of
  platform code; `enqueue` in UC-3 is the host's.
- Task-name deduplication helpers (a `JobRef` encoded as a Cloud Tasks
  task name). A duplicate push is already safe; add it when a host asks.
- Changing how `Run` waits, wakes, claims or counts. Only the shared claim
  query gains a cutoff that `Run` does not use.
- The SQLite backend (#53). Its storage contract must carry claim-by-key
  and the cutoff; that track owns it.

## Decisions

- **"Due now" is the jobs due at the start of the call, plus the steps
  its own jobs dispatch with no delay. It does not chase timers or work
  from outside.** This always terminates. Jobs due at the start are
  finite. Jobs that other callers write during the call are due after its
  start (the tech spec covers transactions already open at the start). A
  chain of follow-up steps is finite, because every cycle in a definition
  has a `max_visits` or a `sleep`, `sleep` must be positive, and
  `RunOnce` does not follow timers. It also keeps a sequential run from
  taking one call per step. Rejected: claim until nothing is due
  (Graphile's shape), which need not return under steady inflow or with a
  short `sleep` loop. Rejected: only jobs due at the start (Oban's
  default), which makes a five-step run take five scheduler ticks.
- **No store dispatch hook. The host gets job references from
  `Result.Jobs()` and `Execution.Jobs`, and pushes them after its own
  commit. A scheduled `RunOnce` is the safety net for a lost push.**
  `StartTx`, `DeliverTx` and `CancelTx` run in the caller's transaction,
  and pgx gives the store no commit callback. A hook there would fire
  before the commit, or on a rollback. The caller knows when it commits,
  so it pushes. `Execute` commits its own settle, so it returns the
  follow-up jobs. Rejected: an after-commit hook (cannot be correct for
  the `Tx` methods). Rejected: a relay loop that pushes undispatched jobs,
  which needs a `dispatched_at` column (a migration) and a process that
  runs all the time, the thing this track removes.
- **The jobs that a `WithOnFinished` hook writes are not returned. A
  child-run hop waits for the next scheduled `RunOnce`.** The hook calls
  `DeliverTx` on the worker's transaction, and the store cannot tell
  those jobs from the settle's own without a query by transaction ID,
  which scans the jobs table. Rejected: that query; also rejected: making
  the store track jobs per transaction, which needs state keyed by
  `pgx.Tx` in a store that is otherwise stateless. Revisit if a host's
  child-run latency matters.
- **A job reference is the natural key (run ID, StepID, timer), not the
  `jobs.id` bigserial.** The host can build it from `Result.Commands`
  with no change to how the store writes jobs. It names one attempt or
  one timer for the life of the run, and reads well in logs. Rejected:
  `jobs.id`, which needs `INSERT ... RETURNING` plumbing through the
  persist path that four other tracks change, and means nothing to a
  person.
- **When the caller's context ends, the claim counts toward
  `MaxClaims`.** For `RunOnce` and `Execute` the context is a request
  with a deadline, and the platform retries. A step longer than the
  deadline would be cut off and retried without end if the give-back did
  not count. Counting fails it as `abandoned` after `MaxClaims`, so the
  developer learns the step needs a longer-lived host. The claim is still
  given back (its lease cleared), so the next attempt does not wait a
  lease. Only the worker's own `Shutdown` refunds the claim, because a
  deploy is not the step's fault. `Run` keeps its rule: its context is the
  process lifetime, and worker-drain keeps that stop unchanged. Rejected:
  refund on every cancel, which loops forever on a long step.
- **`RunOnce` returns the first error of a job it ran, after it runs the
  other due jobs.** A settle that did not commit, or a job postponed for
  an unloadable definition, then reaches the scheduler's or the Cloud Run
  job's failure count and alerting, as `Execute`'s error reaches Cloud
  Tasks. The job itself is safe: it waits for its lease or its new time.
  Rejected: log and return nil, which hides a database fault from the
  platform. Rejected: stop at the first job error, which lets one bad
  definition hold back every other run.
- **`Execute` reports through its error, not a three-value enum.** The
  handler makes one decision: 2xx or 5xx. A nil error is 2xx, and
  `Settled` tells "done" from "nothing to do" for logs. Rejected: an
  `Outcome` enum next to an error, which gives the handler two channels
  that can disagree.
- **One sentinel, `ErrNotClaimable`, for busy, not due, no handler and not
  claiming.** The handler answers 5xx to all of them. The message says
  which. Rejected: one sentinel per reason, which adds names for no
  different action.
- **`Execute` does not count toward `Concurrency`.** The platform's
  request concurrency bounds it, and hosts.md says how to set it.
  Rejected: a shared semaphore, which would make a request wait inside the
  handler while the task queue already limits the rate.
- **hosts.md and the examples name Cloud Run, Cloud Scheduler and Cloud
  Tasks.** The AGENTS.md rule "docs and examples never name a product"
  bars the names of the applications and companies that use this
  library, not the platforms it runs on. `hosts.md` already names Cloud
  Run. The scale-to-zero advice needs the platforms' real limits and
  settings, so it names them. Code in the library stays free of platform
  code (Not in scope).
- **`RunOnce` and `Execute` join the worker-drain lifecycle (R-7).**
  Otherwise `Shutdown` returns while their handlers run, `Active()` reads
  0, the application closes the pool, and the claims count: the failure
  #50 fixes. `RunOnce` returns nil after `Shutdown`, like `Run`.
  `Execute` returns `ErrNotClaimable` so that Cloud Tasks retries the task
  on another instance.

## Success

A developer runs the postgres host on Cloud Run with zero minimum
instances. A sleep timer fires within one scheduler interval of its time.
Every step of a sequential run finishes in one `RunOnce` call. With Cloud
Tasks, a duplicate task never runs a step twice while a lease is live.
What must not get worse: `Run`'s behavior and its claim query plan (it
still scans `jobs_claim` and stops at its limit).

## Risks and open questions

- The implementation stacks on the worker-drain branch. If its lifecycle
  changes shape before it merges, R-7 follows it. Not a blocker.
- A step that runs longer than the Cloud Tasks handler limit (30 minutes)
  is cut off when the request ends, and the claim counts. hosts.md tells
  the developer to run such steps in a warm worker or a Cloud Run job, or
  to hand them off with `ErrDeferred`.
- A scheduled `RunOnce` may claim a job before its Cloud Task arrives.
  That is correct: the task then finds the job gone and answers 200. The
  step still runs inside a request, the scheduler's.
