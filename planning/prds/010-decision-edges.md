# Branch on a model's judgment in an edge condition

Status: Draft
PRD PR: (pending)  Implementation PR: (pending)  Updated: 2026-10-29
Issues: #86, depends on #88
Tech spec: [specs/010-decision-edges.md](../specs/010-decision-edges.md) (to come)

## Problem

A developer who writes agent workflows often branches on a judgment. Is
this ticket urgent? Is this refund legitimate? Which team owns this? Is
the draft done? The workflow cannot ask that question where it branches.
An edge condition is a pure expression, and a judgment needs a model.

So the developer adds a step that exists only to feed the next edge:

```yaml
- name: intake
  action: tickets.fetch
  next: is_urgent

- name: is_urgent              # a dummy step: one question, one answer
  action: ask_model
  with:
    question: Does this need a reply within an hour?
    text: '${{ intake.body }}'
  output:
    type: object
    properties:
      yes:
        type: number
  next:
    - step: page_oncall
      when: is_urgent.yes > 0.8
    - step: inbox
      when: is_urgent.yes <= 0.8
```

Each judgment costs a step, a host kind, a handler and an output schema.
The definition no longer reads as a record of what the run asks. A
multiple-choice judgment is worse. The dummy step's output is a string,
so `switch` cannot check that every answer has a case unless the author
also writes an enum schema by hand, and keeps it in step with the prompt.

Issue #86 asks for this. The pattern shows up in our own examples. In
`codefix`, a `judge` step exists to return `ship` or `revise`, with a
hand-written enum schema, so a `switch` can route on it. In `typed`, a
`classify` step does the same for `bug_fix`, `feature` or `other`. If we
do nothing, every agent workflow keeps
paying the dummy-step tax, and choice routing keeps losing its
exhaustiveness check.

## Context

**Our code.** `route` in `apply.go` evaluates a settled step's `switch`
and `when` edges with no I/O, then enters, forks or ends the path. A step
does its work as a dispatch: `DispatchStep` out, a completion event in,
with retry, timeout and idempotency by `StepID.Unit()`. `catch` routes a
step's failure. `typecheck.go` types every expression, and `checkSwitch`
rejects a missing or impossible case for an enum. Hosts map a step kind
to a handler.

**decide** (`../decide`) is our library for decision models. A decision
model answers a typed question instead of writing text: yes or no
(`noul`, the probability of yes), or one of fixed options (`choice`).
One request can hold several questions.

**Comparable systems:**

- **LangGraph** routes with a conditional-edge function, which can call
  a model. The checkpoint does not store the router's answer, so a resumed
  graph can ask again and route another way. The usual fix is a router
  node that writes the answer to state: our dummy step.
- **AWS Step Functions** has a `Choice` state that compares values only.
  A model call is a `Task` state whose output feeds the `Choice`. Two
  states per judgment, and no check that the cases cover the answers.
- **Temporal** puts a model call in an Activity. History records the
  result, and replay reads it instead of calling again. The branch is a
  plain `if`, so the judgment sits inline.

We want Temporal's property in a definition: the question sits at the
branch, the run records the answer, and a replay never asks again. And
we can add what none of the three has: a `switch` that Load checks
against the options.

## Users and job

**Primary user:** a developer who writes agent workflows (or an agent
that writes them). The job: branch on a judgment without a step that
exists only to feed the next edge.

**Secondary user:** the developer who runs the host. They register the
decision handler once, with the model client, and every definition can
then ask decisions.

## Concepts

- **Decision**: a question the run asks a decision model when it leaves
  a step, to choose the next step. The author writes it in a `when` or a
  `switch` as `decide(...)` or `choose(...)`. The host answers it. The
  run records the answer.
- **`decide(question, subject)`**: asks a yes-or-no question about the
  subject. Returns the probability of yes, a number from 0 to 1.
