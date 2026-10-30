# Wait for a delivery with a built-in wait step

Status: Draft
PRD PR: (none yet)
Implementation PR: (none yet)
Updated: 2026-10-29

## Problem

A Go developer builds approval gates, human-in-the-loop agent steps and
webhook callbacks. Each one is the same shape: the run stops at a step
until something outside the run sends a value, then continues with that
value.

Today the developer writes this by hand:

1. Declare a host kind, such as `approval`, only to have a handler.
2. Write a handler that does nothing but return `ErrDeferred`.
3. Store the attempt's full StepID (`main/approve#1`) somewhere the
   webhook can find it, because `Deliver` accepts only a full StepID.
4. Deliver to that exact StepID later.

`postgres/examples/approval` is 157 lines and most of it is this
plumbing. The README lists "Signals" under "Not yet", so a reader who
looks for a way to wait on the outside world finds no answer in the
language.

If we do nothing, every application writes the same deferred kind and
the same StepID bookkeeping, and the most common external wait stays
invisible in the definition.

## Context

**Our code.** The engine already has every piece except a name for it.

- A deferred step stays in flight until a `StepCompleted` or `StepFailed`
  event arrives, or until its `timeout` timer fires (apply.go). Its
  output is checked against the step's `output` schema.
- `sleep` is a control kind with no handler. Apply emits one
  `ScheduleTimer` for it and no `DispatchStep`, so the durable worker
  never claims a step job for it. The durable store writes its step
  record from that timer command (hostcore/store.go).
- Local `Run.Deliver` and durable `Store.Deliver` pass an event to Apply.
  Apply accepts only a canonical StepID and returns `ErrUnknownStep` or
  `ErrInvalidEvent` otherwise.
- `State.InFlight` lists the StepIDs of every running attempt, so the
  state alone can find the one attempt of a step that waits.

**Comparable products.**

| | How the outside reaches the wait | Early delivery | No delivery in time |
| --- | --- | --- | --- |
| Temporal signals | Signal name plus workflow ID. A handler in workflow code stores it. | Buffered in history. | Workflow code decides (a timer races the condition). |
| Step Functions `.waitForTaskToken` | An opaque task token the task passes out. `SendTaskSuccess(token, output)`. | Not possible: no token yet. | `States.Timeout` error; `Catch` and `Retry` apply. |
| Inngest `step.waitForEvent` | An event name plus a match expression. | Ignored: only events after the wait count. | Returns null. |

We take the Step Functions model, without the token: a run ID and a step
name address the wait, so the caller stores nothing. We do not take
Temporal's buffered signals or Inngest's event matching. Both need a
store of events and a matcher, which the concepts rule forbids and the
use cases do not need.

## Users and job

Primary user: a Go developer who writes definitions and the webhook or
API handler that answers them. The job: "stop this run here until a
person or a system answers, then continue with the answer, and fail
cleanly if nobody answers in time."

Secondary user: an operator who reads a run's state and wants to see
what it waits for.

## Concepts

- **wait**: a built-in step kind with no handler. It finishes when
  someone delivers its result, or fails when its timeout passes. `sleep`
  waits for a time; `wait` waits for a delivery.
- **Deliver** (exists): the one way the outside world puts a result into
  a run. A delivery to a wait may name the step only (`approve`) instead
  of the full StepID.

No other new word. A wait's attempt is an ordinary step attempt: it has
a StepID, a step record and a value that later steps read by its name.

## Use cases

### UC-1: Declare an approval gate

The developer writes:

```yaml
steps:
  - name: build
    action: build
    next:
      - approve
      - notify

  - name: notify
    action: ask_release_manager

  - name: approve
    wait: true
    timeout: 72h
    output:
      type: object
      properties:
        ok:
          type: boolean
      required:
        - ok
    next:
      switch: approve.ok
      cases:
        true: deploy
      default: rejected

  - name: deploy
    action: deploy

  - name: rejected
    set: rejected
```

