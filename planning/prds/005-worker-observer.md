# Count and time what the postgres worker does, and see how far behind it is

Status: Approved
PRD PR: #59  Implementation PR: #64  Updated: 2026-10-28
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
  - A job that `MaxClaims` claims left unfinished ends on its next claim,
    claim `MaxClaims+1`. A step fails as `abandoned`. A timer that cannot
    fire aborts its run as `abandoned`. A step whose `abandoned` failure
    could not be applied aborts its run on claim `MaxClaims+2`.
  - The run's definition cannot be loaded. The job is postponed with
    backoff, and claim `MaxClaims` fails the run as
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
`schema.sql`. A per-queue query used a loose index scan to list the
queues, then one scan per queue for the due rows.

- 100,000 future timers, 50,000 due `agent` steps (10% under a live
  lease), 2,000 due `action` steps and 10 future `mail` steps: correct
  counts and ages in 12 ms, 1,382 buffers. It used `jobs_claim` for each
  queue and never read the future timers. A plain `GROUP BY queue` read
  the whole table (2,226 buffers).
- The same with 1,000,000 due `agent` steps: 340 to 385 ms on the local
  test server, 97,679 buffers. For a queue that is most of the table, the
  planner reads the table once, which is the cheapest plan.

The cost grows with the number of due jobs, because the count must check
the lease of each one. One poll every 15 s from one process is cheap even
with a large backlog. The existing index is enough, and no migration is
needed.

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
- **Ending event**: the one event that says how a claim ended: `Settled`,
  `Cancelled`, `LeaseLost`, `GivenBack` or `Abandoned`.
- **Outcome** (`postgres.Outcome`): how a recorded job ended:
  `completed`, `failed`, `deferred` (the handler handed the work off), or
  `fired` (a timer).
- **Queue**: the jobs that workers claim together: all timers, or the
  steps of one kind. `QueueStat` names a queue with `Kind` and `StepKind`,
  the same fields that `workflow.Job` has. The timer queue has an empty
  `StepKind`.
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
		Cancelled: func(job workflow.Job) { cancelled.Inc(job.StepKind) },
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
      with `OutcomeDeferred`. So does one whose result arrives through
      `Store.Deliver` before the worker marks the job done.
- [ ] A timer fires `Claimed` once, then `Settled` once with
      `OutcomeFired`. Its `job.Kind` is `workflow.CommandScheduleTimer`.
- [ ] Output that Postgres cannot store fires `Settled` once with
      `OutcomeFailed` and an error of type `invalid_output`.
- [ ] A step whose run is cancelled while the handler runs fires
      `Cancelled` once, and not `LeaseLost`. So does a step that times
      out.
- [ ] A result that the run rejects fires `Cancelled`, and not `Settled`.
- [ ] A step whose job another worker reclaims fires `LeaseLost` once. So
      does a step whose heartbeats fail for a whole lease, and a result
      that cannot be recorded before the lease runs out.
- [ ] Two workers: worker A's lease runs out while its handler runs (A
      is paused past the lease), worker B claims the job and settles it,
      and then A's next heartbeat finds no job. A fires `LeaseLost` once,
      and not `Cancelled`.
- [ ] A step that is interrupted by shutdown fires `GivenBack` once and
      no `Settled`. So does a timer that cannot fire during shutdown.
- [ ] With `MaxClaims` 2, a step that two earlier claims left unfinished
      fires `Claimed`, then `Abandoned` once on claim 3, and no `Settled`.
- [ ] With `MaxClaims` 2, a timer whose settle always fails fires
      `LeaseLost` on claims 1 and 2, and `Abandoned` once on claim 3 when
      it fails the run.
- [ ] With `MaxClaims` 3, a run whose definition cannot be loaded fires
      `Claimed` on claims 1, 2 and 3, no ending event on claims 1 and 2,
      and `Abandoned` once on claim 3 when it fails as
      `definition_unavailable`.
- [ ] A claim that is postponed because the run's definition cannot be
      loaded fires `Claimed` and no ending event.
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

One goroutine in one process polls every 15 seconds and sets two gauges
per queue. It uses the same labels as UC-1.

```go
for range time.Tick(15 * time.Second) {
	stats, err := store.QueueStats(ctx)
	if err != nil {
		continue
	}
	// A queue with no jobs is not in stats, so drop the old series. A
	// scrape between Reset and Set reads no series for that one poll.
	dueJobs.Reset()
	oldestDue.Reset()
	for _, q := range stats {
		dueJobs.Set(string(q.Kind), q.StepKind, float64(q.Due))
		oldestDue.Set(string(q.Kind), q.StepKind, q.OldestDue.Seconds())
	}
}
```