- **`choose(question, subject, options)`**: asks which of the options
  fits the subject. Returns one option. Its type is the enum of the
  options, so a `switch` on it must handle each one.
- **Decision handler**: the host's handler for the reserved `decision`
  kind. It receives every decision of one step's `next` block in one job,
  and returns the answers. The real one, built on decide, lives in the
  `decisions/` module (#88). Tests use a fake.

A decision is not a step. It has no name, takes no visit, and cannot be
read by later steps. It is part of leaving the step it sits on: that
step's `next` block waits for the answers, then routes.

## Use cases

The host in each use case registers the decision handler (UC-6).

### UC-1: Page on-call for urgent tickets

```yaml
- name: intake
  action: tickets.fetch
  match: first
  next:
    - step: page_oncall
      when: decide("Does this need a reply within an hour?", intake.body) > 0.8
    - step: inbox
```

`intake` completes. The run asks one decision. The answer is 0.93, so the
run enters `page_oncall`. On 0.40 it enters `inbox`.

Acceptance:
- [ ] The host receives one decision job with one question, after
      `intake` completes and before any next step starts.
- [ ] An answer of 0.93 routes to `page_oncall`; 0.40 routes to `inbox`.
- [ ] The run's state holds the question, the subject and the answer.
      Reloading that state and delivering more events never dispatches
      the decision again.

### UC-2: Route to a team, and let Load check the cases

```yaml
- name: intake
  action: tickets.fetch
  next:
    switch: choose("Which team owns this?", intake.body, ["billing", "engineering", "other"])
    cases:
      billing: billing_queue
      engineering: eng_queue
      other: triage
```

Acceptance:
- [ ] An answer of `"engineering"` routes to `eng_queue`.
- [ ] Remove the `other` case and Load fails with the existing message:
      `next: switch on choose(...) does not handle other; add cases or a default`.
- [ ] Add a case `sales` and Load fails with the existing message:
      `next: case "sales": choose(...) is never "sales" (it is one of billing, engineering, other)`.
- [ ] An answer that is not one of the options fails the attempt with
      `invalid_output`, and the step's `retry` applies.

### UC-3: Unsure goes to a human, for one ask

```yaml
- name: intake
  action: refunds.fetch
  match: first
  next:
    - step: approve
      when: decide("Is this refund legitimate?", intake.body) > 0.9
    - step: deny
      when: decide("Is this refund legitimate?", intake.body) < 0.2
    - step: human_review
```

Acceptance:
- [ ] The host receives one decision job with one question, not two.
- [ ] 0.95 routes to `approve`, 0.10 to `deny`, 0.55 to `human_review`.
- [ ] `examples/decide` runs this definition with a fake handler and no
      API key, and shows all three routes.

### UC-4: Revise a draft until it addresses every comment

```yaml
- name: draft
  agent: writer
  max_visits: 5
  next:
    - step: draft
      when: decide("Does the draft address every comment?", draft.text) < 0.9
    - step: publish
      when: decide("Does the draft address every comment?", draft.text) >= 0.9
```

Acceptance:
- [ ] Each visit of `draft` asks one decision about that visit's text.
- [ ] Answers 0.5, 0.7, 0.95 give three visits of `draft`, then `publish`.
- [ ] A decision does not count toward `max_visits`. Answers below 0.9
      on every visit fail the run on the sixth entry with type
      `max_visits`, as today.

### UC-5: A decision fails

The model provider is down. The decision handler returns an error on
every attempt.

```yaml
- name: intake
  action: tickets.fetch
  retry:
    max_attempts: 3
  catch: inbox
  match: first
  next:
    - step: page_oncall
      when: decide("Does this need a reply within an hour?", intake.body) > 0.8
    - step: inbox
```

Acceptance:
- [ ] The decision job is retried under `intake`'s `retry` policy, with
      its backoff, and each attempt is bounded by `intake`'s `timeout`.
- [ ] After the last attempt, `catch` on `intake` routes to `inbox`.
      `intake`'s output stays readable there, as it completed.
- [ ] Without `catch`, the run fails. Its `RunError` names `intake` and
      the decision's attempt, and carries the handler's error type and
      message.

### UC-6: The host registers decisions once

```go
kinds := workflow.KindMap{
    "action":   nil,
    "decision": nil, // definitions may now use decide and choose
}
runner := &local.Runner{
    Handlers: map[string]workflow.Handler{
        "action":   runAction,
        "decision": decisions.Handler(client), // from the decisions/ module
    },
}
```

Acceptance:
- [ ] With `"decision"` in the kinds, Load accepts `decide` and `choose`.
- [ ] Without it, Load fails: `next page_oncall: when: decide needs the
      decision kind; register a decision handler with the host`.
- [ ] A step written with the `decision` kind fails Load:
      `decision is reserved for decide and choose`.
- [ ] The same definition and handlers run the same way on the local,
      sqlite and postgres hosts.

## Requirements

**Language.**

- R-1: `decide(question, subject)` returns a number from 0 to 1, never
  nil. `choose(question, subject, options)` returns one of the options,
  typed as their enum, never nil.
- R-2: The question is a string literal. The options are a non-empty list
  of distinct string literals. Otherwise Load fails, naming the edge:
  `decide: the question must be a string literal` and
  `choose: the options must be a list of distinct string literals`.
- R-3: The subject is any expression of the step's edge scope. It cannot
  contain another decision call.
- R-4: Decision calls are allowed only in a `next` block's `when` and
  `switch`. Anywhere else (`each`, `with`, `set`, `outputs`, a list
  function body such as `map` or `filter`) Load fails:
  `decide can only be used in next: when or switch`.
- R-5: `decide` and `choose` are reserved names. A step cannot use them.

**Running.**

- R-6: When a step with decision calls settles, the run evaluates every
  subject in its `next` block, then dispatches one decision job. It holds
  each distinct question once. Two calls are the same when the function,
  question, subject and options are equal.
- R-7: Every call in the block is asked, whatever the rest of the
  expression does: `match: first`, `&&`, `||`, `if` and `try` do not skip
  one. So the questions never depend on the answers.
- R-8: The path waits at the step until the answers arrive, then routes
  with them. A join that names a later step treats the waiting path as
  one that can still arrive.
- R-9: The run saves the answers in its state. Routing reads only the
  saved answers. A replay or a reloaded run never asks again.
- R-10: The decision job uses the source step's `retry` and `timeout`.
  An answer that is the wrong type, out of 0 to 1, or not an option
  fails the attempt with `invalid_output`.
- R-11: A decision that fails after its attempts is a failure of the
  source step: its `catch` applies, else the run fails.
- R-12: Each visit of the step asks again. A decision does not count as
  a visit.
- R-13: A cancel stops a decision in flight like any step's work.

**Host.**

- R-14: A host enables decisions with one kind (`decision`) and one
  handler. No new command or event type. The job's params and the
  answers' shape are documented in `documentation/hosts.md`, so a test
  fake is a few lines.
- R-15: The root module keeps its two dependencies. The real handler
  lives in the `decisions/` module.

**Proof and docs.**

- R-16: The use cases pass in `internal/hosttest` (and `list.go`) on all
  hosts. `property_test.go` and `soundness_fuzz_test.go` generate
  decision edges.
- R-17: `documentation/definitions.md`, `documentation/hosts.md`,
  `llms.txt`, `doc.go` and the README row "Branch on a value" describe
  decisions.

## Not in scope

- `score(question, subject, lo, hi)`. Two functions cover the use cases.
  Add it when a user needs it.
- Plain-English conditions (`when: "the customer sounds angry"`). Sugar
  on this feature; later.
- A `decide` step kind. It returns answers as data, which a host kind
  can do today with no engine change.
- Model, provider or threshold settings in the definition. The host owns
  the model. Thresholds are literals in the expression.
- Batching decisions across steps, or across runs. One job per `next`
  block is enough to start.
- The probability of a `choose` answer. `switch` needs only the option.
- A UI.

## Decisions

- **The host enables decisions with a reserved `decision` kind in
  `Kinds`.** Hosts already tell Load what they run this way, and
  `Handlers` is already keyed by kind, so one name does both jobs. Load
  can then reject `decide` early on a host that cannot answer it.
  Rejected: a new `LoadOption` (a second place to configure the same
  fact); always allowing it (the run would fail at run time instead).
- **One rule for retry and timeout: the source step's.** The author
  already set how hard that step tries. Rejected: a `decision:` block on
  the step (new config surface for one case); retry only inside the
  handler (attempts would not show in the run, and a crash would lose
  them). decide's client still retries transport errors inside one
  attempt.
- **A failed decision is a failure of the source step for `catch`.**
  `catch` is how an author routes a failure today, and the decision
  belongs to that step. Rejected: a new error route on the edge, or a
  run failure that `catch` cannot see.
- **Ask every call, with no short-circuit.** The set of questions then
  depends only on the step's value, so one job holds them all and the
  same run always asks the same questions. Rejected: lazy evaluation
  (several round trips, and questions that depend on answers).
- **Dedupe on (function, question, subject, options).** UC-3 asks one
  question on two edges, and must cost one ask. Rejected: dedupe on the
  question alone (two subjects are two questions).
- **Literal question and options.** Load can type `choose`, the
  definition stays a readable record of what the run asks, and the dedupe
  key is stable. Rejected: computed questions.
- **No decision calls in list function bodies.** A body runs once per
  element, so the number of questions would depend on the data, and the
  subject on `it`. Rejected: allowing it and asking per element.
- **A decision is not a visit, and has no name.** It is how a step
  leaves, not a step. Rejected: counting it toward `max_visits`, which
  would halve a loop's bound for no reason.
- **The answers are not readable by later steps.** Keep one way to get
  data: a step's output. An author who needs the answer as data writes a
  step. Rejected: exposing them as `intake.decisions`, which adds a
  second data channel.
- **No exported fake in the root module.** The job shape is documented,
  so a fake is a few lines; `internal/hosttest` and `examples/decide`
  each have one. Rejected: an exported `workflow.FakeDecider`, public API
  that exists only for tests.
- **Name the functions `decide` and `choose`.** `decide` matches the
  library, and `choose` reads as English in a `switch`. Rejected:
  `noul` and `choice`, decide's wire names, which a workflow author does
  not know.
- **The decision's ID, its place in history and the job's exact shape
  go to the tech spec.** The PRD fixes what the user sees: the ID names
  the source step and its visit, differs from the step's own units, and
  keeps `StepID.Unit()` stable across attempts.

## Success

- UC-1 to UC-4 need no dummy step: each is one step plus its `next`.
- A `choose` switch with a missing case fails Load, every time.
- Replays and reloaded runs never call the handler again for an answered
  decision (asserted in `internal/hosttest`).
- Must not get worse: a definition without decisions routes exactly as
  today, with no extra command and no change to its state. Order
  independence and Load's type promise stay green.

## Risks and open questions

- **Eager asking costs money.** `x && decide(...)` asks even when `x` is
  false. We document it next to the functions. An author who wants to
  skip a costly ask puts the cheap check on an earlier step.
- **A path that waits at a step is new to joins.** If the engine shows it
  as settled too early, a join can fire without it. The tech spec must
  show how `canStillArrive` sees a waiting path, and the property test
  must generate joins after decision edges.
- **The guard module (#88) is not merged.** The engine and the fake do
  not need it. The real handler stacks on its branch if it has not
  merged in time. Not blocking.
- **A subject that fails to evaluate fails the run**, even inside `try`,
  because every subject is evaluated before routing (R-7). Accepted for
  the first version; the error message names the edge.
