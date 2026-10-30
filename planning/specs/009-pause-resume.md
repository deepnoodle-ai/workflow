# Pause and resume a run: engine status, host hold

**Status:** Accepted
**Date:** 2026-10-29
**Workflow:** Spec, then build after the PRD check. The design is clear;
the claim query and the local hold need a second pair of eyes.
**PRD:** [prds/009-pause-resume.md](../prds/009-pause-resume.md)

## Context

An operator must be able to hold one run, look, and continue it (PRD 009).
A run has dispatched work before a Pause arrives: a retry is dispatched at
once with a backoff `NotBefore`, and a step can wait in a busy queue. On a
durable host that work is a job row; on `local` it is a goroutine that
sleeps until `NotBefore`. The engine cannot reach either. So the hold
lives in the host, and the engine only records the status.

## Goals

- `Store.Pause` and `Store.Resume` (postgres, sqlite) and `Run.Pause` and
  `Run.Resume` (local) exist and match Cancel's variants.
- After a Pause commits, no claim takes a step job of the run, and local
  calls no handler of the run, until Resume or Cancel.
- Timer jobs still fire during a pause, and a step timeout that fires
  during a pause uses up no attempt: Resume re-arms it.
- Apply gives the same result as today for every event sequence without a
  Pause. `property_test.go` and the soundness fuzz stay green.
- The postgres claim keeps its index plan (`claim_test.go`).

## Non-goals

- An engine-side hold of dispatches, a held-dispatch list, or a resume
  order. The host hold makes them unnecessary.
- Moving timers by the pause length.
- A schema migration. The filter uses `runs.status`, which exists.

## Proposal

```
          Pause / Resume (one-line wrappers, like Cancel)
                          |
                          v
   Apply: running <-> paused, State.Reason, timeouts held while paused.
                          |
         +----------------+------------------+
         v                                   v
  durable (hostcore + backends)            local driver
  claim skips step jobs of paused runs     runStep waits while paused
  Resume wakes workers                     Resume or cancel ends the wait
```

### Engine (root package)

- `state.go`: add `StatusPaused RunStatus = "paused"`. `Terminal()` and
  `Outcome()` do not change: paused is not terminal, and its outcome is
  "" like running. Update the status doc comment and the `Reason` field
  comment ("why the run is paused, or why it was cancelled").
- `event.go`: add `EventPause` and `EventResume` next to `EventCancel`.
  `Reason` is read for Pause. `Force` is ignored for both.
- `apply.go`, the event switch:

  ```go
  case EventPause:
      if st.Status != StatusRunning {
          return st, nil, nil
      }
      r.st = st.clone()
      r.st.Status, r.st.Reason = StatusPaused, ev.Reason
  case EventResume:
      if st.Status != StatusPaused {
          return st, nil, nil
      }
      r.st = st.clone()
      r.st.Status, r.st.Reason = StatusRunning, ""
  ```

  Both then go through `drain`, `collect`, `prune` and the `UpdatedAt`
  stamp, as every event does. They queue no transition, so `drain` does
  nothing.

- A paused run must progress exactly like a running one. Add one helper
  and use it at each check that means "the run is in its main phase":

  ```go
  // progressing reports whether the run is in its main phase: running,
  // or paused, which the host holds and Apply does not.
  func (s RunStatus) progressing() bool {
      return s == StatusRunning || s == StatusPaused
  }
  ```

  The checks: `drain` (`r.st.Status != StatusRunning`), `live` (the
  main-path return), `fireJoins` (the early return), and `failRun` (the
  guard). The `EventStart` literal keeps `StatusRunning`. A grep for
  `StatusRunning` in non-test files must find only that literal, the
  constant, and the helper. (As built: the main-phase checks call
  `progressing()`, so the grep also finds the `EventPause` and
  `EventResume` cases.)