Acceptance:
- [ ] `QueueStats` returns one `QueueStat` for each queue that has at
      least one job in the outbox, sorted by `Kind`, then `StepKind`.
- [ ] A step queue has `Kind` `workflow.CommandDispatchStep` and the
      step kind in `StepKind`. The timer queue has `Kind`
      `workflow.CommandScheduleTimer` and an empty `StepKind`.
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
  - `Cancelled func(job workflow.Job)`: the run no longer wants the
    attempt, so the worker dropped it. The step was cancelled or timed
    out, the run ended or was deleted, its result arrived through
    `Store.Deliver` while the handler ran, or the run rejected the result.
    The store removed the job while this claim's lease was live, and it
    does not run again. A handler that returned `workflow.ErrDeferred`
    fires `Settled` (`deferred`) instead, because the hand-off happened.
  - `LeaseLost func(job workflow.Job)`: the worker lost its claim, so
    another claim runs the attempt again or has already run it. Another
    worker claimed the job (and may have settled it), no heartbeat
    succeeded for a whole lease, the result could not be recorded before
    the lease ran out, or a database error made the worker leave the job
    to its lease.
  - `GivenBack func(job workflow.Job)`: on shutdown, the worker gave the
    claim back without counting it toward `MaxClaims`.
  - `Abandoned func(job workflow.Job)`: the worker gave up on the job. A
    step or timer fires it on claim `MaxClaims+1`, after `MaxClaims`
    claims left it unfinished: the worker recorded the step as failed
    with type `abandoned`, or failed the run as `abandoned` (a timer, or
    on claim `MaxClaims+2` a step whose failure could not be applied). A
    run whose definition cannot be loaded fires it on claim `MaxClaims`,
    when the worker fails the run as `definition_unavailable`. These are
    the worker's thresholds today, and this PRD does not change them.
  - `ClaimError func(err error)`: a claim query failed. It does not fire
    when the error comes from shutdown.
- R-3: `Settlement` has `Outcome Outcome`, `Duration time.Duration`, and
  `Error *workflow.StepError` (set only for `OutcomeFailed`). `Outcome`
  is a string type with the constants `OutcomeCompleted`, `OutcomeFailed`,
  `OutcomeDeferred` and `OutcomeFired`.
- R-4: Each claimed job fires `Claimed` once, then exactly one ending
  event, as this table says. The one exception is a postponed claim. It
  has no ending event, because the job waits for its run's definition and
  comes back as a new claim. So `Claimed` minus the ending events is not
  a count of work in flight. Issue #50 proposes `Worker.Active` for that.

  | How the claim ends | Event |
  | --- | --- |
  | The run applied the result | `Settled` |
  | Output Postgres cannot store, recorded as `invalid_output` | `Settled` (`failed`) |
  | A deferred job is marked done | `Settled` (`deferred`) |
  | A deferred job is gone (its result came through `Store.Deliver` first) | `Settled` (`deferred`) |
  | A timer fires | `Settled` (`fired`) |
  | The job is gone at heartbeat, settle, abort or give-back, and the write started while the claim's lease was live | `Cancelled` |
  | The run is gone (`ErrRunNotFound`) | `Cancelled` |
  | The run rejects the event (a permanent `Apply` error) | `Cancelled` |
  | The job exists under a newer fence | `LeaseLost` |
  | The job is gone, and the write started after the claim's lease ran out | `LeaseLost` |
  | No heartbeat succeeds for a whole lease | `LeaseLost` |
  | The settle or finish misses the lease deadline | `LeaseLost` |
  | A database error: loading the definition, settling with an unloadable definition, failing the run, giving the claim back | `LeaseLost` |
  | Shutdown gives the claim back (a step, a timer, a definition load) | `GivenBack` |
  | Claim `MaxClaims+1` of a step or timer: the step fails as `abandoned`, or the run fails | `Abandoned` |
  | Claim `MaxClaims` of a run whose definition cannot be loaded: the run fails | `Abandoned` |
  | The definition cannot be loaded, before claim `MaxClaims`: postponed | none |

  A fenced write that changes no row asks whether the job still exists.
  A row means that a newer claim holds the job: `LeaseLost`. No row has
  two causes. While the claim's lease is live, no other worker can claim
  the job, so only the run removed it (a cancel, a timeout, a result
  through `Store.Deliver`, or the run ended or was deleted): `Cancelled`.
  After the lease runs out, another worker may have claimed the job and
  settled it, which also deletes the row: `LeaseLost`. The deleted row
  cannot tell these two apart, and in both this worker lost its claim.
  The worker measures the lease on its own clock, from the start of its
  last successful heartbeat, so its lease ends no later than the one the
  database holds. Settle, finish and abort run under a deadline at the
  end of the lease, so their writes always start inside it. A heartbeat
  or give-back can start after it, for example when the process was
  paused.