`notify` and `approve` start in the same Apply, so the wait is in place
before the notify handler runs.

Acceptance:
- [ ] `Load` accepts this definition with no host kind for `approve`.
- [ ] `approve.ok` types as `bool` in the typecheck, the same as a host
      step with that output schema.
- [ ] Entering `approve` emits one `ScheduleTimer` for its timeout and no
      `DispatchStep`. The durable worker claims no step job for it.
- [ ] `Load` rejects `wait` without `timeout`, `wait` with any value
      other than `true`, and `wait` with `with`, `each`, `concurrency` or
      `continue_on_error`. Each error names the step and the key.

### UC-2: A webhook delivers the answer (durable host)

```go
_, err := store.Deliver(ctx, runID, workflow.Event{
	Kind:   workflow.EventStepCompleted,
	StepID: "approve",
	Output: map[string]any{"ok": true},
})
```

Acceptance:
- [ ] The store resolves `approve` to the one waiting attempt, records
      the event and the step record under the full StepID, and the run
      continues to `deploy`.
- [ ] A full StepID (`main/approve#1`) still works.
- [ ] Output that fails the schema fails the step with type
      `invalid_output`, as for any step.
- [ ] A `StepFailed` delivery fails the step; `retry` and `catch` apply.
- [ ] The same flow works through `DeliverTx`, `DeliverSQLTx` and the
      sqlite store, because they share hostcore.

### UC-3: Nobody answers in time

Acceptance:
- [ ] At 72h the timer fires and the attempt fails with type `timeout`
      (retryable), as any step timeout does.
- [ ] A `catch` that lists the error type `timeout` routes the run, and `retry` waits
      again with a fresh timer and a new attempt.

### UC-4: A delivery the run is not waiting for

Acceptance:
- [ ] A bare name with no waiting attempt (not reached yet, or already
      settled) returns an error that matches `workflow.ErrNotWaiting`
      with `errors.Is`. The message names the step. The state does not
      change.
- [ ] A bare name with two or more attempts in flight (the step on two
      parallel paths) returns `ErrInvalidEvent`, and the message lists
      the StepIDs so the caller can pick one.
- [ ] A bare name that is not a step in the definition returns
      `ErrUnknownStep`.

### UC-5: The same flow in process

```go
err := run.Deliver(workflow.Event{
	Kind:   workflow.EventStepCompleted,
	StepID: "approve",
	Output: map[string]any{"ok": true},
})
```

Acceptance:
- [ ] Local `Run.Deliver` resolves the name inside its event loop and
      returns the same errors as UC-4.
- [ ] A new example under `examples/` (60 to 120 lines, YAML in a file)
      runs this approval gate with the local host.

## Requirements

- R-1: `wait: true` is a control kind, next to `join`, `set` and `sleep`.
  It takes `timeout` (required), `output`, `retry`, `catch`, `next`,
  `match`, `max_visits` and `description`.
- R-2: Entering a wait, and each retry of it, creates a running attempt
  and emits only the attempt's timeout `ScheduleTimer`, with
  `StepKind: "wait"`.
- R-3: A `StepCompleted` or `StepFailed` for a wait attempt settles it
  as it settles a host step: output normalized and schema-checked, size
  limits charged, retry and catch applied.
- R-4: `State.Resolve(id StepID) (StepID, error)` returns a canonical
  StepID unchanged, and a bare step name as the StepID of that step's
  one running attempt. It returns the errors of UC-4.
- R-5: Local `Run.Deliver` and hostcore `Store.Deliver` call
  `State.Resolve` on the event's StepID, on the state they hold or lock,
  before Apply. Apply itself still accepts only a canonical StepID.
- R-6: `Outstanding` rebuilds a running wait as its timeout timer only.
- R-7: The durable store writes a step record for a wait from its timer
  command, as it does for `sleep`.