- A decided outcome ends the pause with no extra code: `failRun` sets
  `failing` or calls `conclude`, and `conclude` sets a finishing or
  terminal status. Add one line where each starts: when the status was
  `paused`, clear `State.Reason`. Cancel sets its own reason after
  `cancelAll`, so a cancel of a paused run keeps the cancel reason.

- **A timeout that fires during a pause is re-armed at Resume.** State
  gains one bookkeeping field
  (not API), serialized as `timeouts`:

  ```go
  // timeouts holds the attempts whose step timeout fired while the run
  // was paused, by StepID: zero until Resume re-arms the timeout, then
  // the new deadline. An entry goes when its attempt settles.
  timeouts map[string]time.Time
  ```

  - `TimerFired` for a step timeout (not a sleep) while the status is
    `paused`: set `timeouts[id]` to zero. Do not emit `CancelStep`, and
    do not fail the attempt. The unit stays `running`, so durable hosts
    keep its step job and settle no step record.
  - `EventResume`: for each zero entry, sorted, whose attempt still runs,
    set the deadline to `at + step.Timeout` and emit `ScheduleTimer` with
    it. An entry with a deadline already has a live timer: leave it, so a
    second pause neither adds a timer nor moves the deadline.
  - An entry goes when its attempt settles or is replaced. After each
    Apply, an entry stays only if `State.Unit(id)` is not nil (nil for a
    replaced attempt) and its status is `running` (not for a settled
    one). So an attempt-1 entry does not stay while attempt 2 runs, and an
    attempt that completes during a pause loses its entry. A cancel, a
    failure or a completion settles every attempt, so the map empties
    when the pause ends that way.
  - `Outstanding` uses an entry's deadline for the timeout timer it
    rebuilds, and rebuilds none for a zero entry: that timer has fired.
  - `clone` copies the map.

  A timer that has not fired at Resume keeps its deadline. Apply cannot
  tell a timer's age from `TimerFired`, so it cannot move it.

- `Abort` needs no change. It treats every non-terminal status alike.
  `Outstanding` on a paused run returns the dispatches in flight, and the
  host holds them.

### Host contract (`documentation/hosts.md`)

Add to "Cancel and Abort" (renamed "Cancel, pause and Abort") and to
"Status and time":

> A host must not start a dispatched step of a paused run. It still fires
> timers. When the run leaves `paused` (Resume, Cancel, or a decided
> outcome), the held steps may start, and the host should start them
> without waiting for a poll. A claim made before the Pause commits may
> still start its step.

### Durable hosts

Shared policy stays in `internal/hostcore`; each backend changes its SQL.

**Claim filter.** Add one predicate to each claim of a step job:

```sql
AND (j.kind = 'timer' OR NOT EXISTS (
    SELECT 1 FROM runs r WHERE r.id = j.run_id AND r.status = 'paused'))
```

- postgres `claimSQL`: inside the `LATERAL` subquery, next to the
  `lease_expires` test, so `LIMIT` counts only claimable rows and
  `FOR UPDATE SKIP LOCKED` locks only them. `runs.id` is the primary key,
  so each test is one index probe.
- postgres `claimKeySQL`: the statement already joins `runs r`; add
  `AND (t.kind = 'timer' OR r.status <> 'paused')`.
- sqlite `claimSQL` (the inner `SELECT`) and the `ClaimKey` statement: the
  same predicate with the `{{p}}` prefix.
- `QueueStats`, both backends: the same predicate in the count, so `Due`
  still means "can be claimed now" (its doc comment says so).

Status `paused` as a literal in SQL is fine: hostcore already writes
`next.Status` into `runs.status` on every update.

**Why the claim did not happen.** `JobStatus` gains `RunPaused bool`, read
with a join on `runs`. `whyNotClaimed` checks it after "no handler" and
before "not due", and returns `ErrNotClaimable: its run is paused`. A
Cloud Tasks handler answers 503, and the queue retries the task.

