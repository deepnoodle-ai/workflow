# Approve a job that the guard blocked

Status: Draft
PRD PR: TBD  Implementation PR: TBD  Updated: 2026-10-31
Issue: #98. Builds on the guard (PRD 012, #97) and the wait step (PRD 010, #100).
Tech spec: [specs/014-approve-blocked-jobs.md](../specs/014-approve-blocked-jobs.md)

## Problem

`decisions.Guard` stops a flagged job before it runs. The step fails with
a non-retryable `guard_blocked` error, and a definition can `catch` it.
That is the right default, but many flagged jobs are fine. A deploy is
flagged because it publishes. A `git push --force` to a feature branch is
flagged because it rewrites history. The operator wants a person to look
at the job and say "run it".

Today there is no safe way to do that:

- A `catch` can route to a `wait` step where a person answers. But after
  "yes", the definition has nothing that runs the blocked job. A copy of
  the step runs a second job, whose params the person never saw, and the
  guard blocks it again.
- A handler can keep its own "approved" list. Two reviews of #91 and #97
  found that this state races a retry, a reclaim, the step's timeout and a
  cancel. Each fix found a new race.

If we do nothing, an operator who turns on the guard must choose between
failing every flagged job and turning the guard off for a step kind. Both
make the guard less useful for the agent work it exists to protect.

## Context