- R-8: README drops the "Signals" line from "Not yet"; llms.txt drops
  the signals words from "Not yet supported". definitions.md, hosts.md,
  llms.txt, doc.go and kinds.go describe `wait` and name-only delivery.
- R-9: Engine tests cover UC-1 to UC-5. One suite test in
  `internal/hosttest` (listed in `list.go`) covers UC-2 to UC-4 on every
  backend. Property and soundness fuzz tests include wait steps and stay
  green.

## Not in scope

- Buffering a delivery that arrives before the wait. It needs an event
  store. The fork in UC-1 removes the race for the common case, and
  `ErrNotWaiting` tells a caller to try again.
- Event names, topics or match expressions. The step name is the
  address.
- `with` and `each` on a wait. No handler reads `with`. A wait per item
  makes every delivery need a full StepID; add it when someone asks.
- Telling the host that a wait started, beyond the timer command and the
  state. A step before or beside the wait sends the notification.
- Pause interplay. The pause-resume track owns it: a delivery to a wait
  in a paused run records the result, and successors start after Resume.

## Decisions

- **The syntax is `wait: true` with a required `timeout`.** A kind key
  needs a value, and `true` is the one that adds no meaning. Rejected
  `wait: 72h`: it reads as "pause 72 hours", which is `sleep`. Rejected
  `wait: {}`: block-style YAML cannot write it cleanly.
- **`timeout` is required.** Every wait then emits exactly one command,
  its timer, so the durable store records it and `Outstanding` rebuilds
  it with the rule `sleep` already uses. A wait with no deadline leaks a
  run forever when the answer never comes. Rejected an optional timeout:
  a wait with no command needs a new command kind or a host that scans
  the state for waits. A developer who wants "forever" writes a long
  timeout.
- **Name-only delivery is `State.Resolve`, called by the hosts, not by
  Apply.** The durable store keys the event log, step record and job
  deletion by `ev.StepID`, so it needs the full StepID before Apply
  anyway. Apply stays exact, and the event log keeps full StepIDs.
  Rejected: Apply resolves names (the store would still resolve first,
  so two places do one job). Rejected: a token the step hands out
  (Step Functions); the caller would have to store it, which is the
  boilerplate we remove.
- **Resolution works for any step, not only wait.** A deferred host step
  is addressed by name the same way; the rule is one sentence and needs
  no kind check. A `sleep` addressed by name still fails in Apply with
  `ErrInvalidEvent`, as today.
- **One new sentinel, `ErrNotWaiting`.** A caller must tell "not waiting
  now" (retry later, or a duplicate after success) from a malformed or
  unknown step. It is not in hostcore's `IsPermanent` list, because the
  worker never delivers a bare name. Ambiguity reuses `ErrInvalidEvent`:
  the caller sent too little, which that sentinel already means.
- **A timeout is a normal step timeout.** Type `timeout`, retryable, so
  `retry` and `catch` work with no new rule. Rejected Inngest's "returns
  null": every later step would have to test for null.
- **A wait creates no step job.** Its only row in the job table is its
  timeout timer, the same timer every step with `timeout` has.

## Success

- The approval gate needs no host kind, no handler and no stored StepID:
  the new example has none.
- A webhook handler needs only the run ID and the step name.
- Nothing gets worse: existing definitions, deferred steps and full
  StepID deliveries behave as before; property and soundness fuzz tests
  stay green; `make test-long` passes.

## Risks and open questions

- `wait` becomes a reserved key. A host that declared a kind named
  `wait` breaks at Load. The library is pre-1.0, and no example or test
  uses that name. The release notes say so.
- A delivery that races ahead of the wait gets `ErrNotWaiting`. The
  docs recommend the fork in UC-1 and a retry on `ErrNotWaiting`. If
  users hit the race often, early buffering is a later PRD.
- Pause-resume also edits apply.go. Both changes stay in their own
  functions; the second to merge rebases. Not blocking.
