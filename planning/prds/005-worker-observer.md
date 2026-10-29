# Count and time what the postgres worker does, and see how far behind it is

Status: Draft
PRD PR: TBD  Implementation PR: TBD  Updated: 2026-10-28
Issues: #54

## Problem

A developer who runs `postgres.Worker` in production must put it on a
dashboard and alert on it. They need to know how many jobs the worker
claims, how each one ends, how long steps take, how often the worker loses
a lease or gives a claim back, how often a step is abandoned, whether
claims fail, and how far behind the outbox is.

Today the worker reports only through `Logger`. To get a count, the
developer must parse log lines, and the log has no durations. Nothing
reports queue lag at all. The developer must read the `jobs` table with
their own SQL, which depends on columns the library does not document as
API (`queue`, `lease_expires`).

If we do nothing, each application writes its own log parser and its own
lag query. A schema change then breaks those queries without warning.

## Context

### What the code does today

- `Worker.Run` claims due jobs in a loop (`claim`), and runs each one on
  its own goroutine (`process`). Every claim takes a new fencing token.
- `process` ends a job in one of these ways:
  - The result is recorded and the job is deleted (`settle`, or `finish`
    for a handler that returns `workflow.ErrDeferred`). A timer fires the
    same way.
  - The heartbeat finds the job gone or claimed again, or no heartbeat
    succeeds for a whole lease. The handler's context is cancelled and its
    result is dropped. A settle that finds the fence moved, or that cannot
    commit before the lease runs out, also drops the result.
  - On shutdown the claim is given back (`release`) and the claim count
    is refunded.
  - After `MaxClaims` claims the step fails as `abandoned`, or the run is
    aborted (a timer that cannot fire, a step that cannot even be failed).
  - The run's definition cannot be loaded. The job is postponed with
    backoff, and after `MaxClaims` claims the run fails as
    `definition_unavailable`.
- A failed claim query is logged at Error level and the loop polls again.
- Each of these outcomes writes one log line. None of them is counted.
- Every job has a generated `queue` column: `timer` for every timer, and
  `step:<kind>` for the steps of one kind. The `jobs_claim` index is
  `(queue, run_at, id)`. A job is due when `run_at <= now()` and it has no
  live lease.
- No `Example*` test exists yet in either module. CI runs the postgres
  module against Postgres 18 with `WORKFLOW_PG_DSN` set.

Issue #54 asks for an optional observer with no metrics dependency, and
for a store method that reports queue lag.

### How comparable libraries do the job

- **Go `net/http/httptrace.ClientTrace`**: a struct of optional func
  fields, called synchronously. A caller sets only the hooks it needs.
  This is the shape the brief chooses.
- **River (Go, Postgres job queue)**: `Client.Subscribe` returns a
  buffered channel of job events with `JobStatistics` (queue wait, run
  duration, complete duration). The channel is asynchronous, so a slow
  subscriber can miss events. Metrics adapters live in a separate module.
- **Asynq (Go, Redis job queue)**: `Inspector.GetQueueInfo` returns
  `QueueInfo` with `Latency`, "the age of the oldest pending task in the
  queue". Its Prometheus collector is a separate package that polls the
  inspector.
- **Sidekiq**: `Sidekiq::Queue#latency` is the age of the oldest enqueued
  job. Dashboards alert on it.
- **Oban (Elixir)**: emits `:telemetry` events such as job start, stop
  and exception, with a duration and the job's state. Handlers run
  synchronously in the job's process.
- **Temporal Go SDK**: takes a `MetricsHandler` interface. The SDK names
  its metrics, and adapters map them to a metrics library.

Two patterns are common. Job events are hooks or a stream, and the metrics
library is the application's choice. Queue lag is the age of the oldest
job that is ready to run, read on demand from the store. We follow both.
We do not follow Temporal: if the library names the metrics, the
application cannot choose names, labels or buckets.

### Queue stats cost

A probe on Postgres 18 used the `jobs` table and `jobs_claim` index from
`schema.sql`. It held 100,000 future timers, 50,000 due `agent` steps (10%
under a live lease), 2,000 due `action` steps and 10 future `mail` steps.
A per-queue query used a loose index scan to list the queues, then one
range scan of `jobs_claim` per queue. It returned correct counts and ages
in 12 ms and read 1,382 buffers. It never read the future timers. A plain
`GROUP BY queue` read the whole table (2,226 buffers). So the existing
index is enough, and no migration is needed.