- R-5: The worker builds no value and calls nothing for a field that is
  nil. The callbacks take `workflow.Job` and `Settlement` by value, so a
  set field adds no allocation either.
- R-6: Callbacks run synchronously on the worker's goroutines, and
  concurrently with each other. The doc comment on `Observer` says so,
  and says that a slow `Claimed` delays the handler and a slow `Settled`
  holds one of the worker's `Concurrency` slots. The worker does not
  recover a panic in a callback.
- R-7: `Store.QueueStats(ctx) ([]QueueStat, error)` returns, per queue,
  `QueueStat{Kind workflow.CommandKind, StepKind string, Due int,
  OldestDue time.Duration}` as UC-3 defines. It uses the `jobs_claim`
  index to list queues and reads no future job of a queue with few due
  jobs. It adds no migration. Its doc comment says that the cost grows
  with the number of due jobs, so one process should poll it.
- R-8: No file in either module imports a metrics or tracing library.
- R-9: An `Example*` test in the postgres module wires an `Observer` to
  plain counters, runs a short workflow, and prints the counts and one
  `QueueStats` result. It has an `// Output:` block. Without
  `WORKFLOW_PG_DSN` it does not run, so `go test` still passes without
  Postgres.
- R-10: `documentation/hosts.md` (Workers) has a "Metrics" paragraph:
  the events, the one-ending-event rule, the callback rules, and
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
- A reason on `Abandoned` (`abandoned` or `definition_unavailable`). The
  run's error and the log say which. Add a field when a user asks.
- A queue filter on `QueueStats`. Add it when a user asks.
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
- **Exactly one ending event per claim (R-4), except a postponed
  claim.** Each path of `process` maps to one row, so a test can check
  each path, and counts add up. Rejected: fire `Settled` for every end
  with an outcome such as `lost`, because the issue and the brief name
  those as separate events.
- **`Cancelled` is its own event (PRD review).** Timeouts and run cancels
  are normal. Lost claims point at slow or cut-off workers. One event for
  both would page on every timeout. The name matches `StepCancelled`.
  Rejected: one `LeaseLost` for both.
- **A rejected result fires `Cancelled`.** The run will never take the
  result, the job is removed, and the attempt does not run again, which
  is what `Cancelled` means to the user. The worker also logs a warning.
  Rejected: `Settled`, which would count a result the run did not record.
- **A job gone after the lease ran out fires `LeaseLost` (PR review).**
  Once the lease is out, a newer claim may have settled the job, and a
  deleted row cannot say what removed it. In both cases the worker lost
  its claim, and `LeaseLost` is the event that alerts on that. The rule
  does not need the run's lock, so it also holds for `finish`, which
  deletes without it. Rejected: keep a tombstone row, or read the run's
  state, to tell a settle by another worker from a cancel. Both add a
  write or a query to every settle, for a race that needs a worker paused
  past its lease.
- **A database error that leaves a job to its lease fires `LeaseLost`.**
  The user sees the same thing as a lost claim: the work is not recorded
  and runs again after the lease. Rejected: no event, which leaves a gap
  in R-4.
- **A run that fails as `definition_unavailable` fires `Abandoned`.** The
  run is lost on claim `MaxClaims`, the same as an abandoned step.
  Rejected: no event, which hides a lost run from metrics.
- **`Settled` fires after the commit.** It counts results that are in the
  database. A result that the worker drops is `Cancelled` or
  `LeaseLost`. Rejected: fire when the handler returns, which would count
  results that never land.
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
- **`QueueStat` has `Kind` and `StepKind`, not the `jobs.queue` string
  (PRD review).** These are the labels that the callbacks get from
  `workflow.Job`, so throughput and lag for one step kind share a panel.
  The `step:<kind>` format stays internal. Rejected: a `Queue` string,
  which makes users parse it.
- **`QueueStats` stays a full count, with no new index (PRD review).**
  1,000,000 due jobs cost under 0.4 s, and one process polls. Rejected:
  a capped count, which hides the size of a large backlog.
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
- A new exit from `process` (for example in #50 or #56) must map to a row
  of R-4. Mitigation: the ending events fire inside `settle`, `finish`,
  `abort` and `release`, so a new path that uses them is covered. Not
  blocking.
