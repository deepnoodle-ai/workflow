# Approval: hold a blocked job for a person, then run it

Status: Draft
PRD: [prds/014-approve-blocked-jobs.md](../prds/014-approve-blocked-jobs.md)
Issue: #98

## Context

The guard (#97) fails a flagged job with a non-retryable
`guard_blocked`. The PRD asks for a way to hold that job, ask a person,
and run the same job once they approve, with all state in the run.

The pieces exist. A `wait` attempt is in flight with no handler until a
delivery or its timer settles it. A decision job (#103) is a built-in
kind that the engine dispatches and the host must register. Retries
re-evaluate `with` from the path's scope, which does not change while the
step is in flight. Apply serializes every event of a run.

## Goals

- A step with `approval` holds an attempt that fails with a listed error
  type, and dispatches an approval job in its place.
- The approval job's answer runs the unit's job again, approved, or
  fails the unit with the blocking error.
- No host API change. No new event kind.

## Non-goals

See the PRD's "Not in scope". In short: authorization, edits to the job,
retrying approval jobs.

## Proposal

### Definition

```yaml
approval:
  errors: [guard_blocked]   # required, non-empty
  timeout: 24h              # required
```

```go
// Step
Approval *Approval `json:"approval,omitempty"`

type Approval struct {
	Errors  []string `json:"errors"`
	Timeout Duration `json:"timeout"`
}
```

Load errors (validate.go):

- `approval` on a step that is not a host kind, or on an `on_failure` or
  `finally` step: "approval needs a host kind" / "... is not allowed on
  on_failure and finally steps".
- `errors` empty, or `timeout` missing or not positive.
- `approval` anywhere, when `kinds.Known(KindApproval)` is false:
  "approval needs the host to register kind \"approval\"".
- `approval` used as a step's kind key: reserved, like `decision`.

The definition hash covers `approval`, as any field.

### Kinds and shapes

```go
// KindApproval is the step kind of approval jobs.
const KindApproval = "approval"

// Hold is what an approval job asks a person to approve.
type Hold struct {
	Kind  string     `json:"kind"`  // the held job's step kind
	Error *StepError `json:"error"` // what blocked it
}

// Command gains
Hold     *Hold `json:"hold,omitempty"`     // an approval job
Approved bool  `json:"approved,omitempty"` // a person approved this unit's job
```

`Job` embeds `Command`, so a handler reads `job.Hold` and `job.Approved`.

`Unit` gains the same two fields. Only the engine writes them:

```go
Hold     *Hold `json:"hold,omitempty"`     // set while the attempt is an approval job
Approved bool  `json:"approved,omitempty"` // set from approval on; every later attempt carries it
```

The approval job's output schema is fixed:

```go
var approvalAnswer = &Schema{Type: "object", Required: []string{"approved"},
	Properties: {"approved": {Type: "boolean"}, "reason": {Type: "string"}}}
```

### Hold

In `unitFailedAttempt`, before the retry check:

```go
if a := s.Approval; a != nil && !u.Approved && u.Hold == nil && a.matches(err.Type) {
	r.hold(n, i, err)
	return
}
```

`hold`:

1. `cmd := dispatchCommand(n, i, u.Attempt+1, time.Time{})`. A `with`
   error fails the unit with it, as `dispatch` does.
2. `cmd.StepKind = KindApproval`, `cmd.Hold = &Hold{s.Kind, err}`,
   `cmd.OutputSchema = approvalAnswer`. Params, Config and Item stay the
   held job's, so the person sees what will run.
3. `u.Attempt++`, `Running`, `DispatchedAt = r.at`, zero `NotBefore`,
   `u.Hold = cmd.Hold`, `u.Error = err` (so the host records the held
   attempt as failed, as it does a retried one).
4. Emit the dispatch and a `ScheduleTimer` for the same StepID,
   `StepKind: KindApproval`, at `r.at + a.Timeout`.

The approval job's StepID is the next attempt: `main/deploy#2`. That
keeps `Resolve`, `InFlight`, `State.Unit` and the durable records as they
are; the records show the hold as an attempt of kind `approval`.

### Answer

The event branch for a unit gains one case before today's switch: when
`u.Hold != nil`, the event is about the approval job.

| Event | Effect |
| --- | --- |
| StepCompleted, valid, `approved: true` | `u.Hold = nil`, `u.Error = nil`, `u.Approved = true`, `dispatch(n, i, time.Time{})` |
| StepCompleted, valid, `approved: false` | `failHeld(n, i, "rejected: " + reason)` |
| StepCompleted, invalid answer | `failHeld(n, i, "invalid approval answer: ...")` |
| StepFailed | `failHeld(n, i, "approval job failed: " + msg)` |
| TimerFired, paused | `holdTimeout(id)`, as for a step timeout |
| TimerFired | emit `CancelStep` (kind `approval`), `failHeld(n, i, "not approved within 24h")` |

`failHeld` builds `&StepError{Type: hold.Error.Type, Message:
hold.Error.Message + "; " + why}`, not retryable, clears `u.Hold`, and
calls `unitFailed` then `progress`. It never retries and never holds
again, so `catch` sees the blocking type.

The guard at the top of the branch that rejects `TimerFired` for a step
with no `timeout` also accepts it when `u.Hold != nil`.

`dispatchCommand` sets `Approved: u.Approved` on every dispatch, so a
retry of an approved job is approved (UC-5), and a reclaim reads it from
the job's stored command.

The answer is not the step's value: `unitCompleted` is not called, and
nothing is charged to the state limit beyond the attempt's own fields.

### Retry budget

`retry` counts the step's own attempts. A unit holds at most once (an
approved unit never holds; a refused hold fails the unit), so:

```go
own := u.Attempt
if u.Approved {
	own-- // the approval job's attempt
}
... own < rt.MaxAttempts ...
```

### Pause, Resume, Outstanding

- An approval job is a step job: the durable hosts hold it while paused.
- `rearmTimeouts`: a unit with `Hold` re-arms with kind `approval` and
  `Approval.Timeout`.
- `Outstanding`: for a running unit with `Hold`, emit the approval
  dispatch (`holdCommand`, shared with `hold`) and its timer at
  `DispatchedAt + Approval.Timeout`, or the re-armed deadline.

### Cancel and run end

No change: a cancel emits `CancelStep` for every running attempt,
approval jobs included, and a late answer is a late event (ignored by
StepID; `ErrNotWaiting` by name once the step is settled).

### Hosts

No API change. The durable hosts claim approval jobs from queue
`step:approval` and record them as step records of kind `approval`.
`AttemptOutcome` needs no change: the held attempt reads as failed from
the next attempt's `Error`; the approval attempt reads as completed with
the answer, because approve clears `Error` and a refusal fails the unit.
A test covers both records on each backend.

The local host dispatches by kind, so it needs a handler for
`approval`, which Load already requires.

### decisions

```go
if job.Approved {
	return next(ctx, job) // a person approved this job
}
```

The package doc gains the approval flow. `ExampleGuard_approval` runs it
on the local host with `decidetest`.

### Docs

- definitions.md: an "Approving a blocked job" section.
- hosts.md: approval jobs, how to deliver an answer, and to deliver to
  the approval job's StepID, not the step name.
- llms.txt, doc.go and README: one line each.

## Alternatives considered

- **`catch` to a `wait`, then a copy of the step with `approved_by`.** The
  copy's params can differ from what the person saw. Binding approval to
  content needs a hash in state and a check in the guard. Rejected.
- **A new `EventApprove`.** Every host would need a new method, and an
  approval could target any running attempt. The approval job's output
  carries the same answer through `Deliver`. Rejected.
- **No approval job, only a hold that the app finds by polling.** Nothing
  tells the app to ask a person, and an app that misses the hold lets it
  time out. A job, which Load requires a handler for, is at-least-once
  and works on every host. Rejected.
- **Retry a failed approval job.** It needs a policy and a deadline that
  survives attempts. Fail closed is safe and simple. Deferred until a
  user asks.

## Tradeoffs and consequences

- The approval job shares the unit's attempt numbers. Retry must
  subtract it, and the step records interleave it with real attempts.
  That is also an audit trail: blocked, asked, ran.
- A hold uses `Hold`/`Approved` on `Unit`, two fields in every state;
  both are omitted when unset, so old states load unchanged.

## Test plan

Root package (`approval_test.go`, through `fakeHost`):

- Hold, approve, run: commands, StepIDs, `Approved` on the dispatch,
  params equal across the three attempts.
- Reject, timeout, approval job failure, invalid answer: each fails the
  unit with the blocking type, not retryable; `catch` routes it.
- An approved job that fails retryably is retried, approved, and the
  budget counts own attempts only.
- A listed error after approval does not hold again.
- Pause during a hold: the deadline fires, is held, and is re-armed by
  Resume; an answer during the pause dispatches, held by the host.
- Cancel during a hold cancels the approval job; a late answer is
  ignored.
- `Outstanding` during a hold returns the approval job and its timer.
- Load: every rule above.
- `property_test.go` and the soundness fuzz gain an `approval` step.

Host suite (`internal/hosttest`, `list.go`): hold, approve and run on
both backends, with the step records; a timeout ends the hold. Local: one
test in `local/`.

decisions: the guard runs an approved job without calling decide.

## Open questions

- Should `approval` default `errors` to `[guard_blocked]`? The engine
  would then name a type it does not own. Proposed: no.
