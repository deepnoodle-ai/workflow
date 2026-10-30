# Pause a run, look, and resume it

Status: Approved
PRD PR: #94  Implementation PR: #104  Updated: 2026-10-29 (timeout rule revised after the #94 review)
Tech spec: [specs/009-pause-resume.md](../specs/009-pause-resume.md)

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
  worker, not the one run.
- Fail the steps on purpose until the run fails. That burns retries and
  ends the run too.
- Edit the stored state or the jobs table by hand. Neither is API, and a
  wrong edit corrupts the run.

README lists "Pausing and resuming a run by hand" under "Not yet".
PRD 002 named operator pause as a need of the old engine (US-005). The
need did not go away with the rewrite in #42.

## Context

**Our code.** The engine is `Apply(def, state, event)`. `EventCancel` is
the one operator event today. Hosts wrap it: `local.Run.Cancel`,
`postgres.Store.Cancel`, `CancelTx` and `CancelSQLTx`, and
`sqlite.Store.Cancel` and `CancelSQLTx`. Each wrapper is one line that
delivers the event.

Apply dispatches work before it starts. A retry is dispatched at once,
with a `NotBefore` for its backoff. On a durable host, each dispatch is a
job row with a `run_at`, and a worker claims it when it is due. On
`local`, each dispatch is a goroutine that waits for `NotBefore` and then
calls the handler. So when an operator pauses a run, the run can already
have work that is dispatched but not started: a retry that waits for its
backoff, or a step that waits in a busy queue. The engine cannot hold that
work. Only the host decides when a job starts, so the hold must live in
the host.

**Worker pause is not run pause.** `MayClaim` pauses a worker's claiming.
It holds every run on that worker and does not change any run's state.
Run pause is per run, on every host, and is part of the run's state.

**How comparable products pause.** (From their public docs, 2026.)

| Product | What pauses | Work in flight | While paused |
|---|---|---|---|
| Temporal | One workflow (`temporal workflow pause` and `unpause`, pre-release, server 1.30+). Status `Paused`, which list queries can filter on. | Activities finish | No new activity starts, and retries are held. Timers keep running. Signals and fired timers are recorded and processed at unpause. Cancel takes effect at unpause; terminate at once. |
| AWS Step Functions | Nothing. The user stops the execution and redrives it, or builds a `waitForTaskToken` gate into the definition. | Stopped | Not applicable |
| Inngest | A function, not a run | Runs stop after their active step | No new run starts from new events. Those events do not run after resume unless the user replays them. |
| Argo Workflows | One workflow (`argo suspend`) | Pods finish | No new node starts until `argo resume` |

Temporal's workflow pause is the closest model. It pauses one run, holds
new work and retries, and shows `Paused` as a status. We follow it, with
three differences. Our Cancel works at once on a paused run. A run whose
outcome is decided while paused moves on to its cleanup. And a step
timeout that fires during the pause does not fail the step (see
Decisions).

## Users and job

Primary user: the developer who operates workflows in production and
builds the operator's tools (an admin endpoint, a CLI, a button). When a
tradeoff comes up, we optimize for this job: "stop new work on this run
now, without losing the run, and continue it later."

Secondary user: a developer who debugs a run in a test or with `local`.

## Concepts

- **Pause**: an operator holds a run. Steps that already run finish, and
  their results are recorded. Nothing new starts until Resume: no new
  steps and no retries.
- **Resume**: the operator releases the hold. The held steps start, and
  the run continues.
- **`paused`**: a new `RunStatus`. A run is `paused` only between a Pause
  and the next Resume, Cancel, or decided outcome. It is not terminal.
- **Held step**: a step attempt that the run dispatched but the host does
  not start, because the run is paused. On a durable host it is a job row
  that no worker claims. On `local` it is a goroutine that waits.

Pause and Resume sit next to Cancel. Unlike Cancel, they do not decide the
outcome.

## Use cases

The examples use this definition.

