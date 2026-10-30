# Decision edges: `decide` and `choose` in `when` and `switch`

**Status:** Accepted
**Date:** 2026-10-29
**Workflow:** spec, then build in parallel. The PRD fixes the behaviour;
this spec fixes the shape. Review lands while the engine is built.
**PRD:** [prds/013-decision-edges.md](../prds/013-decision-edges.md).
Issues: #86, #93. The `decisions/` module: PRD PR #91 (issue #88).

## Context

The PRD lets an author write `decide(question, subject)` and
`choose(question, subject, options)` in a step's `next` block. `Apply` has
no I/O, so it cannot call a model while it routes. Today `route`
(`apply.go`) evaluates `switch` and `when` in one pass and moves the path.
This spec splits that pass in two when the block holds decisions: ask,
then route. The ask is an ordinary dispatch. Its answers are saved in
state, so a replay routes the same way and never asks again.

## Goals

- The PRD's use cases UC-1 to UC-7 pass on the local, sqlite and postgres
  hosts.
- No new command type, event type or `Handler` signature. A host adds one
  kind and one handler.
- A definition without decisions emits the same commands and the same
  state as before. Existing tests pass unchanged.
- Load's type promise holds for decision edges: the soundness fuzz test
  finds no value whose runtime type differs from its Load type.

## Non-goals

- `score`, plain-English conditions, a decide step kind, batching across
  steps (PRD "Not in scope").
- `catch` on control steps (#93).
- The real handler. It lands in the `decisions/` module after #91; the
  root has only the contract and test fakes.

## Proposal

### Overview

```
step settles ── route ──┬─ no decisions ─────────────────────► evaluate edges, move path
                        │
                        └─ decisions, none asked ─► ask: evaluate subjects,
                                                    save questions, dispatch
                                                    <node>.next#1 (kind "decision")
                                                              │
                 host runs the decision handler ◄─────────────┘
                              │
       StepCompleted <node>.next#N ─► validate, save answers ─► route again:
                                                               evaluate edges with
                                                               the saved answers
       StepFailed / TimerFired ─► retry (the fixed decision policy)
                                  └─ last attempt ─► failNode: catch, else failRun
```

The path stays `active` at the node from the step's completion until the
route. That is how it looks while a step runs, so joins need no change
(see "Joins").

### Load: finding the calls

Load must find every decision call before the run, with its literal
arguments, its subject as something it can evaluate, and where it sits.
The root cannot see an expression's syntax tree; expr can. So expr (our
library) gains two read-only methods:

```go
// Calls returns each call of the named functions, in source order,
// including calls nested in other calls.
func (p *Program) Calls(names ...string) []Call

type Call struct {
	Name string
	// Args holds each argument as its own program, compiled with the
	// parent's options.
	Args []*Program
	// Bound is true inside the body of an iterating form (map, filter,
	// ...), where it, index or a named element is bound.
	Bound bool
}

// Literal returns the program's value when it is a literal: a string,
// number, bool or nil, or a list or object of literals.
func (p *Program) Literal() (any, bool)
```

