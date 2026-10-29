# workflow: a pure progression engine

Oct 28, 2026 · Curtis Myzie

The `workflow` Go library is now one pure function, `Apply(def, state, event) → (state, commands)`, plus hosts that run it in-process or on Postgres. A run is a row, not a process: any worker can advance it, and it can wait days for a person or an agent. Definitions are typed, so mistakes surface at validation rather than hours into a run. It is for services that already run durable jobs and use only the core package, and for programs that want a ready Postgres host. It is merged on main (#42). The API may still change before v1.0, and we would like your feedback before it settles. The old engine remains at `github.com/deepnoodle-ai/workflow@v0.0.5`.

## What changed

The engine no longer runs workflows; it computes what should happen next. Everything that touches the outside world moved to a host, and the library ships two.

|  | Before | Now |
| --- | --- | --- |
| Engine | An executor that ran a workflow in-process, one goroutine per branch | `Apply`: a pure function from state and one event to new state and commands. No I/O, goroutines or clock |
| Where a run lives | In the process running it, with checkpoints to resume | In a row. Each event advances it in one short transaction, on any worker |
| Persistence | Interfaces to implement (`Checkpointer`, `StepProgressStore`, `ActivityLogger`, `SignalStore`) | The `State` is plain JSON. A Postgres host is included |
| Step execution | A registry of Go activities | Commands (`DispatchStep`) the host carries out. Kinds such as `agent` or `action` belong to the host |
| Waiting on people or other systems | Signals, pause, durable sleep | A step whose result is delivered later, any time; `sleep` and `timeout` become timer commands |
| Expressions | Pluggable script engine, untyped | expr, type-checked against JSON Schemas at validation, and enforced at run time |
| Routing | Conditional edges | `switch` on a value (checked against its enum) and boolean `when` edges |
| Joins | Wait for named branches | Wait for every live path that can still reach them. The names are an assertion that Load checks |
| Failure handling | Per-step `catch` | `catch`, `on_failure` steps when the run fails, and `finally` steps whatever the outcome |

The change is large: about 35,000 lines removed and 16,000 added, including tests and docs. There is no compatibility layer.

## At a glance

![Apply decides what happens next; the host does it](progression-host-loop.png)

Apply only decides. Everything that touches the outside world happens in the host, after the commit, and comes back as an event.

## The core API

The whole engine is one call:

```go
next, commands, err := workflow.Apply(def, state, event)
```

An event is one of five things: the run started, a step completed, a step failed, a timer fired, or the run was cancelled. `Event.At` is the only clock Apply reads, so the same state and event always give the same result. Apply never modifies the state it is given.

A command is what the host must do: `DispatchStep` (run a step attempt, not before `NotBefore`), `CancelStep`, `ScheduleTimer`, and `RunCompleted`, `RunFailed` or `RunCancelled`.

Every attempt has a stable, readable `StepID`, such as `main/verify[3]#1`: path `main`, step `verify`, item 3, attempt 1. A loop's later visits show as `main/poll@2#1`. A StepID is unique within its run. With the run ID it keys the attempt: the job, the step record, the dedupe of results. `StepID.Unit()` (`main/verify[3]`) drops the attempt, so a retry keeps it; with the run ID it is the idempotency key for side effects outside the host.

A host drives a run like this:

```go
def, _ := workflow.Load(doc, workflow.KindMap{"agent": nil, "action": nil})

st, cmds, _ := workflow.Apply(def, nil, workflow.Event{
    At: now, Kind: workflow.EventStart, Input: map[string]any{"user": "u_123"},
})
// In one transaction: store st, and store cmds as an outbox.
// After commit, carry out cmds. Each result comes back as an event:

st, cmds, _ = workflow.Apply(def, st, workflow.Event{
    At: now, Kind: workflow.EventStepCompleted,
    StepID: "main/lookup#1", Output: map[string]any{"email": "ada@example.com"},
})
```

**The host contract.** A host that follows these five rules gets exactly-once progression from at-least-once delivery:

1. **Persist atomically.** Store the new state and the commands in the same transaction as the event, with the run row locked.
2. **Act after commit.** Carry out commands only after the transaction commits.
3. **Deliver at least once.** Report every result as an event and retry until it is applied. Duplicates and late results are ignored.
4. **Run each attempt idempotently.** A dispatch can be carried out twice. Key side effects outside the host on the run ID and `StepID.Unit()`, which a retry keeps.
5. **Keep the definition.** A state records its definition's hash, and Apply refuses another. A run keeps the version it started with. The hash covers the document only: the host's kind schemas are its current code, so change them compatibly, or ship an incompatible change as a new kind name.

**What Apply guarantees in return:** the same ordered events give the same states and commands. Successful completions of steps in flight at the same time may arrive in any order, and the run reaches the same final state and dispatches the same steps. Failures and timeouts are not order-free: a failure that arrives before a sibling's success fails the run first, and a timeout races its step's result. Nothing that settled is dispatched again; a step retries only if it declares `retry` and the error is retryable. A cancel cancels everything in flight and decides the outcome once: delivering it again changes nothing, and the finally steps it started run to the end (`Force` stops them). Property tests check order independence over 200 shuffled orders of the PR review, up to 40 orders each of 60 random graphs, and random graphs with loops and finally steps.

The full contract is in `documentation/hosts.md`.

## Definitions

A definition is data: JSON, or YAML the host decodes and passes to `LoadMap`. Assistants can write it, people can edit it, a UI can draw it, and a run can resume months later on another build. This one reviews a pull request:

```yaml
name: Review pull request
schemas:
  findings: {type: object, required: [items], properties: {items: {type: array, items: {type: string}}}}
  verdict:  {type: object, required: [finding, confirmed],
             properties: {finding: {type: string}, confirmed: {type: boolean}}}
  # review and pull_request_event omitted here
input: {type: object, required: [event], properties: {event: pull_request_event}}
steps:
  - name: pending
    action: github.commit.set_status        # a host kind; the host decides what it means
    with: {sha: '${{ input.event.pull_request.head.sha }}', state: pending}
    next: [correctness, tests]              # two edges: the path forks

  - {name: correctness, agent: reviewer, model: claude-opus-5-5,  output: findings, next: [gather]}
  - {name: tests,       agent: reviewer, model: claude-haiku-5-5, output: findings, next: [gather]}

  - name: gather
    join: [correctness, tests]              # waits for both paths; both steps must have settled
    next: [verify]

  - name: verify
    each: 'concat(correctness.items, tests.items)'   # one dispatch per finding
    concurrency: 4
    agent: reviewer
    output: verdict
    with: {finding: '${{ item }}'}
    retry: {max_attempts: 3, backoff: 10s, errors: [rate_limited]}
    match: first
    next:
      - {step: report, when: 'any(verify, it.confirmed)'}
      - {step: approve}

  - {name: report, agent: reviewer, output: review, next: [post]}
  - {name: approve, set: {verdict: APPROVE, body: No confirmed findings.}, next: [post]}

  - name: post
    action: github.pull_request.create_review
    with: {event: '${{ coalesce(report, approve).verdict }}'}   # whichever ran

on_failure:
  - {name: errored, action: github.commit.set_status, with: {state: error, description: '${{ error.message }}'}}
finally:
  - {name: audit, action: audit.log, with: {outcome: '${{ run.status }}'}}   # whatever the outcome
outputs:
  verdict: {value: '${{ coalesce(report, approve).verdict }}', schema: {enum: [APPROVE, REQUEST_CHANGES, COMMENT]}}
```

Values in `with:`, `set:` and `outputs` are templates, written `${{ expr }}` as in GitHub Actions. A value that is exactly one template keeps the expression's type, so `${{ correctness.items }}` stays a list. Only `${{ }}` is a template: shell `${HOME}` and JavaScript template literals pass through as text.

- **Kinds.** Each step has one kind key. `join`, `set` and `sleep` belong to the library; every other key (`agent`, `action`) is a host kind. Everything about a host kind except `with:` goes to the host unevaluated, in `Command.Config`.
- **Paths.** When several edges match, the path forks. A join waits for every live path that can still reach it, then merges what arrived. The steps it names are an assertion: each must have settled on an arriving path, so it is safe to read after the join. Load rejects a name that a path can skip.
- **`each`** runs one dispatch per item, up to `concurrency` at a time. Its value is the list of item outputs, in item order.
- **Failure.** Nothing retries unless it declares `retry`. `catch` routes a failure to another step, which reads the error from the failed step's value. Otherwise the run fails, everything in flight is cancelled, and `on_failure` runs. `finally` steps run last, in order, whatever the outcome; one that fails is reported in `FinallyErrors` and does not change the outcome.
- **`timeout`, `sleep` and `max_visits`** cover deadlines, waits and loop bounds. Every cycle must have a bound. The state holds a loop's latest passes, not its history, so a poll loop that runs for months holds one poll's output.
- **Outputs.** `outputs:` names the run's results, each with an optional schema checked when the run completes. `Definition.OutputSchema()` returns them as one JSON Schema, for a host that publishes a workflow as a tool.

Routing on a value uses `switch` instead of a chain of conditions:

```yaml
- name: classify
  agent: triager
  output: triage                  # kind: bug | feature | question
  next:
    switch: classify.kind
    cases: {bug: reproduce, feature: spec, question: answer}
```

## Typed definitions

`Load` type-checks every expression and parameter block before anything runs. Types come from JSON Schemas: each step's `output:`, the run's `input:`, and schemas the host supplies for its tools. Steps without a schema stay unchecked, so typing can be added gradually.

These are four single mistakes in a triage workflow (the classify step above, three handlers, and a step that comments on the issue), and what `Load` reports (from `examples/typed`):

```
classify.summary -> classify.sumary
  step comment: with: `classify.sumary`: no field "sumary" in
    {kind: "bug" | "feature" | "question", summary: string} (did you mean "summary"?)

"feature": "spec" -> "feture": "spec"
  step classify: next: case "feture": classify.kind is never "feture" (it is one of bug, feature, question)
  step classify: next: switch on classify.kind does not handle feature; add cases or a default

coalesce(reproduce, spec, answer).body -> reproduce.body
  step comment: with: `reproduce`: may be nil here; use ?. or try(...)

"issue": "${{ input.issue.number }}" -> "issue": "${{ input.issue.title }}"
  step comment: with.issue: string is not integer
```

- **Maybe-nil reads.** Validation works out from the graph which steps are certain to have run before each step. Reading a step that might not have run needs `?.`, `try(expr, fallback)` or `coalesce`. The checker knows `coalesce(reproduce, spec, answer)` is never nil, because one of the three alternatives must have run.
- **Routing.** A `switch` must handle every value of its enum or have a `default`. A `when` must be a boolean, so `when: verify` (a list) is an error. Under the default match, an edge without `when` after a conditional one reads as an else but is always followed, so Load asks for `match: first` or an explicit `match: all`.
- **The promise.** If a definition validates and every step's output matches its schema, no expression fails at run time on a missing field, a nil, or a wrong type. Two independent reviews set out to break this promise and found 15 ways to do it. Each is fixed and is now a regression test, so the promise is checked on every build.
- **Run time.** Apply enforces the same schemas. The input is checked at start, parameters before dispatch (the host never sees a bad call), step outputs on completion, and run outputs at the end. `Command.OutputSchema` carries the schema, so a host can ask a model for exactly that shape.

A host that knows its tools implements one optional method, and `with:` blocks are then checked against each tool's parameters:

```go
func (k hostKinds) Schemas(kind string, cfg map[string]any) (params, output *workflow.Schema, err error) {
    if kind == "action" {
        tool := k.tools[cfg["action"].(string)]   // e.g. github.comment
        return tool.InputSchema, tool.OutputSchema, nil
    }
    return nil, nil, nil
}
```

The type checker lives in the expr library as `Program.Check`, so the checker and the evaluator read the same syntax, and other expr users get it too.

## Running it

Most hosts never call `Apply` themselves. Two hosts ship with the library. Both call the same `workflow.Handler`, keyed by kind, with a `workflow.Job`: the command plus the run ID and how many times it has been claimed. A handler tested in-process runs on Postgres unchanged.

**In-process** (`local`), for tests, CLIs and single-process programs:

```go
r := &local.Runner{Handlers: map[string]workflow.Handler{
    "agent": func(ctx context.Context, job workflow.Job) (any, error) {
        return runAgentTurn(ctx, job.Config["agent"], job.Params)
    },
}}
st, err := r.Run(ctx, "run-1", def, input)   // st.Status, st.Outputs, st.Error
```

Return a `*workflow.StepError` to set the error type and whether it is retryable, or `workflow.ErrDeferred` when the result arrives later. `Start` returns a `*Run` with `Wait`, `Deliver` (for deferred steps) and `Cancel`. Cancelling the context stops the run without cancelling it; `Save` and `Resume` continue it from the saved state.

**Postgres** (`postgres`, a separate module on pgx). It keeps definitions by hash, run state in jsonb, a record per step attempt, an event log, and an outbox of jobs. Workers claim jobs with `SKIP LOCKED` and hold leases with heartbeats. Each claim raises the job's fencing token, which shuts out late results from a worker that lost its lease. The store loads every definition itself, with its own kinds and limits.

```go
store, _ := postgres.New(pool, kinds)
_ = store.Migrate(ctx)

_, _ = store.Start(ctx, postgres.NewRun{
    ID: "deploy-1.4.0", Definition: def, Input: map[string]any{"version": "1.4.0"},
})

w := &postgres.Worker{Store: store, Handlers: map[string]workflow.Handler{
    "action": runAction,
    "approval": func(ctx context.Context, job workflow.Job) (any, error) {
        notifyApprovers(job.RunID, job.StepID, job.Params)   // email, Slack, a page in the app
        return nil, workflow.ErrDeferred                     // the result arrives later
    },
}}
go w.Run(ctx)
```

A step that returns `ErrDeferred` stays in flight with no worker holding it, for minutes or days. When the person decides, the application delivers the result in its own transaction, for example from an HTTP handler. Delivering it twice is safe:

```go
store.Deliver(ctx, "deploy-1.4.0", workflow.Event{
    Kind: workflow.EventStepCompleted, StepID: "main/approve#1",
    Output: map[string]any{"approved": true, "by": "grace"},
})
```

`StartTx`, `DeliverTx` and `CancelTx` take the caller's transaction, so starting a run or recording a result can commit atomically with the application's own writes. `Cancel` is idempotent: a second cancel reports `Applied` false. A run whose definition no longer loads against the current kinds is unavailable; after `MaxClaims` claims a worker fails it with `workflow.Abort`, which runs neither `on_failure` nor `finally`. A host with its own durable job system can skip both hosts and map commands onto its jobs.

Runnable examples: `examples/apply` (Apply by hand), `examples/typed`, `examples/retry` (retry, timeout, catch, `on_failure`, `finally`), `examples/loop` (a bounded poll loop), `examples/prreview` and `postgres/examples/approval`.

## Not in this version

- **Operator pause and resume.** There is no way yet to pause a run by hand.
- **Signals.** Waiting on a named external event is not built; waiting on the outside world is a deferred step delivered later.
- **Child workflows.** A step cannot start another workflow and wait for it, except through a host kind that does so.
- **Run queries and retention.** The Postgres store reads one run, its steps and its events. There is a GIN index on run metadata but no API yet to list or search runs, and no retention.
- **Other expression engines.** The pluggable script engine is gone; expressions are expr only. (SQLite came back as a durable host in #73.)
- **Backoff jitter.** Apply cannot use randomness, so backoff is exact. A host may add jitter when it schedules, since later is always safe.
- **A migration path.** The old API is removed, with no compatibility layer.

## Questions for you

1. **The boundary.** Would you run one of the included hosts, or map commands onto a job system you already have? What would you need from the library to do the second?
2. **Strict types.** Expressions may read only the properties a schema declares, which is what catches typos. Is declaring what you read acceptable, or too much friction?
3. **Maybe-nil.** Reading a step that might not have run needs `?.`, `try(...)` or `coalesce`. Would you rather the engine infer joins from which steps read which?
4. **Step names.** Steps are bare names in expressions (`classify.kind`), so a step cannot be called `first`, `count` or `split`. Would a `steps.` prefix be worth the longer expressions?
5. **Invalid output.** An output that does not match its schema fails the step without retry, since retrying an action could repeat its side effects. An agent host that wants retries validates the output itself. Is that the right default?
6. **What is missing** for the workflows you would run: signals, pause, child workflows, run listing, or something else?