**Our code.** A failed attempt goes through `unitFailedAttempt` in
`apply.go`: retry if the step's `retry` matches, else fail the unit and
let `catch` route it. Every attempt has its own StepID; `StepID.Unit()`
is the same for every attempt and is the idempotency key. A `wait` step's
attempt is in flight with no handler until a delivery or its timeout
settles it, and a host resolves a step name to that attempt
(`workflow.Resolve`). Decision edges (#103) showed how the engine
dispatches a job of a built-in kind (`decision`) that the host must
register (Load fails without it), with a fixed policy and an output
schema the engine checks. Apply runs one event at a time per run, so an
approval and a timeout or cancel can never both win.

**Comparable products.**

- *Claude Code and similar agents* ask the user before a risky tool call.
  The same call runs once the user says yes. Nothing is copied or
  re-planned.
- *GitHub Actions environments* hold a job that needs a reviewer. The job
  waits with its inputs fixed, reviewers approve or reject it, and a
  timeout rejects it. Approval runs that job, not a new one.
- *Temporal and Inngest* leave it to user code: a signal or
  `waitForEvent` that the workflow checks before the activity runs.

The shared pattern: the held job keeps its inputs, a person approves that
exact job, and approval lets it run once, as if it had passed the check.

**The pain is real.** Issue #98 came out of the guard's review: two
designs that kept approval in handler state failed review on races. The
guard's docs today tell operators to route a blocked job "to a step
where a person reviews it", and then they have no way to run it.

## Users and job

Primary user: a Go developer who operates a host that runs agent
workflows with the guard. Job: when the guard blocks a job, ask a person,
and run that same job if they approve, with no state of their own.

Secondary user: a definition author. Job: say which steps may be
approved and how long a person has, in the definition.

## Concepts

- **Hold**: what happens to a blocked job of a step that declares
  `approval`. The job does not run and does not fail yet. The step waits
  for a person, like a `wait` step.
- **Approval job**: the job the engine dispatches for a hold, of the
  built-in kind `approval`. Its handler, which the host registers, asks a
  person. The answer is the approval job's result.
- **Approved job**: the held job, run again after a person approved it.
  Its `Job.Approved` is true, and the guard runs it without a check.

## Use cases

### UC-1: A person approves a blocked deploy

The author lets a person approve `deploy` when something blocks it:

```yaml
- name: deploy
  action: deploy
  with:
    target: ${{ plan.target }}
  approval:
    errors: [guard_blocked]
    timeout: 24h
  catch:
    - errors: [guard_blocked]
      next: report
```

The operator registers a handler for approval jobs. It posts the job to
a review queue and returns `ErrDeferred`:

```go
handlers[workflow.KindApproval] = func(ctx context.Context, job workflow.Job) (any, error) {
    // job.Hold.Error: "command-risk flagged publish (yes 92%)"
    // job.Params: {"target": "prod"}, the held job's params
    if err := reviews.Post(ctx, job.RunID, job.StepID, job.Hold, job.Params); err != nil {
        return nil, err
    }
    return nil, workflow.ErrDeferred
}
```

A person reads the review and approves. The app delivers the answer to
the approval job's StepID:

```go
store.Deliver(ctx, runID, workflow.Event{
    Kind:   workflow.EventStepCompleted,
    StepID: approvalStepID, // main/deploy#2
    Output: map[string]any{"approved": true, "reason": "release 4.2, ok by Ana"},
})
```

The engine dispatches `deploy` again with the same params and
`Job.Approved` true. The guard runs it without asking the model. The run
goes on with the deploy's output.

Acceptance:
- [ ] The guarded handler is called twice: the guard blocks the first
      call and runs the deploy on the second, approved one. The deploy
      itself runs once. The approval job's handler is called once.
- [ ] The approved job's `Params`, `Item` and `Config` equal the held
      job's and the approval job's.
- [ ] The step records show three attempts: `deploy#1` failed with
      `guard_blocked`, `deploy#2` (kind `approval`) completed with the
      answer, `deploy#3` completed with the deploy's output.

### UC-2: A person rejects it

The app delivers `{"approved": false, "reason": "wrong target"}`.

Acceptance:
- [ ] The deploy does not run again.
- [ ] The step fails with the blocking error's type, `guard_blocked`,
      not retryable, so the same `catch` routes it. The message adds
      "rejected: wrong target".

### UC-3: No one answers in time

24 hours pass with no answer.

Acceptance:
- [ ] The step fails with `guard_blocked`, not retryable. The message
      adds "not approved within 24h".
- [ ] A late approval is refused (`ErrNotWaiting` by name) or ignored
      (by StepID), as for a `wait` that timed out. The deploy does not
      run.

### UC-4: A cancel, a pause or a restart during review

Acceptance:
- [ ] A cancel cancels the approval job; a later approval runs nothing.
- [ ] A pause holds the approval job like any step job. The review
      deadline is re-armed in full at Resume, as a step timeout is. An
      approval delivered while paused is recorded, and the approved job
      runs after Resume.
- [ ] A host that restarts, reclaims or retries keeps the hold and the
      approval: both are in the run's state and the job's command.

### UC-5: The approved job fails and retries

The approved deploy fails with a retryable error, and the step has
`retry: 3`.

Acceptance:
- [ ] The retry is approved too: the guard does not check it again.
- [ ] The hold does not use a retry attempt: a step with `retry: 3` gets
      three runs of its own handler, not counting the approval job.

### UC-6: An agent cannot approve its own job

An agent writes a definition with `approval:` and a step whose params
set `approved: true`.

Acceptance:
- [ ] `Job.Approved` is false: only an approval job's answer sets it. A
      definition has no way to write it.

### UC-7: The host has no approval handler

Acceptance:
- [ ] Load fails a definition with `approval:` when the host's kinds do
      not include `approval`, as it does for decision edges. A hold never
      waits for a handler that no worker has.

## Requirements

- R-1: A host-kind step may declare `approval: {errors, timeout}`.
  `errors` is a non-empty list of error types; `timeout` is a required
  duration. Load rejects it on `wait`, control kinds, `on_failure` and
  `finally` steps.
- R-2: Load rejects a definition with `approval` when `approval` is not a
  known kind, and rejects `approval` as a step's kind key.
- R-3: A failed attempt whose error type is in `approval.errors` holds
  the unit, retryable or not, unless the unit's job is already approved.
  The hold is checked before `retry`.
- R-4: A hold dispatches the next attempt as an approval job: kind
  `approval`, the held job's step, params, config and item, and `Hold`:
  the held job's kind and the error that blocked it. Its output schema is
  `{approved: boolean (required), reason: string}`. A timer bounds it at
  `approval.timeout`.
- R-5: `{approved: true}` dispatches the next attempt as the step's own
  job, with `Approved` true. Every later attempt of the unit is approved.
- R-6: `{approved: false}`, the deadline, or a failure of the approval
  job (an error, an invalid answer) fails the unit with the blocking
  error's type, not retryable, and a message that says why. It does not
  retry.
- R-7: Retry counts only attempts of the step's own job.
- R-8: `decisions.Guard` calls next without a check when `job.Approved`.
- R-9: A hold changes nothing for steps without `approval`: the blocked
  attempt fails as today.
- R-10: The local, postgres and sqlite hosts run approval jobs with no
  host API change. The durable step records show the approval attempt
  with kind `approval`, its params and its answer.
- R-11: Outstanding, Pause and Resume treat an approval job as a step job
  and its deadline as a step timeout.
- R-12: Docs: definitions.md (approval), hosts.md (approval jobs),
  llms.txt, the decisions package doc, and one example.

## Not in scope

- Who may approve. The app checks the person before it delivers.
- A record of who approved beyond `reason`, which the step records keep.
- Approving any failure in place, outside a step's `approval.errors`.
- Editing the job before approval. A person who wants other params
  rejects it and the definition re-plans.
- Retrying a failed approval job. It fails closed; see Decisions.

## Decisions

- **Hold in place, not a copy.** Approval runs the same unit, with its
  params evaluated from the same scope. A copied step could run params
  the person never saw.
- **The answer is the approval job's output.** No new event kind and no
  new host API: `Deliver` and `ErrDeferred` already carry it, by StepID or
  step name, on every host.
- **A built-in kind, registered like `decision`.** The engine cannot ask
  a person, and Load makes sure some handler will.
- **Fail as blocked.** A rejection, a timeout and a broken approval job
  all fail with the blocking error, so the author's existing `catch` for
  `guard_blocked` keeps working. The message tells them apart.
- **Fail closed when the approval job fails.** A person was never asked,
  so the job does not run. An approval handler should hand off fast and
  return `ErrDeferred`; it can retry its own notification first.
- **The engine does not know about the guard.** `approval.errors` names
  the types, so any handler that blocks with its own type can use it.

## Success

- The guard's docs show a complete approve flow in under 30 lines of
  host code and 8 lines of YAML.
- No handler in the example keeps state.
- `property_test.go` and the fuzz tests stay green with approval steps.

## Risks and open questions

- **Same params on approval.** R-5 relies on a path's scope not changing
  while its step is in flight. Retries already rely on it. A test checks
  it (UC-1).
- **Deliver by name.** `deploy` resolves to whatever attempt is in
  flight: before a hold, that is the job itself. The docs say to deliver
  to the approval job's StepID.
- **One more built-in kind.** `approval` joins `decision`. A host that
  routes kinds to services must route this one to the service that talks
  to reviewers.