expr desugars a pipe before the walk, so `x | decide("q")` reports
`decide(x, "q")`, and the question check rejects it. This needs an expr
release (v1.4.0) before the root can use it (deepnoodle-ai/expr#32). The root
keeps its dependency count.

For each step, Load compiles its `next` block as today and then collects
`decisionCall` values for the block:

```go
type decisionCall struct {
	fn       string   // "decide" or "choose"
	question string
	options  []string // choose only
	subject  *program
}
```

`Step.decisions []decisionCall` holds them in source order: `switch`
first, then each `when` in edge order. Load does not merge calls. The run
merges them by ID (below), so two calls that ask the same question about
the same text are one question, however they are written.

### Load errors

Each error names the step and the place, as today
(`next <target>: when:` or `next: switch:`).

| Case | Message |
| --- | --- |
| decision call, host lacks the kind | `decide needs the decision kind; register a decision handler with the host` |
| question not a string literal, or empty | `decide: the question must be a string literal` |
| options not a non-empty list of distinct string literals | `choose: the options must be a list of distinct string literals` |
| wrong number of arguments | `decide takes (question, subject)` / `choose takes (question, subject, options)` |
| call in a subject | `decide: the subject cannot ask another decision` |
| call in an iterating body (`Bound`) | `decide cannot be used inside map, filter or another list function` |
| bare `decide` in `when` (type number) | `decide returns the probability of yes (0 to 1); compare it, for example decide(...) > 0.8` |
| call in `each`, `with`, `set`, `outputs`, `on_failure`, `finally` | `decide can only be used in next: when or switch` |
| subject reads a possibly-nil value without `?.` (a guard in the condition does not count) | `decide("q", ...): subject: <the may-be-nil problem>; every decision is asked before routing, so a guard in the condition does not protect its subject: use optional access (intake.body?.text) or pass the whole value` |
| step of kind `decision` | `kind "decision" is reserved for decide and choose; give the host's kind another name` |
| incomplete or impossible `switch` on `choose` | the existing `checkSwitch` messages |

`decide` and `choose` join `exprFunctions`, so `reservedName` rejects
them as step names with the existing message.

### Typecheck rules

`funcTypes` gains two entries. The typer already passes it to every
`Program.Check`, so the rules apply everywhere:

- In an edge scope (`when`, `switch`): `decide` returns `expr.Number()`,
  not optional. `choose` returns `expr.EnumOf(options...)`, not
  optional. It reads the options from its third argument's type: a list
  literal of strings types as a list whose element is the enum of those
  strings. If the type has no enum, the literal check above has already
  failed, and the result is `expr.Any`.
- In every other scope: both return the error "decide can only be used
  in next: when or switch". `FuncType` can return an error, so no new
  pass is needed.

Each subject is also typed alone, in the edge's scope. Every decision is
asked before routing, so a guard in the condition (`x != nil && decide("q",
x.body) > 0.5`) does not protect it: `ask` evaluates the subject without
it. Load rejects a subject that reads a possibly-nil value without `?.`,
names the edge, and suggests optional access or the whole value. Other
run-time failures (an index out of range, a failing function) stay
outside Load's promise and fail the run with `ErrorTypeExpression`.
Rejected: ask lazily, which would make the questions depend on answers
and break one job per block.

Runtime matches it. The runtime `decide` returns the saved `float64`,
which Apply checked is within 0 to 1. The runtime `choose` returns the
saved string, which Apply checked is one of the options. Neither returns
nil. The step's own value is unchanged on the `next` routes. On a `catch`
route it is `{error: {type, message}}`, which is how Load already types a
caught step, so a failed decision needs no new typing.

### Running: ask

`route(pathID, key)` gains a first stage when `len(s.decisions) > 0`:

1. `n.Decision == nil`: call `ask(n, p)` and return.
2. `n.Decision.Unit.Status` is not `completed`: return. The path waits.
3. Completed: evaluate `switch` and `when` as today, with a context
   that carries `n.Decision`'s answers.

`ask` evaluates every call's subject with `r.env(p, nil)`, then makes one
question per distinct ID:

- **Subject text.** A string is used as it is. Any other value is
  encoded with encoding/json v1 (map keys sorted), with HTML escaping off
  and no trailing newline. nil is `null`. A subject may be optional:
  `decide("q", intake.body?.text)` is allowed, and asks about `null`
  when the value is nil.
- **ID.** The first 16 bytes, in hex, of SHA-256 over the v1 JSON of
  `[fn, question, subjectText, options]`. Equal calls get equal IDs, so
  UC-3 asks once.
- A subject that fails to evaluate fails the run with
  `ErrorTypeExpression`, as a failing `when` does today.

`ask` saves the questions (without their subjects), charges their size
to the node, and dispatches attempt 1.

Each attempt re-evaluates the subjects, as `dispatchCommand` re-evaluates
`with`. The path's scope cannot change while it waits, and Apply is
deterministic, so the text and IDs are the same on every attempt and on
every replay.

### Synthetic unit ID

The StepID grammar gains one suffix:

```
<path>/<step>[@<visit>].next#<attempt>      main/intake.next#1
                                             main.tests/draft@3.next#2
```

Step names are identifiers, so `.` cannot appear in them, and the suffix
is unambiguous. A decision never has an `[index]`, even after an `each`
step: it belongs to the visit, not to an item.

- `StepID.Unit()` is `main/intake.next`, the same for every attempt. It
  is the idempotency key, as for any unit.
- `StepID.Step()` is `intake`, the step the decision leaves.
- `parse` returns the node key and a `decision` flag. `dropped`,
  `State.Unit`, `State.InFlight` and the event branch of `Apply` use the
  flag.

### Job shape

The decision is a `CommandDispatchStep`:

| Field | Value |
| --- | --- |
| `StepID` | `main/intake.next#1` |
| `Step` | `intake` |
| `StepKind` | `decision` (`workflow.KindDecision`) |
| `Index` | -1 |
| `Attempt`, `NotBefore` | as for any attempt |
| `Params` | the questions, below |
| `OutputSchema` | an object that requires each question ID |
| `Config` | nil |

```json
{
  "questions": [
    {
      "id": "3f2a9c...",
      "type": "decide",
      "question": "Is this refund legitimate?",
      "subject": "Order 4411 arrived broken. Photos attached."
    },
    {
      "id": "9b1c07...",
      "type": "choose",
      "question": "Which team owns this?",
      "subject": "Order 4411 arrived broken. Photos attached.",
      "options": ["billing", "engineering", "other"]
    }
  ]
}
```

Questions are sorted by ID. The handler returns an object with one
answer for each ID:

```json
{"3f2a9c...": 0.93, "9b1c07...": "billing"}
```

A `decide` answer is a number from 0 to 1. A `choose` answer is one of
the options. A missing ID, an extra ID or a wrong value fails the attempt
with `invalid_output`, marked retryable. Unlike a step's output, it is
retried: any decision failure uses one of the 3 attempts. The schema
covers the shape. Apply checks the 0-to-1 range, because `Schema` has no
`minimum` or `maximum`.

A `ScheduleTimer` with the same StepID bounds each attempt.

### State shape

`nodeState` gains one field:

```go
// Decision is the job that answers the decisions in the step's next
// block, once the step completed.
Decision *decisionState `json:"decision,omitempty"`

type decisionState struct {
	Unit    Unit                   `json:"unit"`    // attempt, status, times, error
	Answers map[string]*answerState `json:"answers"` // by question ID
}

type answerState struct {
	Type     string   `json:"type"` // decide or choose
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Value    any      `json:"value,omitempty"` // nil until answered
}
```

```json
"main/refund": {
  "key": "main/refund",
  "step": "refund",
  "status": "completed",
  "decision": {
    "unit": {"attempt": 1, "status": "completed"},
    "answers": {
      "3f2a9c...": {
        "type": "decide",
        "question": "Is this refund legitimate?",
        "value": 0.55
      }
    }
  }
}
```

There is no subject in state, only its hash in the ID (PRD R-9). The
answers are JSON-normalized on the way in. Their size is charged to the
node and counts toward `MaxStateBytes`. A state without `decision` loads
as before. `Unit.Output` holds the accepted answers by ID, as for any
unit, because the durable hosts record a settled attempt's output from
`State.Unit`. That is how the step records show the answers.

Note for #87 (replay a model upgrade): a replay does not need the
subject in state. It re-applies the run's events to the new definition.
Apply is deterministic, so it reaches each decision with the same state,
computes the same subject text and the same ID, and can ask the new model
the same question. The text is also in each decision job's params, which
the durable hosts keep in their step records. Both paths rely on one
fact: the subject depends only on the path's scope, which no later event
changes while the path waits. `TestDecisionReplayAsksTheSameQuestion`
(test plan) checks it: it re-applies a run's events to a fresh state and
requires the same decision job, subject text and ID.

### Retry, timeout: one fixed policy

Every decision uses the same policy. It never inherits the step's
`retry` or `timeout`: those are for the step's own work, which can take
30 minutes, and a decision is a short model call.

```go
var decisionRetry = &Retry{MaxAttempts: 3} // usual backoff: 1s, x2, cap 5m
const decisionTimeout = time.Minute
```

One rule: a decision gets 3 attempts; any failure uses one, retryable or
not, a malformed answer included; after the third, the step fails. A
model that misformats an answer once often gets it right the next time.

Decisions do not go through `unitFailedAttempt`, which retries only a
failure marked `Retryable` that the step's `retry` matches. They have
their own path in `decision.go`: `decisionCompleted` checks the answers,
and every failure (a handler error, a timeout, a bad answer, the state
limit) goes to `decisionFailedAttempt`, which dispatches the next attempt
until the third. Host steps keep today's rule.

The policy does not depend on the step's kind, so a control kind added
later needs no change. It is not configurable (PRD: no thresholds or
settings as config).

### Failure

After the last attempt, Apply calls `failNode(n, id, err)` with the
decision's StepID. `failNode` already does the rest:

- The node's status becomes `failed`, so `value(n)` is
  `{error: {type, message}}`.
- A matching `catch` sets the scope and enters the target.
- Otherwise `failRun`, with a `RunError` that names the step and the
  decision's StepID. A control step has no `catch`, so its run fails.

The node was `completed` until then, and `Scope` and `Settled` were set
on completion. `failNode`'s catch branch sets them again to the same
values. The units' outputs stay charged until the node is pruned; this
is accounting only.

### Cancel

`cancelAll` skips nodes that are not running. It gains one case: a node
with a running decision gets a `CancelStep` for the decision's StepID,
and the decision becomes `cancelled`. The node stays `completed`.
`failRun` calls `cancelAll`, so a failing sibling stops a decision in
flight too.

### Joins

`canStillArrive` counts every live path whose step can reach the join.
A path that waits for a decision is `active`, with `Step` set to the
source step and `Node` to its node, which is the same as while the step
ran. So the join waits for it, and arrival order stays irrelevant.
`canStillArrive` still only goes from true to false: the path leaves the
step only by `route` or `failNode`, as before. Nothing in the join code
changes. The property test must show this (see "Test plan").

### Hosts

- Hosts need no code to run decisions. The command is a dispatch, and
  `Handlers` is keyed by `StepKind`.
- `Outstanding` (`outstanding.go`) rebuilds a running decision's
  dispatch and timer from state, so `local` resumes a saved run that
  waits for a decision. For each node it checks `n.Decision.Unit`, not
  only `n.Units`, and builds the commands with the same code as the
  first dispatch, from the unit's attempt and `NotBefore`.
- One fix in `internal/hostcore`: `store.go` treats a `TimerFired` as a
  sleep's completion when the definition step is a sleep. A decision on a
  sleep step's `next` also has a timer, so the check must also see that
  the StepID is not a decision (its unit ends in `.next`).
- The durable hosts' step records show each decision attempt with kind
  `decision`, the questions in params and the answers as output. That is
  the run history the PRD asks for. `examples/replay` shows it unchanged.

### Docs

`definitions.md`: a "Decisions" section under Routing, the two functions
in the Functions table, the eager-asking rule, the subject rule, and the
fixed decision policy. `hosts.md`: the kind, the job and answer shapes, a fake
handler, and the rule from PRD R-15a: do not wrap the decision handler
in a Guard (PRD PR #91). `llms.txt` and `doc.go`: the same, briefly. README: the
"Branch on a value" row gains "or `decide` / `choose` for a judgment".

## Alternatives considered

- **New command and event types** (`AskDecision`, `DecisionAnswered`).
  Clearer in a log, but every host would need new storage, new job
  handling and its own retry and timeout. Dispatch already has all of
  it. Rejected.
- **A hidden step in the graph** after each step with decisions. It
  would reuse nodes fully. But a run's graph never changes, and a hidden
  step would show up in `computeLoops`, joins, visits and `max_visits`.
  Rejected.
- **Find the calls at run time with a recording function.** No expr
  change. But `&&`, `||`, `if` and `try` skip calls, so the run would not
  see every question until it had some answers. That breaks the
  one-job-per-block rule and makes the questions depend on answers.
  Rejected.
- **Parse expressions in the root with go/parser.** No expr release. But
  expr rewrites its source before parsing (`?.`, JSON literals, keyword
  fields), and the root would have to copy those rules and keep them in
  step. Rejected.
- **Store the subject text** for replay. Not needed (see the #87 note),
  and a loop would store a long text again on each visit. Rejected.

## Tradeoffs and consequences

- **Eager asks cost money.** `x && decide(...)` asks even when `x` is
  false. The docs say so next to the functions.
- **The root depends on a newer expr** with an API made for this
  feature. It is small and general (`Calls`, `Literal`), but it is a
  release in another repo first.
- **A completed node can have work in flight.** Readers of state and
  `InFlight` see a running decision on a completed node. That is new.
- **The StepID grammar grows.** Hosts that only use `Unit()`, `Step()`
  and `Attempt()` are unaffected. Code that parses StepIDs itself, as
  `hostcore` does for sleep timers, must learn the suffix.
- **Breaking for a few definitions and hosts.** A step named `decide` or
  `choose` now fails Load. A host kind named `decision` now fails Load
  where a step uses it. The release notes say so.
- **The subjects are evaluated on every attempt.** Cheap, and it keeps
  the subject out of state.

## Rollout

1. Release expr v1.4.0 with `Calls` and `Literal`. This needs the
   human's approval, like any tag.
2. `feat/decision-edges`, stacked on the PRD branch: the expr bump, the
   engine (`decision.go`, with the decision retry path; `apply.go`,
   which routes `.next` events to it and waits in `route`; `state.go`,
   `event.go`, `outstanding.go`, `abort.go`), the typecheck rules, the
   hostcore timer fix, the tests, the docs and `examples/decide` with a
   fake. One PR.
3. A separate small PR after #91 and step 2 land: the real handler in
   `decisions/`, built on decide's `NewRequest`, `Ask` and `SystemOne`,
   with one request per job. It reports a provider failure as a
   retryable `StepError` of type `decision_failed`.

State is backward compatible: the new field is optional. The definition
hash is unchanged for definitions without decisions.

## Test plan

**Root unit tests** (`decision_test.go`, on `fakeHost`, which
round-trips state through JSON after each Apply):

- UC-1 to UC-6 at the engine level: the dispatch's fields and params,
  one job for UC-3, routing per answer, and a loop of three visits.
- Eager asks: `match: first`, `&&`, `if` and `try` do not skip a call.
- Retry and timeout: the fixed policy on a host step with its own
  `retry` and `timeout`, and on a join.
- `TestDecisionReplayAsksTheSameQuestion`: re-apply a run's events to a
  fresh state; the decision job, its subject text and its ID are the
  same, and no answered decision is dispatched again.
- `invalid_output` for a missing ID, an extra ID, 1.5 and an unknown
  option, each using one of the 3 attempts.
- `TestDecisionBadAnswerThenAGoodOne`: attempt 1 returns a malformed
  answer and attempt 2 a valid one. The step routes on attempt 2's
  answer, and two decision attempts are dispatched.
- Failure: `catch` routes, and the target reads `{error: ...}`. No catch
  fails the run. A control step fails the run.
- Cancel and a failing sibling both stop a decision in flight.
- A reloaded state never dispatches an answered decision again. Late and
  duplicate decision events are ignored.
- StepID: `parse`, `Unit`, `Step` and `State.Unit` for `.next` IDs, and
  rejection of `.next` with an index.

**Load tests** (`typecheck_test.go`, `validate_test.go`): each row of the
Load errors table, and `checkSwitch` on `choose`.

**Property test** (`property_test.go`): the generator puts `decide` and
`choose` on random edges, including steps before a join and back edges
in loops. The fake answers each question from its ID (a hash to a
number, or to an option), so every order sees the same answers. The test
shuffles completions, decision completions among them, and checks that
the final state and the dispatch set are the same in every order.

**Soundness fuzz** (`soundness_fuzz_test.go`): the generator writes
decision calls into `when` and `switch`, with `catch` on some steps. The
runner answers decision jobs with random valid answers and fails some
after their attempts. It checks every value an expression reads against
its Load type, the catch-route `{error: ...}` included.

**Host suite** (`internal/hosttest`, one entry each in `list.go`), on
local, sqlite and postgres:

- `DecisionsOnEveryHost`: the pure driver, local and the backend run
  the same cases and must agree: UC-3 (one job, one question), UC-2,
  UC-4, UC-5, UC-6, and a decision after a sleep step.
- `DecisionAskedOnceAndRecorded`: a durable host asks each decision
  once, and its step record shows the question and the answer.

**Example:** `examples/decide` runs UC-3 with a fake handler, three
times, and prints the three routes.

## Open questions

- **The expr API's exact shape.** `Calls` and `Literal` as above are the
  proposal. If expr's maintainers prefer one method that returns the
  calls with literal values already resolved, the root needs only small
  changes. It does not change this design. Resolve in the expr PR.
- **The ID length.** 16 bytes of SHA-256 is plenty for the few questions
  in one block. Keep it unless review objects.
