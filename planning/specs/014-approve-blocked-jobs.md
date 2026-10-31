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
- The approval job's answer runs the step's job again, approved, or
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

- `approval` on a step that is not a host kind, on an `each` step, or on
  an `on_failure` or `finally` step.
- `errors` empty, or `timeout` missing or not positive.
- `approval` anywhere, when `kinds.Known(KindApproval)` is false:
  "approval needs the host to register kind \"approval\"".
- `approval` used as a step's kind key: reserved, like `decision`.

The definition hash covers `approval`, as any field.

### Its own unit

An approval job belongs to a visit of the step, as a decision job does.
The StepID grammar gains a second suffix:

```
<path>/<step>[@<visit>].approval#<attempt>     main/deploy.approval#1
```

No `[index]`: `approval` is not allowed on `each`. `parse` returns the
node key and a suffix kind (none, decision, approval) in place of the
`decision` flag. `Unit()` is `main/deploy.approval`, `Step()` is
`deploy`. `dropped`, `State.Unit`, `State.InFlight`, `rearmTimeouts` and
the event branch of `Apply` switch on the suffix, as they do for
decisions.

An approval job has one attempt. It is never retried.

### Shapes

```go
const KindApproval = "approval"
const ErrorTypeApprovalFailed = "approval_failed"

// Hold is what an approval job asks a person to approve.
type Hold struct {
	Kind    string     `json:"kind"`    // the held job's step kind
	Attempt int        `json:"attempt"` // the held attempt
	Error   *StepError `json:"error"`   // what blocked it
}

// Command gains
Hold     *Hold `json:"hold,omitempty"`     // an approval job
Approved bool  `json:"approved,omitempty"` // a person approved this visit's job
```

`Job` embeds `Command`, so a handler reads `job.Hold` and `job.Approved`.

`nodeState` gains, next to `Decision`:

```go
Approval *approvalState `json:"approval,omitempty"`

type approvalState struct {
	Unit     Unit `json:"unit"`               // the approval job: attempt 1, status, times, answer
	Hold     Hold `json:"hold"`
	Approved bool `json:"approved,omitempty"` // the answer was yes
}
```

The approval job's output schema is fixed:

```go
var approvalAnswer = &Schema{Type: "object", Required: []string{"approved"},
	Properties: {"approved": {Type: "boolean"}, "reason": {Type: "string"}}}
```

### Hold

In `unitFailedAttempt`, before the retry check:

```go
if a := s.Approval; a != nil && n.Approval == nil && a.matches(err.Type) {
	r.hold(n, i, err)
	return
}
```

`n.Approval == nil` means the visit was never held: an approved visit
never holds again, and a refused one has failed.

`hold`:

1. `cmd := dispatchCommand(n, i, u.Attempt+1, time.Time{})` evaluates
   `with` as the next attempt would. A `with` error fails the unit with
   it, as `dispatch` does.
2. Rewrite it into the approval job: StepID `makeApprovalID(n.Key, 1)`,
   `StepKind: KindApproval`, `Attempt: 1`, `Hold: {s.Kind, u.Attempt,
   err}`, `OutputSchema: approvalAnswer`. Params and Config stay the
   step's, so the person sees what will run.
3. The unit stays `Running` with its attempt unchanged, and
   `u.Error = err`, so the node stays open. `n.Approval = {Unit:
   {Attempt: 1, Status: Running, DispatchedAt: r.at}, Hold: ...}`.
4. Emit the dispatch, and a `ScheduleTimer` for the approval StepID,
   `StepKind: KindApproval`, at `r.at + a.Timeout`.

The held attempt (`deploy#1`) is settled from the host's point of view:
its event was applied and nothing more will arrive for it. The unit's
`Running` status only keeps the node open. A late or duplicate event for
`deploy#1` is ignored because `n.Approval != nil` marks the attempt as
held (checked in the event branch next to `attempt < u.Attempt`).

### Answer

Events for `<visit>.approval#1`:

| Event | Effect |
| --- | --- |
| StepCompleted, valid, `approved: true` | `n.Approval.Approved = true`, approval unit completed with the answer, `u.Error = nil`, `dispatch(n, i, time.Time{})` |
| StepCompleted, valid, `approved: false` | approval unit completed; `refuse(n, i, "rejected: " + reason)` |
| StepCompleted, invalid answer | approval unit failed `invalid_output`; `brokenApproval(n, i, ...)` |
| StepFailed (incl. an abandoned claim) | approval unit failed; `brokenApproval(n, i, msg)` |
| TimerFired, paused | `holdTimeout(id)`, as for any timeout |
| TimerFired | emit `CancelStep` (kind `approval`); approval unit failed `timeout`; `refuse(n, i, "not approved within 24h")` |

- `refuse` fails the unit with `&StepError{Type: hold.Error.Type,
  Message: hold.Error.Message + "; " + why}`, not retryable.
