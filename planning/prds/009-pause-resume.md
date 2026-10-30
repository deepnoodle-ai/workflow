# Pause a run, look, and resume it

Status: Draft
PRD PR: #TBD  Implementation PR: #TBD  Updated: 2026-10-29

## Problem

A Go developer runs workflows in production on the postgres or sqlite
host, or in-process with `local`. An operator sees a run go wrong. The
input is bad, or a downstream API is down and each new step fails against
it. The operator wants to stop new work on that run now. Then they look at
the run, fix the cause, and let the run continue from where it stopped.

Today the only tool is Cancel. Cancel ends the run for good: it cancels
the steps in flight, runs the finally steps, and the run ends `cancelled`.
The work the run already did is lost to it. The operator must start a new
run and redo that work, or let the run keep failing.

Other ways out are worse:

- Stop the workers (`MayClaim`, `Shutdown`). This stops every run on the
  worker, not the one run, and a timer job still fires when a worker comes
  back.
- Fail the steps on purpose until the run fails. That burns retries and
  ends the run too.
- Edit the stored state by hand. It is not API, and a wrong edit corrupts
  the run.

README lists "Pausing and resuming a run by hand" under "Not yet".
PRD 002 named operator pause as a need of the old engine (US-005). The
need did not go away with the rewrite in #42.

## Context

**Our code.** The engine is `Apply(def, state, event)`. `EventCancel` is
the one operator event today. Hosts wrap it: `local.Run.Cancel`,
`postgres.Store.Cancel`, `CancelTx` and `CancelSQLTx`, and
`sqlite.Store.Cancel` and `CancelSQLTx`. Each wrapper is one line that
delivers the event. The shared core (`internal/hostcore`) persists what
Apply returns: a `DispatchStep` or `ScheduleTimer` becomes a job row, and
a worker claims job rows. No job row means no work.

Every new attempt comes from one place in Apply: the dispatch of a unit.
That covers a first attempt, an `each` item, and a retry. A retry is
dispatched at once with a `NotBefore` for its backoff. A `sleep` is a
timer, and its `TimerFired` settles it. A step's `timeout` is a timer on
the attempt. `set`, `join` and routing are pure: they do no I/O and start
nothing outside the engine.

`State.Unit` statuses already include `pending`: a unit that is not yet
dispatched, used today for `each` concurrency. `Outstanding` rebuilds the
commands for `running` units only.

**Worker pause is not run pause.** `MayClaim` pauses a worker's claiming.
It holds every run on that worker and does not change any run's state.
Run pause is per run, on every host, and is part of the run's state.

**How comparable products pause.** (From their public docs, 2026.)

| Product | What pauses | Work in flight | While paused |
|---|---|---|---|
| Temporal | One activity (activity pause). A whole workflow needs a signal that the workflow code checks. | Runs to the end | No new attempt of that activity starts |
| AWS Step Functions | Nothing. The user stops the execution and redrives it, or builds a `waitForTaskToken` gate into the definition. | Stopped | Not applicable |
| Inngest | A whole function: all its runs | Runs to the end | No step of its runs starts; new runs wait |
| Argo Workflows | One workflow (`argo suspend`) | Runs to the end | No new node starts until `argo resume` |

The common shape, where it exists: work in flight finishes, nothing new
starts, resume continues. No product cancels the work in flight. Argo's
suspend is closest to what we need: per run, operator-driven, and nothing
in the definition changes. We follow it.

## Users and job

Primary user: the developer who operates workflows in production and
builds the operator's tools (an admin endpoint, a CLI, a button). When a
tradeoff comes up, we optimize for this job: "stop new work on this run
now, without losing the run, and continue it later."

Secondary user: a developer who debugs a run in a test or with `local`.

## Concepts

- **Pause**: an operator holds a run. Steps that already run finish, and
  their results are recorded. Nothing new starts until Resume: no new
  steps, no retries, and no new work from a timer that fires.
- **Resume**: the operator releases the hold. The steps that the pause
  held start, and the run continues.
- **`paused`**: a new `RunStatus`. A run is `paused` only between a Pause
  and the next Resume, Cancel, or decided outcome. It is not terminal.
- **Held step**: a step attempt that the run would start but does not,
  because the run is paused. It is not dispatched, so it has no job row
  and no handler call.

Pause and Resume sit next to Cancel. Unlike Cancel, they do not decide the
outcome.

## Use cases

The examples use this definition.

```yaml
name: order

steps:
  - name: charge
    action: payments.charge
    retry:
      max_attempts: 3
      backoff: 30s
    next: wait_a_bit

  - name: wait_a_bit
    sleep: 1h
    next: ship

  - name: ship
    action: shipping.create_label

finally:
  - name: release
    action: inventory.release
```

### UC-1: Pause a run on a durable host

```go
res, err := store.Pause(ctx, "order-123", "payment API down")
```