**Wake on Resume.** In `hostcore.Store.persist`, after the job loop:
when `prev.Status == paused` and `next.Status` is not, set `wroteJobs`,
so `tx.JobsWritten` runs. That is `pg_notify` on the jobs channel for
postgres and `hub.signal` for sqlite. A Cancel of a paused run wakes them
too, which the finally jobs need anyway.

**Held jobs in Resume's result.** In `DeliverLocked`, after `persist`, on
a Resume (`paused` to `running`): append the `DispatchStep` commands of
`workflow.Outstanding(def, next)` to the result's commands, not to the
persisted ones. `Result.Jobs()` then lists every running step unit after
the re-armed timers: the held ones, and any a worker is already running
(its claim was made before the Pause). A push host sends them again; a
push of a running one finds it leased, which is harmless. A held step's job stays
in the outbox. Once the run leaves `paused`, a re-push claims it and runs
the step. A second push of a job already claimed gets `ErrNotClaimable`.
(As built in #104, 48bafc0, with the suite test `ResumeListsTheHeldSteps`.
A deferred step is in flight with no job, since its job ended at the
hand-off; its listed reference finds no job, and `Execute` returns nil.)

**Storable.** `EventPause` cleans NUL characters from `Reason`, like
`EventCancel`.

**Public methods.** Each delivers the event, like Cancel:

| Host | Pause | Resume |
|---|---|---|
| postgres | `Pause`, `PauseTx`, `PauseSQLTx` | `Resume`, `ResumeTx`, `ResumeSQLTx` |
| sqlite | `Pause`, `PauseSQLTx` | `Resume`, `ResumeSQLTx` |

`internal/parity`'s `store` interface gains `Pause`, `PauseSQLTx`,
`Resume` and `ResumeSQLTx`.

### local

The driver gains a hold gate, guarded by `d.mu`:

```go
// held is non-nil while the run is paused. It is closed when the run
// leaves paused.
held chan struct{}
```

- (As built, the helper is `d.hold`. It starts right after `Apply`, before
  the reply to `Pause`, and is released after the commands, so a cancel
  stops a held step first.)
- `loop` calls `d.hold(true)` when the status is `paused`, before it
  carries out commands, on each pass, including the first. So a paused state that
  `Runner.Resume` loads is held from the start.
- `runStep`, after the `NotBefore` wait and before `d.call`:

  ```go
  for {
      d.mu.Lock()
      held := d.held
      d.mu.Unlock()
      if held == nil {
          break
      }
      select {
      case <-ctx.Done():
          return // cancelled: the attempt is not wanted
      case <-held:
      }
  }
  ```

  A cancel of the run emits `CancelStep` for each held attempt, which
  cancels its context. The wait ends, and the handler is never called.
- `Run.Pause(reason)` and `Run.Resume()` deliver the events, like
  `Run.Cancel`. The doc comments of `Run.Resume` and `Runner.Resume` name
  each other.

Timers need no change: `time.AfterFunc` fires and `TimerFired` applies.

## Tests