- `brokenApproval` fails it with `ErrorTypeApprovalFailed`, not
  retryable, message "approval job failed: ...".
- Both call `unitFailed` then `progress`, so `catch` routes as usual.
  Neither retries nor holds again.

`dispatchCommand` sets `Approved: n.Approval != nil &&
n.Approval.Approved` on every dispatch, so a retry of an approved job is
approved and a reclaim reads it from the job's stored command. Nothing
outside an approval job's answer can set it.

The answer is the approval unit's output, kept for the step records and
charged to the node like a decision's answers. It is never the step's
value: `n`'s value comes from the approved job.

### Retry budget

`retry` counts attempts since the approval:

```go
own := u.Attempt
if a := n.Approval; a != nil && a.Approved {
	own -= a.Hold.Attempt // attempts up to the blocked one
}
... own < rt.MaxAttempts ...
```

Before a hold, nothing changes. With `max_attempts: 3`, `#1` fails
retryably, `#2` is blocked and approved, and `#3`, `#4`, `#5` may run.

### Resolve

For a step with `approval`, a name resolves only to a running approval
job of the step's latest visit, else `ErrNotWaiting`. This keeps a
duplicate approval, delivered by name, from completing the approved job.
A full StepID is not touched by `Resolve`: a handler that hands off with
`ErrDeferred` on such a step delivers by StepID.

### Pause, Resume, Outstanding

- An approval job is a step job: the durable hosts hold it while paused.
- `rearmTimeouts`: an approval StepID re-arms with kind `approval` and
  `Approval.Timeout`.
- `Outstanding`: for a running approval unit, emit its dispatch
  (`approvalCommand`, shared with `hold`) and its timer at
  `DispatchedAt + Approval.Timeout`, or the re-armed deadline. It emits
  no dispatch for the held unit.

### Cancel and run end

`cancelUnits` also cancels a running approval unit, with a `CancelStep`
of kind `approval`, as it cancels a running decision. A late answer is a
late event: ignored by StepID, `ErrNotWaiting` by name.

### Hosts

No API change. The durable hosts claim approval jobs from queue
`step:approval` and record them as step records of kind `approval`.
`AttemptOutcome` reads the approval unit through `State.Unit`, which
learns the suffix. The held attempt (`deploy#1`) is recorded as failed
with the blocking error when the hold is applied: `AttemptOutcome`
returns `u.Error` for a running unit whose node has `Approval` and whose
attempt is the held one. The persist step deletes `deploy#1`'s jobs then,
since nothing more is wanted from it.

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
- hosts.md: approval jobs, delivering an answer, and `Resolve`'s rule.
- llms.txt, doc.go and README: one line each.

## Alternatives considered

- **The approval job as the next attempt of the unit.** Simpler grammar,
  but it shares the job's idempotency key (a child-run approval would
  collide with a child-run step), skews the retry budget and lets a name
  delivery reach the real job. Rejected in review.
- **`catch` to a `wait`, then a copy of the step with `approved_by`.** The
  copy's params can differ from what the person saw. Rejected.
- **A new `EventApprove`.** Every host would need a new method. The
  approval job's output carries the same answer through `Deliver`.
  Rejected.
- **No approval job, only a hold the app finds by polling.** Nothing
  tells the app to ask a person. A job, which Load requires a handler
  for, is at-least-once and works on every host. Rejected.
- **Retry a failed approval job.** It needs a policy and a deadline that
  survives attempts. `approval_failed` is safe and visible. Deferred.

## Tradeoffs and consequences

- One more StepID suffix and one more node field, following decisions.
  Both are omitted when unset, so old states load unchanged.
- A step with `approval` cannot take a deferred result by name.

## Test plan

Root package (`approval_test.go`, through `fakeHost`):

- Hold, approve, run: commands, StepIDs, `Approved` on the dispatch,
  params equal across the held job, the approval job and the approved
  job.
- Reject and timeout fail with the blocking type; handler error,
  abandoned claim and invalid answer fail with `approval_failed`; none
  retryable; `catch` routes each.
- Budget: UC-7 of the PRD, in numbers.
- A listed error after approval does not hold again.
- Resolve: by name before a hold, during it, after approval and after
  the run ends.
- A late event for the held attempt is ignored.
- Pause during a hold: the deadline fires, is held, and is re-armed by
  Resume; an answer during the pause dispatches the approved job.
- Cancel during a hold cancels the approval job; a late answer is
  ignored.
- `Outstanding` during a hold returns the approval job and its timer.
- Load: every rule above.
- `property_test.go` and the soundness fuzz gain an `approval` step.

Host suite (`internal/hosttest`, `list.go`): hold, approve and run on
both backends, with the three step records; a timeout ends the hold.
Local: one test in `local/`.

decisions: the guard runs an approved job without calling decide.

## Open questions

- Should `approval` default `errors` to `[guard_blocked]`? The engine
  would then name a type it does not own. Proposed: no.