The run's status becomes `paused`. A `charge` attempt that runs now
finishes, and its result is recorded. No new job row appears for the run.

Acceptance:
- [ ] `res.Applied` is true and `res.State.Status` is `paused`.
- [ ] `res.State.Reason` is `"payment API down"`.
- [ ] The step in flight is not cancelled. Its handler's context stays
      live. Its result is recorded as usual.
- [ ] After that result, the run has no job row for a new attempt.
- [ ] `GetRun` shows status `paused`. The event log has a `pause` event
      with the reason.
- [ ] Pause of a run that is not `running` changes nothing: `Applied` is
      false, and the status and reason stay as they were.
- [ ] Pause of a run that does not exist returns the same error as Cancel.

### UC-2: Resume the run

```go
res, err := store.Resume(ctx, "order-123")
```

The held steps start. The run continues as if it had not paused.

Acceptance:
- [ ] `res.Applied` is true and the status is `running`.
- [ ] `res.State.Reason` is empty.
- [ ] `res.Commands` holds a `DispatchStep` for each held step, in a fixed
      order: by step ID. Each `concurrency` limit holds.
- [ ] A held retry keeps its attempt number and its backoff `NotBefore`.
      If the backoff ended during the pause, the retry runs now.
- [ ] Resume of a run that is not `paused` changes nothing: `Applied` is
      false.
- [ ] The run finishes with the same outcome and outputs as a run that
      never paused.

### UC-3: Cancel a paused run

```go
res, err := store.Cancel(ctx, "order-123", "give up")
```

Acceptance:
- [ ] The status becomes `cancelling`, then `cancelled` when `release`
      finishes. The run is not `paused` after the cancel.
- [ ] Held steps never start. They do not appear as cancelled attempts,
      because they were never dispatched.
- [ ] `State.Reason` is `"give up"`.

### UC-4: A timer ends while the run is paused

A `charge` attempt fails while the run is paused. Or `wait_a_bit` ends
while the run is paused.

Acceptance:
- [ ] The failed attempt is recorded. Its retry is held: no job row, no
      handler call.
- [ ] The sleep settles when its timer fires. `ship` is held.
- [ ] A step's `timeout` still fires during a pause. The timed-out attempt
      is recorded, and its retry is held.
- [ ] After Resume, the held retry and `ship` start.

### UC-5: Pause and resume with local

```go
run, _ := runner.Start(ctx, "order-123", def, input)
_ = run.Pause("debug")
// look at the run, fix the cause
_ = run.Resume()
st, err := run.Wait()
```

Acceptance:
- [ ] `run.Pause` and `run.Resume` return nil on a live run, and
      `ErrNotRunning` once the run has finished or stopped, like
      `run.Cancel`.
- [ ] A paused run saved with `Save` and continued with `Runner.Resume`
      stays paused. No held step starts until `run.Resume()`.
- [ ] An `Example` test shows pause and resume, and its output proves
      that no step started while the run was paused.

## Requirements

- R-1: Two engine events, `EventPause` (with `Reason`) and `EventResume`,
  sit next to `EventCancel`.
- R-2: Pause moves a `running` run to `paused` and sets `State.Reason`.
  For any other status it returns the state unchanged with no commands.
- R-3: While paused, Apply accepts step results and timer firings. It
  records them and moves the run through `set`, `join` and routing as
  usual. It does not dispatch: each unit it would dispatch stays
  `pending`. A retry stays `pending` with its attempt and backoff kept.
- R-4: Resume moves a `paused` run to `running`, clears `State.Reason`, and
  dispatches the held units, sorted by node key and then unit index,
  within each step's `concurrency`. For any other status it returns the
  state unchanged with no commands.
- R-5: Cancel works on a paused run as on a running one. Held units are
  skipped, and the finally steps run.
- R-6: When the run's outcome is decided while it is paused (every path
  ended, or a step failed for good), the pause ends. The run moves to
  `completing`, `failing` or a terminal status, and its `on_failure` and
  `finally` steps run.
- R-7: `Outstanding` returns no command for a held unit. A durable host
  writes no job row for one.
- R-8: Host API, matching Cancel exactly:
  - postgres: `Store.Pause(ctx, runID, reason)`, `Store.Resume(ctx,
    runID)`, and the `Tx` and `SQLTx` variants that Cancel has.
  - sqlite: `Store.Pause`, `Store.Resume`, and the `SQLTx` variants.
  - local: `Run.Pause(reason)` and `Run.Resume()`.
  Each one delivers the event, as Cancel does. `internal/parity` checks
  that postgres and sqlite share the new methods.
- R-9: `hostcore.Storable` cleans NUL characters from a pause reason, as it
  does for a cancel reason.