- Engine (`apply_test.go` or a new `pause_test.go`):
  - Pause and Resume no-ops for each other status; `ErrNotStarted` before
    Start.
  - A step completes while paused: its successor's `DispatchStep` is
    emitted, and the status stays `paused`.
  - A join fires while paused.
  - A timeout while paused: no `CancelStep`, no new attempt, status stays
    `paused`; Resume emits a `ScheduleTimer` at the Resume time plus the
    timeout; that timer then fails the attempt as usual; `Outstanding`
    while paused rebuilds no timeout for it, and after Resume rebuilds the
    new deadline.
  - Each outcome while paused: the last path ends (`completed`, Reason
    cleared); a step fails for good (`failing`, `on_failure` dispatched);
    Cancel (`cancelling`, finally dispatched, Reason is the cancel's).
  - Property: insert a Pause and a Resume at random points of a random
    event sequence, each stamped with the time of the event before it.
    The final state and commands equal the ones without them. Add it to
    `property_test.go`'s generator, at the default small sample.
- `internal/hosttest` (both backends), one test in `list.go`,
  `PauseHoldsStepsUntilResume`: a step fails with a retryable error and a
  short backoff; Pause; wait past the backoff; `RunOnce` calls no handler
  and `Execute` returns `ErrNotClaimable`; `QueueStats` shows no due step;
  Resume; `RunOnce` runs the retry; the run completes.
- `internal/hosttest`, `PauseStopsTheTimeoutClock` (the PR #94 review's
  test): a step with a short timeout and a retry pending; Pause; wait
  past the timeout; `RunOnce` fires the timer; the handler count does not
  change, no attempt is used up, and the status stays `paused`. Resume;
  the attempt runs and the run completes.
- `internal/hosttest`, `CancelAPausedRun`: cancel a paused run; the held
  attempt is cancelled without running, and `finally` runs.
- Engine, two pauses: a timeout fires in the first pause; Resume; pause
  again; Resume. After the second Resume the deadline is still the first
  Resume plus the timeout, no second `ScheduleTimer` is emitted, and
  `Outstanding` rebuilds that deadline.
- Engine: an attempt whose timeout fired during a pause completes during
  the same pause; its entry is gone after the completion.
- postgres `claim_test.go`: run the plan check with one paused run that
  holds due jobs, and keep the "Index Scan using jobs_claim" assertion.
  Relax the "Rows Removed by Filter" assertion only for the paused rows.
- local: a test for the hold and the cancel during the hold, a test for
  `Runner.Resume` of a paused saved state, and `ExamplePause` in
  `local/example_test.go`.

## Alternatives considered

- **Hold new dispatches in the engine** (the first PRD draft). Units stay
  `pending` while paused, and Resume dispatches them. It misses every
  attempt dispatched before the Pause, so the host needs a hold anyway.
  Two holds, plus a held-unit shape and a resume order. Rejected.
- **Move held jobs out of reach**: on Pause, set `run_at` of the run's
  step jobs to infinity; on Resume, restore it from the command's
  `NotBefore`. The claim query stays as it is, and held jobs cost no
  claim read. But every write path that sets `run_at` (insert during the
  pause, `Postpone`, the give-back on shutdown) must know about pause,
  and a restore bug loses a job's due time. Rejected for now; it is the
  fallback if the claim cost shows up (see Tradeoffs).
- **Re-check the run's status in the worker before each handler call.**
  It narrows the race with a Pause but does not close it, and it costs a
  read per job. Rejected. The documented rule covers the race.

## Tradeoffs and consequences

- **Held jobs stay at the front of their queue.** Each claim of that
  queue reads past them: one index row and one `runs` probe each. A few
  paused runs cost nothing visible. Thousands of held due jobs (a paused
  run with a large `each`) slow every claim of that queue. If that shows
  up, use the `run_at` fallback above.
- **A race with the Pause.** A job claimed before the Pause commits
  starts. This is stated in the contract.
- **A hung handler times out later.** A handler that hangs during a
  pause gets its timeout a full `timeout` after the Resume.
- **A short pause does not extend a timeout, and the cut-off is sharp.**
  If the timer has not fired by the Resume, it keeps its deadline, and a
  held attempt gets only the time that is left. A 60s timeout dispatched
  at t=0 gives 1s after a Resume at 59s, but a full 60s after a Resume at
  61s.
- **Cloud Tasks with one request per job.** A held job's task gets 503
  until Resume. With a short queue retry limit the task is dropped, and
  the scheduled `RunOnce` safety net claims the job after Resume.

## Rollout

No migration and no flag. Runs that never pause see no change. A state
from an older version never has `paused`, so it loads as before. An
older worker that shares a database with a newer one does not know the
filter and would claim held jobs, so deploy the workers before anyone
uses Pause. The PR description says so.

## Open questions

- None that change the design. If the property test finds a difference
  that the Pause introduces, a `Status == StatusRunning` check was
  missed: fix the check, not the test.