```yaml
name: order

steps:
  - name: charge
    action: payments.charge
    timeout: 1m
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
finishes, and its result is recorded. No worker starts a new step of the
run.

Acceptance:
- [ ] `res.Applied` is true and `res.State.Status` is `paused`.
- [ ] `res.State.Reason` is `"payment API down"`.
- [ ] The step in flight is not cancelled. Its handler's context stays
      live, and its result is recorded as usual.
- [ ] A `charge` retry that waits for its backoff when the Pause commits
      does not start. A test waits past the backoff and sees no handler
      call, through `Run`, `RunOnce` and `Execute`.
- [ ] `Execute` of a job of a paused run returns `ErrNotClaimable`, with
      a message that says the run is paused.
- [ ] `QueueStats` does not count jobs of a paused run as due.
- [ ] `GetRun` shows status `paused`. The event log has a `pause` event
      with the reason.
- [ ] Pause of a run that is not `running` changes nothing: `Applied` is
      false, and the status and reason stay as they were.
- [ ] Pause of a run that does not exist returns `ErrRunNotFound`, as
      Cancel does.

### UC-2: Resume the run

```go
res, err := store.Resume(ctx, "order-123")
```

The held steps start. The run continues as if it had not paused.

Acceptance:
- [ ] `res.Applied` is true and the status is `running`.
- [ ] `res.State.Reason` is empty.
- [ ] Workers wake at once and claim the held jobs: postgres through its
      notify channel, sqlite through its in-process wake-up.
- [ ] A held retry keeps its attempt number and its backoff time. If the
      backoff ended during the pause, the retry runs now.
- [ ] An attempt whose timeout fired during the pause gets a new, full
      timeout that counts from the Resume.
- [ ] Resume of a run that is not `paused` changes nothing: `Applied` is
      false.
- [ ] A run that paused and resumed ends with the same outcome and
      outputs as a run that got the same step results without a pause.

### UC-3: Cancel a paused run

```go
res, err := store.Cancel(ctx, "order-123", "give up")
```

Acceptance:
- [ ] The status becomes `cancelling`, then `cancelled` when `release`
      finishes. The run is not `paused` after the cancel.
- [ ] Held steps never start. Their attempts are recorded as cancelled,
      as for any attempt that a cancel stops.
- [ ] `State.Reason` is `"give up"`.

### UC-4: Time passes while the run is paused

A `charge` attempt fails while the run is paused. Or `wait_a_bit` ends.
Or a `charge` attempt runs past its `timeout`.

Acceptance:
- [ ] The failed attempt is recorded. Its retry is held.
- [ ] The sleep settles when its timer fires. `ship` is held.
- [ ] A step's `timeout` that fires during a pause does not fail the
      attempt, also when the host held it. A test pauses past the step
      timeout with a retry pending: the handler count does not change,
      no attempt is used up, and the status stays `paused` until Resume.
- [ ] A pause shorter than the time left on a timeout does not change
      that timeout. The docs say so.
- [ ] After Resume, the held retry and `ship` start.
- [ ] When the outcome is decided while the run is paused, the pause
      ends. For example: the last `charge` attempt fails, so the run
      fails. The status moves to `failing`, not `paused`, and the
      `on_failure` and `finally` steps start with no Resume. A test covers
      it, and the operator docs say so.

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
- [ ] A handler that a paused run dispatched is not called until
      `run.Resume()`. A cancel ends the wait, and the handler is never
      called.
- [ ] A paused run saved with `Save` and continued with `Runner.Resume`
      stays paused. No handler is called until `run.Resume()`.
- [ ] The doc comments of `Run.Resume` and `Runner.Resume` each say what
      the other does.
- [ ] An `Example` test shows pause and resume, and its output proves
      that no handler ran while the run was paused.

## Requirements

- R-1: Two engine events, `EventPause` (with `Reason`) and `EventResume`,
  sit next to `EventCancel`.
- R-2: Pause moves a `running` run to `paused` and sets `State.Reason`.
  Resume moves a `paused` run to `running` and clears `State.Reason`.
  Neither emits a command. For any other status, each returns the state
  unchanged with no commands.
- R-3: While paused, Apply does everything it does for a running run: it
  records results, settles sleeps, moves the run through the graph, and
  emits dispatches. The one difference is a step timeout.
- R-3a: A step timeout that fires while the run is paused does not fail
  the attempt. Apply records the attempt in State. Resume emits a new
  timeout timer for each recorded attempt that still runs, a full
  `timeout` from the Resume time.
- R-4: When the run's outcome is decided while it is paused, the pause
  ends. The run moves to `completing`, `failing` or a terminal status, and
  its `on_failure` and `finally` steps run. `State.Reason` is cleared,
  unless the run was cancelled.
- R-5: The host rule, in the `documentation/hosts.md` contract: a host
  does not start a dispatched step of a paused run. It still fires timers.
  Resume makes the held steps startable again.
- R-6: Durable hosts (postgres and sqlite): the claim (`Claim` and
  `ClaimKey`) skips step jobs whose run is `paused`. Timer jobs are
  claimed as usual. `QueueStats` does not count the skipped jobs.
  `Execute` reports `ErrNotClaimable` for such a job. A Resume wakes the
  workers.
- R-7: `local`: before a handler call, after the `NotBefore` wait, the
  driver waits while the run is paused. Resume or a cancel of the attempt
  ends the wait.
- R-8: Host API, matching Cancel exactly:
  - postgres: `Store.Pause(ctx, runID, reason)`, `Store.Resume(ctx,
    runID)`, and the `Tx` and `SQLTx` variants that Cancel has.
  - sqlite: `Store.Pause`, `Store.Resume`, and the `SQLTx` variants.
  - local: `Run.Pause(reason)` and `Run.Resume()`.
  Each one delivers the event, as Cancel does. `internal/parity` checks
  that postgres and sqlite share the new methods.
- R-9: `hostcore.Storable` cleans NUL characters from a pause reason, as it
  does for a cancel reason.
- R-10: Docs: `documentation/hosts.md` ("Cancel and Abort" covers pause,
  plus the status list and the host rule), `llms.txt`, README and
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
- Stop sleeps and retry backoffs during a pause, or move timers that
  have not fired. See Decisions.
- Pause during `completing`, `failing` or `cancelling`. Cleanup runs to
  the end, as it does for a second cancel.

## Decisions

- **Paused is a `RunStatus`, `paused`.** One field tells the operator
  what the run does. Run listing can filter on it with no new column.
  Temporal's workflow pause works the same way. Rejected: a `Paused bool`
  on State next to `running`. Then two fields describe one fact, and
  every status filter must check both.

- **The host holds the work. The engine records the status.** A run
  has dispatched work before the Pause arrives: retries that wait for a
  backoff, and steps that wait in a queue. Only the host decides when
  that work starts, so the hold must be there anyway. With the hold in
  the host, the engine needs no held-dispatch list, no release logic and
  no resume order. Apply runs as for a running run, and the event
  sequences without a pause give the same results as today. Rejected:
  hold new dispatches in the engine. It misses the work that was
  dispatched before the Pause, and it needs a second hold in the host.

- **A timeout that fires during a pause is re-armed at Resume.** A step
  timeout that fires while the run is paused does not fail the attempt.
  Apply records it, and Resume gives the attempt a new, full timeout from
  the Resume time. Without this, a pause longer than a timeout uses up
  the retries of a held step and fails the run while it is paused (PR
  #94, B1). This stays in the engine and is deterministic. Timer jobs are
  still claimed during a pause, so no host changes for timers. Sleeps and
  retry backoffs still run: if a backoff ends during the pause, the retry
  runs at Resume. A timer that has not fired keeps its deadline: the
  engine cannot tell an old timer from a new one.
  Costs: a handler that hangs during a pause gets its timeout later. A
  pause shorter than the time left on a timeout does not change it, and
  the cut-off is sharp: a 60s timeout dispatched at t=0 gives 1s after a
  Resume at 59s, but a full 60s after a Resume at 61s.
  Rejected: count timeouts from when the handler starts. That changes
  what `timeout` means for every run, and each host must edit timers at
  claim time. Rejected: move every timer by the pause length. That needs
  the pause start in State, and it changes jobs that the host already
  wrote.

- **A decided outcome ends the pause.** If the last path ends, or a step
  fails for good while paused, the run moves on to `completing` or
  `failing`, and its `on_failure` and `finally` steps run. A pause holds
  new forward work, not cleanup. This keeps `paused` reachable only from
  `running`. The cost: cleanup steps can call the API that the operator
  paused for. The docs say so. Rejected: hold cleanup until Resume, as
  Temporal does. That needs a paused form of each finishing status, or a
  second field.

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
  releases a pause. The doc comment of each names the other. One word for
  unpause, the same on every host. Rejected: `Run.Unpause`. It adds a
  second word for one idea across the hosts.

- **Resume's `Result.Jobs()` lists the re-armed timeout timers and the
  run's held step jobs.** (Revised after the #104 review, S1.) The held
  jobs are already in the outbox, and workers that `Run` wake at once. A
  host that pushes one request per job (Cloud Tasks) sends the listed
  jobs again, so a held step starts at Resume, not at the queue's next
  retry, whose backoff can be long. A job pushed twice is claimed once.
  The list comes from `Outstanding` on the resumed state, so no new query
  is needed. Rejected: return no held jobs and rely on the queue's retry
  and the scheduled `RunOnce`. Then Resume does not resume on push hosts
  until the queue retries.

## Success

- An operator holds a run with one call on each host and continues it
  with one call.
- No step handler starts for a paused run on any host, after the Pause
  commits. A test proves it with a retry that waits for its backoff.
- Nothing gets worse for runs that never pause: `property_test.go` and
  the soundness fuzz stay green, the engine gives the same result for
  every event sequence without a Pause, and the claim query keeps its
  index plan.

## Risks and open questions

- **A claim that races the Pause.** A worker can claim a job just before
  the Pause commits and start it just after. The guarantee is: no claim
  that starts after the Pause commits takes a step job of the run. The
  docs say so. A re-check before the handler call would narrow the window
  but not close it. Does not block.
- **Claim cost with many held jobs.** The claim reads past held jobs that
  are due, because they stay at the front of their queue. A few paused
  runs cost a few index reads per claim. Thousands of held jobs would
  slow every claim of that queue. The tech spec has a fallback if this
  shows up. Does not block.
- **Status checks that mean "progressing".** Apply checks
  `Status == running` in several places. A paused run must pass each of
  them, or joins stop firing and failures are lost. The tech spec lists
  each check. Does not block.
- **Hosts that read `running`.** A host app that tests
  `Status == StatusRunning` to mean "not finished" sees `paused` as
  finished. `Terminal()` is the documented test, and the docs say so.
  Does not block.
- **wait-step overlap.** A `wait` step that gets its delivery while the run
  is paused records it, and the host holds its successors. Both tracks
  edit `apply.go` and `event.go`. The second PR to merge rebases. Does not
  block.