- R-10: Docs: `documentation/hosts.md` ("Cancel and Abort" becomes "Cancel,
  pause and Abort", plus the status list), `llms.txt`, README and
  `doc.go`. Remove "Pausing and resuming a run by hand" from README's
  "Not yet" list and the matching words from `llms.txt`.

## Not in scope

- Pause one path or one step. The operator's job is to hold the run.
  Per-path pause made PRD 002 complex, and no use case needs it now.
- A pause step kind in the definition. That is a gate an author writes.
  The `wait` step (track wait-step) does that job.
- Auto-resume after a time. The host app can call Resume from a timer or
  a cron job.
- Cancel the work in flight on pause. The operator can Cancel instead.
  No compared product does it.
- Pause many runs at once, such as every run of a definition. The host
  app lists runs (track run-listing) and pauses each one.
- Stop the clock during a pause. See Decisions.
- Pause during `completing`, `failing` or `cancelling`. Cleanup runs to
  the end, as it does for a second cancel.

## Decisions

- **Paused is a `RunStatus`, `paused`.** One field tells the operator
  what the run does. Run listing can filter on it with no new column.
  Rejected: a `Paused bool` on State next to `running`. Then two fields
  describe one fact, and every status filter must check both.

- **Pause holds dispatches, not events.** Results and timer firings are
  applied while paused. Pure steps (`set`, `join`, routing) still run.
  Only the start of host work is held. The operator sees the latest
  results while they look, and Resume has nothing to replay. Rejected:
  queue every event until Resume. That needs an event store in every
  host, and a step result that waits for Resume is a result the operator
  cannot see.

- **The clock does not stop.** Sleep and timeout durations include paused
  time. A retry keeps the `NotBefore` its backoff set. If that time is past
  at Resume, the retry runs at once. The pause is for the operator's fix,
  and the time after it does not need to repeat. It also keeps Apply
  simple: no timer moves. Rejected: extend every timer by the pause
  length. That needs the pause start in State, and it changes timers that
  the host already scheduled.

- **A decided outcome ends the pause.** If the last path ends, or a step
  fails for good while paused, the run moves on to `completing` or
  `failing`, and its `on_failure` and `finally` steps run. A pause holds
  new forward work, not cleanup. This is the same rule as Cancel on a
  paused run, and it keeps `paused` reachable only from `running`.
  Rejected: hold cleanup until Resume. That needs a paused form of each
  finishing status, or a second field.

- **The pause reason reuses `State.Reason`.** Today it holds the cancel
  reason. It now holds the pause reason while the run is paused. Resume,
  or a decided outcome other than cancel, clears it. A cancel replaces it.
  Durable hosts also keep the reason in the event log. Rejected: a new
  `PauseReason` field. The brief says no pause store beyond State, and one
  reason at a time is enough.

- **A second Pause or a stray Resume is a no-op.** Like a second cancel,
  it returns the state unchanged, so `Applied` is false and a retried
  request is safe. A second Pause does not replace the reason. Rejected:
  an error. Operators retry clicks, and hosts deliver at least once.

- **Host methods match Cancel exactly.** Pause has the same variants as
  Cancel on each host, and Resume takes no reason. An operator who knows
  Cancel knows Pause. Rejected: only `Deliver(EventPause)`. Each wrapper is
  one line, and it makes the feature easy to find.

- **The local method is `Run.Resume`, although `Runner.Resume` exists.**
  `Runner.Resume` continues a saved run in a new process. `Run.Resume`
  releases a pause. The receivers differ, and the docs of each name the
  other. The shared vocabulary says Resume. Rejected: `Run.Unpause`. It
  adds a second word for one idea across the hosts.

- **Resume dispatches in node key order, then unit index.** Apply must be
  deterministic. That is the order `Outstanding` and `InFlight` use.
  Rejected: the order in which the units were held. It needs a list in
  State and gives no better result.

## Success

- An operator holds a run with one call on each host and continues it
  with one call. The run then finishes with the same outcome and outputs
  as a run that never paused.
- No step attempt starts while a run is paused, on any host.
- Nothing gets worse for runs that never pause: `property_test.go` and
  the soundness fuzz stay green, and no event sequence without Pause
  gives a different result.

## Risks and open questions

- **Status checks that mean "progressing".** Apply checks
  `Status == running` in several places (drain, `live`, joins,
  `failRun`). A paused run must pass each of them, or joins stop firing
  and failures are lost. The tech spec lists each check. Tests cover a
  join, a failure and a completion while paused. Does not block.
- **Hosts that read `running`.** A host app that tests
  `Status == StatusRunning` to mean "not finished" sees `paused` as
  finished. `Terminal()` is the documented test, and the docs say so.
  Does not block.
- **A `pending` unit with an attempt above zero.** Held retries are a new
  shape of unit. `AttemptOutcome` and the step records must treat the
  failed attempt as failed, not as dispatched. The tech spec covers it.
  Does not block.
- **wait-step overlap.** A `wait` step that gets its delivery while the run
  is paused records it, and its successors are held. Both tracks edit
  `apply.go` and `event.go`. The second PR to merge rebases. Does not
  block.