## Users and job

Primary user: a Go developer who runs `postgres.Worker` in production. They
use a metrics library of their own choice. They want counters, histograms
and gauges for the worker, and alerts on lag and on lost work. They do not
want this library to pull in a metrics dependency.

## Concepts

- **Observer** (`postgres.Observer`): a set of optional functions that the
  worker calls when it claims or ends a job. Set the functions you need on
  `Worker.Observer`. Each function runs on the worker's goroutine, so it
  must be fast and safe for concurrent use.
- **Outcome** (`postgres.Outcome`): how a recorded job ended:
  `completed`, `failed`, `deferred` (the handler handed the work off), or
  `fired` (a timer).
- **Queue**: the jobs that workers claim together. `timer` holds every
  timer. `step:<kind>` holds the steps of one kind. This is the value of
  the `jobs.queue` column.
- **Due job**: a job whose time has come (`run_at` is now or earlier) and
  that no worker holds under a live lease. A job whose lease has expired
  is due again.

## Use cases

### UC-1: Count jobs by outcome

The developer sets one field and increments a counter vector.

```go
w := &postgres.Worker{
	Store:    store,
	Handlers: handlers,
	Observer: postgres.Observer{
		Settled: func(job workflow.Job, s postgres.Settlement) {
			settles.Inc(job.StepKind, string(s.Outcome))
		},
		LeaseLost: func(job workflow.Job) { lost.Inc(job.StepKind) },
		GivenBack: func(job workflow.Job) { givenBack.Inc(job.StepKind) },
		Abandoned: func(job workflow.Job) { abandoned.Inc(job.StepKind) },
		ClaimError: func(error) { claimErrors.Inc() },
	},
}
```

Acceptance:
- [ ] A step whose handler returns an output fires `Claimed` once, then
      `Settled` once with `OutcomeCompleted`.
- [ ] A step whose handler returns an error fires `Settled` once with
      `OutcomeFailed` and `Settlement.Error` set to the recorded
      `*workflow.StepError`.
- [ ] A handler that returns `workflow.ErrDeferred` fires `Settled` once
      with `OutcomeDeferred`.
- [ ] A timer fires `Claimed` once, then `Settled` once with
      `OutcomeFired`. Its `job.Kind` is `workflow.CommandScheduleTimer`.
- [ ] A step whose job is reclaimed by another worker, or cancelled
      while the handler runs, fires `LeaseLost` once and no `Settled`.
- [ ] A step that is interrupted by shutdown fires `GivenBack` once and
      no `Settled`.
- [ ] A step claimed more than `MaxClaims` times fires `Abandoned` once
      and no `Settled`. So does a timer that fails its run this way.
- [ ] A failed claim query fires `ClaimError` once with the error. A
      claim cut off by shutdown does not.
- [ ] Each test above runs with `-race`.

### UC-2: Record step duration in a histogram

```go
Settled: func(job workflow.Job, s postgres.Settlement) {
	if job.Kind == workflow.CommandDispatchStep {
		stepSeconds.Observe(job.StepKind, s.Duration.Seconds())
	}
},
```

Acceptance:
- [ ] `Settlement.Duration` is the time from the handler call to its
      return. A handler that sleeps 50 ms reports at least 50 ms.
- [ ] `Settlement.Duration` is zero for a timer.

### UC-3: Poll queue lag into a gauge

A goroutine polls every 15 seconds and sets two gauges per queue.

```go
for range time.Tick(15 * time.Second) {
	stats, err := store.QueueStats(ctx)
	if err != nil {
		continue
	}
	dueJobs.Reset()
	oldestDue.Reset()
	for _, q := range stats {
		dueJobs.Set(q.Queue, float64(q.Due))
		oldestDue.Set(q.Queue, q.OldestDue.Seconds())
	}
}
```

Acceptance:
- [ ] `QueueStats` returns one `QueueStat` for each queue that has at
      least one job in the outbox, sorted by `Queue`.
- [ ] `Due` counts due jobs only. A future job and a job under a live
      lease are not due. A job whose lease has expired is due.
- [ ] `OldestDue` is how long the oldest due job has been due, measured
      with the database clock. It is zero when `Due` is zero.
