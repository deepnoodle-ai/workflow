# Run due work on demand: RunOnce and Execute

Status: Draft
PRD PR: #<n>
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
adds 1 to `jobs.fence` and `jobs.claims`. A graceful release gives the
claim back and does not count toward `MaxClaims`. Jobs are rows keyed by
`id` (bigserial) and also unique on (run ID, StepID, kind), where kind is
`step` or `timer`. `Result.Commands` already carries every `DispatchStep`
and `ScheduleTimer` that an event wrote to the outbox, with its StepID and
`NotBefore`. Nothing in the public API exposes a job's identity, and no
caller can run one job.

`documentation/hosts.md` says nothing about scale to zero. Its only
Cloud Run advice is about `Lease` and the shutdown grace period, which
the worker-drain track (#50, #51) rewrites.

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

- **Pass**: one call to `RunOnce`. It runs the jobs that were due when it
  started, and the steps that those jobs dispatch at once. Then it
  returns. Work that arrives from outside during a pass, and timers that
  come due during it, wait for the next pass.
- **Job reference** (`JobRef`): the name of one job in the outbox: run ID,
  StepID, and whether it is a timer. It also carries the step kind (to
  route the job to a service that handles it) and the time the job is due.
  A host pushes job references to a task queue. `Execute` takes one.

## Use cases

### UC-1: Cloud Scheduler drains due work every minute

```go
http.HandleFunc("POST /tasks/drain", func(rw http.ResponseWriter, r *http.Request) {
    if err := worker.RunOnce(r.Context()); err != nil {
        http.Error(rw, err.Error(), http.StatusInternalServerError)
        return
    }
    rw.WriteHeader(http.StatusNoContent)
})
```

Cloud Scheduler calls the endpoint every minute. A `sleep: 24h` fires
within a minute of its time. A three-step run started by an API request
finishes in one pass.

Acceptance:
- [ ] A job due when `RunOnce` starts runs and settles in that pass.
- [ ] A step that a job of the pass dispatches with no delay runs in the
      same pass: a run of three sequential steps, started before the pass,
      completes in one `RunOnce` call.
- [ ] A timer that is not due when the pass starts, or that the pass
      writes, does not fire in that pass. The next pass after its time
      fires it.
- [ ] A run started by another caller during the pass is not run by it.
- [ ] A run that loops forever over a `sleep` does not keep `RunOnce`
      from returning.
- [ ] At most `Concurrency` handlers run at once.
- [ ] `RunOnce` returns nil when no job of the pass is left. It returns
      `ctx.Err()` if `ctx` ends first; in-flight claims are then given back
      as in `Run`. It returns a claim error after in-flight jobs settle.
- [ ] `RunOnce` opens no `LISTEN` connection.

### UC-2: A Cloud Run job drains and exits

```go
func main() {
    // ... pool, store, worker
    if err := worker.RunOnce(ctx); err != nil {
        log.Fatal(err)
    }
}
```

Acceptance:
- [ ] The same as UC-1. A non-nil error makes the job's execution fail,
      so the platform's retry and alerting apply.

### UC-3: Cloud Tasks pushes each job as its own request

The service that starts runs pushes the jobs that the start wrote, after
its commit. The task handler runs one job and pushes the jobs that its
result wrote. A scheduled `RunOnce` (UC-1) is the safety net for a push
that was lost.

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
      (not counted toward `MaxClaims`) and returns an error that matches
      `ctx.Err()`.
- [ ] `MaxClaims`, `ErrDeferred`, unloadable definitions and abandoned
      steps behave as in `Run`.
- [ ] Concurrent `Execute` calls on one `Worker` pass the race detector.
- [ ] `Result.Jobs()` lists one reference per `DispatchStep` and
      `ScheduleTimer` in `Commands`. A timer's `RunAt` is its fire time.
      A duplicate `Start` lists none.

## Requirements

- R-1: `Worker.RunOnce(ctx) error` runs one pass, as defined in Concepts,
  with the same heartbeat, settle, fence, release and `MaxClaims` rules
  as `Run`.
- R-2: `Worker.Execute(ctx, ref JobRef) (Execution, error)` runs one job,
  as in UC-3. A nil error means "done or nothing to do": answer 2xx. A
  non-nil error means "retry later": answer 5xx. `Execution.Settled`
  reports whether this call settled the job. `Execution.Jobs` lists the
  jobs its result wrote.
- R-3: `ErrNotClaimable` is the error for a job that exists but that
  `Execute` must not claim now. Its message says why: another worker
  holds the lease, it is due at a later time, or this worker has no
  handler for its kind.
- R-4: `JobRef` has the fields `RunID`, `StepID`, `Timer`, `StepKind` and
  `RunAt`, with JSON tags, so a task body round-trips. A zero `RunAt`
  means due at once.
- R-5: `Result.Jobs()` returns the job references of the result's
  commands.
- R-6: `Run`, `RunOnce` and `Execute` may run at the same time, on one
  `Worker` value or on many. They converge on the fence.
- R-7: When the worker-drain track's "stop claiming" gate is closed
  (`Shutdown` called, or `MayClaim` returns false), `RunOnce` claims
  nothing more, lets its in-flight jobs settle and returns nil, and
  `Execute` returns `ErrNotClaimable`.
- R-8: `documentation/hosts.md` states the scale-to-zero caveat and the
  options: one warm instance with CPU always allocated, a scheduled
  `RunOnce`, or Cloud Tasks with `Execute` plus a scheduled `RunOnce` as
  the safety net. README, llms.txt and the package docs list the new API.
- R-9: Example tests `ExampleWorker_RunOnce` and `ExampleWorker_Execute`,
  10 to 20 lines each, with output.

## Not in scope

- An after-commit dispatch hook in the store. See Decisions. The caller
  pushes the references it gets back.
- A Cloud Tasks client or any Google SDK. The library stays free of
  platform code; `enqueue` in UC-3 is the host's.
- Task-name deduplication helpers (a `JobRef` encoded as a Cloud Tasks
  task name). A duplicate push is already safe; add it when a host asks.
- Changing how `Run` waits, wakes or claims. Only the shared claim query
  gains a cutoff that `Run` does not use.
- The SQLite backend (#53). Its storage contract must carry claim-by-key
  and the cutoff; that track owns it.

## Decisions

- **A pass runs the jobs due at its start, plus the steps its own jobs
  dispatch with no delay. It does not chase timers or work from outside.**
  This always terminates. Jobs due at the start are finite. Jobs that
  other callers write during the pass are due after its start (the tech
  spec covers transactions already open at the start). A chain of follow-up
  steps is finite, because every cycle in a definition has a
  `max_visits` or a `sleep`, `sleep` must be positive, and a pass does not
  follow timers. It also keeps a sequential run from taking one pass per
  step. Rejected: claim until nothing is due (Graphile's shape), which need
  not return under steady inflow or with a short `sleep` loop. Rejected:
  only jobs due at the start (Oban's default), which makes a five-step
  run take five scheduler ticks.
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
- **A job reference is the natural key (run ID, StepID, timer), not the
  `jobs.id` bigserial.** The host can build it from `Result.Commands`
  with no change to how the store writes jobs. It names one attempt or
  one timer for the life of the run, and reads well in logs. Rejected:
  `jobs.id`, which needs `INSERT ... RETURNING` plumbing through the
  persist path that four other tracks change, and means nothing to a
  person.
- **`Execute` reports through its error, not a three-value enum.** The
  handler makes one decision: 2xx or 5xx. A nil error is 2xx, and
  `Settled` tells "done" from "nothing to do" for logs. Rejected: an
  `Outcome` enum next to an error, which gives the handler two channels
  that can disagree.
- **One sentinel, `ErrNotClaimable`, for busy, not due and no handler.**
  The handler answers 5xx to all three. The message says which. Rejected:
  three sentinels, which add names for no different action.
- **`Execute` does not count toward `Concurrency`.** The platform's
  request concurrency bounds it. Rejected: a shared semaphore, which would
  make a request wait inside the handler while the task queue already
  limits the rate.
- **`RunOnce` and `Execute` honor the worker-drain gate (R-7).** A
  superseded revision must not run new work through either path.
  `RunOnce` returns nil because the next pass, on the new revision, takes
  the work. `Execute` returns `ErrNotClaimable` so that Cloud Tasks
  retries the task, which then reaches the new revision.

## Success

A developer runs the postgres host on Cloud Run with zero minimum
instances. A sleep timer fires within one scheduler interval of its time.
Every step of a sequential run finishes in one pass. With Cloud Tasks, a
duplicate task never runs a step twice while a lease is live. What must
not get worse: `Run`'s behavior and its claim query plan (it still scans
`jobs_claim` and stops at its limit).

## Risks and open questions

- The implementation stacks on the worker-drain branch, which changes the
  claim loop. If its "stop claiming" gate has a different shape, R-7
  adapts to it. Not a blocker.
- A step that runs longer than the Cloud Tasks handler limit (30 minutes)
  gets a duplicate task while it runs. The duplicate sees a live lease and
  retries until the first settles. hosts.md tells the developer to hand
  long work off with `ErrDeferred`.
- A scheduled `RunOnce` may claim a job before its Cloud Task arrives.
  That is correct: the task then finds the job gone and answers 200. The
  step still runs inside a request, the scheduler's.