- [ ] A queue with only future or claimed jobs appears with `Due` 0 and
      `OldestDue` 0. A queue with no jobs does not appear.
- [ ] An empty outbox returns an empty slice and a nil error.
- [ ] A test covers due, future, claimed and lease-expired jobs across
      two step kinds and timers.

## Requirements

- R-1: `Worker` has a field `Observer Observer`. Its zero value calls
  nothing. A worker without an observer behaves as it does today.
- R-2: `Observer` has these optional fields:
  - `Claimed func(job workflow.Job)`: the worker claimed the job. It fires
    before the worker loads the run's definition or calls the handler.
  - `Settled func(job workflow.Job, s Settlement)`: the worker recorded
    the job's result and removed the job from the outbox. It fires after
    the transaction commits.
  - `LeaseLost func(job workflow.Job)`: the worker stopped work on the job
    and recorded nothing, because it no longer held the claim. The job was
    cancelled or reclaimed, the run is gone, no heartbeat succeeded for a
    whole lease, or the result could not be recorded before the lease ran
    out. The attempt can run again.
  - `GivenBack func(job workflow.Job)`: on shutdown, the worker gave the
    claim back without counting it toward `MaxClaims`.
  - `Abandoned func(job workflow.Job)`: the job was claimed more than
    `MaxClaims` times. The worker recorded the step as failed with type
    `abandoned`, or failed the run.
  - `ClaimError func(err error)`: a claim query failed. It does not fire
    when the error comes from shutdown.
- R-3: `Settlement` has `Outcome Outcome`, `Duration time.Duration`, and
  `Error *workflow.StepError` (set only for `OutcomeFailed`). `Outcome`
  is a string type with the constants `OutcomeCompleted`, `OutcomeFailed`,
  `OutcomeDeferred` and `OutcomeFired`.
- R-4: Each claimed job fires `Claimed` once. Then it fires at most one of
  `Settled`, `LeaseLost`, `GivenBack` and `Abandoned`. It fires none of
  them only when the worker logs an error and leaves the job for a later
  claim: the run's definition cannot be loaded, or failing the run or
  giving the claim back hits a database error. A give-back that finds the
  fence moved fires `LeaseLost`.
- R-5: The worker builds no value and calls nothing for a field that is
  nil. The callbacks take `workflow.Job` and `Settlement` by value, so a
  set field adds no allocation either.
- R-6: Callbacks run synchronously on the worker's goroutines, and
  concurrently with each other. The doc comment on `Observer` says so,
  and says that a slow `Claimed` delays the handler and a slow `Settled`
  holds one of the worker's `Concurrency` slots. The worker does not
  recover a panic in a callback.
- R-7: `Store.QueueStats(ctx) ([]QueueStat, error)` returns, per queue,
  `QueueStat{Queue string, Due int, OldestDue time.Duration}` as UC-3
  defines. It uses the `jobs_claim` index and reads no job that is not
  yet due. It adds no migration.
- R-8: No file in either module imports a metrics or tracing library.
- R-9: An `Example*` test in the postgres module wires an `Observer` to
  plain counters, runs a short workflow, and prints the counts and one
  `QueueStats` result. It has an `// Output:` block. Without
  `WORKFLOW_PG_DSN` it does not run, so `go test` still passes without
  Postgres.
- R-10: `documentation/hosts.md` (Workers) has a "Metrics" paragraph:
  the events, the at-most-one rule, the callback rules, and
  `QueueStats`. `llms.txt` lists `Observer`, `Settlement`, `Outcome`,
  `QueueStats` and `QueueStat`. README and `doc.go` change only if they
  list the worker's fields (today they do not).

## Not in scope

- Metrics adapters for a specific library. Applications choose their own,
  and the library stays free of dependencies.
- A `context.Context` or trace span in the callbacks. Metrics need
  neither, and no caller has asked for tracing.
- Per-job queue wait at claim time. `QueueStats` covers lag, and adding
  `run_at` to the claim query conflicts with other tracks that change it.
- Events for transient heartbeat or settle errors. They are retried and
  logged. A lasting failure ends as `LeaseLost`.
- Events for a run whose definition cannot be loaded. This is a
  configuration error that the worker logs at Error level.
- A count of claimed jobs in `QueueStats`. Issue #50 proposes
  `Worker.Active` for in-flight work.
- An observer for the `local` host, or for `Store.Start` and `Deliver`.
  Issue #54 is about the postgres worker.

## Decisions

- **A struct of optional func fields.** The user sets only the events they
  need, as with `httptrace.ClientTrace`. Rejected: an interface with every
  method, which forces boilerplate for a user who wants one event.
- **`Worker.Observer` is a value, not a pointer.** The zero value means
  "no hooks", and the worker needs no nil check on the struct. Rejected:
  `*Observer`, which adds a nil case and gains nothing, because the func
  fields are already references.
- **Callbacks receive `workflow.Job` by value.** Handlers already use this
  type. It has `Kind`, `StepKind`, `RunID`, `StepID` and `Claims` for
  labels. Copying it does not allocate. Rejected: a new info type (one more
  type to learn), and a queue string (building `"step:"+kind` allocates on
  every call).
- **At most one ending event per claim (R-4).** Then `Claimed` equals the
  sum of the ending events, except in the logged error cases, and tests
  can check each path. Rejected: fire `Settled` for every end with an
  outcome such as `lost`, because the issue and the brief name those as
  separate events.
- **`Settled` fires after the commit.** It counts results that are in the
  database. A result that the worker drops is `LeaseLost`. Rejected: fire
  when the handler returns, which would count results that never land.
- **`Abandoned` replaces `Settled` for a step past `MaxClaims`.** An
  abandoned step is a crash loop, not a normal failure, and users alert on
  it on its own. Rejected: `Settled` with `OutcomeFailed` plus
  `Abandoned`, which counts one job twice.
- **`Duration` is the handler's run time.** That is the step duration
  users chart. Rejected: claim to commit, which mixes in database latency
  and settle retries.
- **Timers use the same events.** The worker claims and settles a timer
  through the same path. Users tell them apart with
  `job.Kind == workflow.CommandScheduleTimer`. Rejected: separate timer
  events, which double the API for no new information.
- **No panic recovery in callbacks.** A bug in metrics code must be loud,
  as in `httptrace`. Rejected: recover and log, which hides broken
  metrics.
- **`QueueStats` needs no new index.** The probe above shows that a
  per-queue range scan of `jobs_claim` reads only due rows. Rejected: an
  index that includes `lease_expires`. Heartbeats update that column every
  `Lease/3` for every running job, so an index on it would turn those
  heap-only updates into index updates.
- **`QueueStats` lists every queue that has a job, due or not.** A gauge
  for a queue that drains then goes to 0. It does not stay at its last
  value. Rejected: list only queues with due jobs, which leaves a gauge
  stuck when the user does not reset it.
- **The database clock measures `OldestDue`.** `run_at` and the lease use
  the database's `now()`, so worker clock skew cannot make the age wrong.
  Rejected: compute the age in Go.
- **`Queue` uses the `jobs.queue` value** (`timer`, `step:<kind>`). It is
  already in the schema, and it names what a worker claims. Rejected:
  separate `Kind` and `StepKind` fields, which add a field to every stat
  for no new information.
- **The `Example*` test needs Postgres and does not run without it.** The
  example must show real events. Faked output proves nothing. Rejected: an
  example that calls the callbacks by hand.

## Success

- A developer wires UC-1 to UC-3 in about 30 lines, with the metrics
  library of their choice, and no new dependency in `go.mod`.
- An alert on `OldestDue` catches a stalled worker, and a step kind that
  no worker handles (the outbox holds such jobs forever).
- Must not get worse: claim and settle throughput, allocations with no
  observer, and the existing postgres tests under `-race`.

## Risks and open questions

- A slow callback holds a worker goroutine. `Claimed` runs before the
  first heartbeat, so a callback that blocks for a whole lease loses the
  claim. Mitigation: the doc comment, and `Settled` fires after the
  commit, so it cannot cost a result. Not blocking.
- Other tracks change the claim loop at the same time (#50 Shutdown and
  #51 MayClaim, #55 RunOnce and #56 Execute). Mitigation: the hooks go in
  `process`, `settle`, `finishDeferred`, `abort`, `release` and the claim
  error branch, not in the loop's control flow. A new path that calls
  `process` or `release` fires the events with no extra work. Not
  blocking.
- The ending rule (R-4) has error paths that fire nothing. If a reviewer
  finds another path, the implementation maps it or the PRD lists it.
  Not blocking.
