# workflow

A deterministic progression engine for step graphs, built for event-driven
hosts. Go 1.26+. The root module depends only on the standard library and
[`github.com/deepnoodle-ai/expr`](https://github.com/deepnoodle-ai/expr).

```go
next, commands, err := workflow.Apply(def, state, event)
```

That is the whole engine. `Apply` takes a run's state and one event (the run
started, a step completed or failed, a timer fired, the run was cancelled) and
returns the new state and the commands the host must carry out. It does no
I/O, starts no goroutines and reads no clock but `event.At`. The same input
always gives the same output.

No process holds a run. A run is a row; each event advances it in one short
transaction, on whichever worker receives the event. A run can wait days for
a human approval or an agent turn on another service, and survive any number
of deploys.

## A workflow

Definitions are JSON (or YAML decoded by the host and passed to `ParseMap`):

```yaml
name: Review pull request
schemas:
  findings: {type: object, required: [items], properties: {items: {type: array, items: {type: string}}}}
  verdict:  {type: object, required: [finding, confirmed],
             properties: {finding: {type: string}, confirmed: {type: boolean}}}
  # review: {verdict: APPROVE | REQUEST_CHANGES | COMMENT, ...}; github_pull_request: {...}
event_schema: github_pull_request
steps:
  - name: pending
    action: github.commit.set_status          # a host kind; the value goes to Config
    with: {sha: '${event.pull_request.head.sha}', state: pending}
    next: [correctness, contracts, tests]     # three edges: fan out

  - {name: correctness, agent: reviewer, model: claude-opus-5-5,   output: findings, next: [gather]}
  - {name: contracts,   agent: reviewer, model: claude-sonnet-5-5, output: findings, next: [gather]}
  - {name: tests,       agent: reviewer, model: claude-haiku-5-5,  output: findings, next: [gather]}

  - name: gather
    join: [correctness, contracts, tests]     # wait for these steps
    next: [verify]

  - name: verify
    each: 'concat(correctness.items, contracts.items, tests.items)'
    concurrency: 4                            # items run in parallel, four at a time
    agent: reviewer
    output: verdict
    with: {finding: '${item}'}
    match: first
    next:
      - {step: report, when: 'any(verify, it.confirmed)'}
      - {step: approve}

  - {name: report, agent: reviewer, output: review, next: [post]}
  - {name: approve, set: {verdict: APPROVE, status: success, body: No confirmed findings.}, next: [post]}

  - name: post
    action: github.pull_request.create_review
    with: {event: '${coalesce(report, approve).verdict}'}   # whichever ran

on_failure:
  - action: github.commit.set_status
    with: {state: error, description: '${error.message}'}
```

A step's kind is the one key among the host's kinds (`agent`, `action`, ...)
or the library's control kinds: `join`, `set` and `sleep`. Everything about
a host kind except `with:` is passed to the host unevaluated.

**Definitions are typed.** Steps declare output schemas (and hosts can
declare each kind's parameter and output schemas), so `Validate` catches a
misspelled field (`it.confirmd`), a value outside an enum, a `with:` that
does not fit a tool's parameters, a `when` that is not a boolean, a `switch`
that misses a case, and a read of a step that may not have run on every path
to the reader. Apply enforces the same schemas at run time. Enum routing uses
`next: {switch: classify.kind, cases: {bug_fix: fix, ...}}`. See
[documentation/definitions.md](documentation/definitions.md) for the full
language.

## The host loop

```go
def, _ := workflow.Parse(doc)
_ = def.Validate(workflow.KindMap{"agent": checkAgent, "action": checkAction})

st, cmds, _ := workflow.Apply(def, nil, workflow.Event{At: now, Kind: workflow.EventStart, Trigger: ev})
// persist st and cmds in one transaction; after commit, carry out cmds:
//   DispatchStep   run the step (idempotent by cmd.StepID), not before cmd.NotBefore
//   ScheduleTimer  deliver TimerFired for cmd.StepID at cmd.NotBefore
//   CancelStep     stop that work if you can; a late result is ignored
//   RunCompleted / RunFailed / RunCancelled   the run is over

st, cmds, _ = workflow.Apply(def, st, workflow.Event{
    At: now, Kind: workflow.EventStepCompleted, StepID: "main/verify[3]#1", Output: out,
})
```

Every unit of work has a stable, readable `StepID` such as `main/verify[3]#1`
(path `main`, step `verify`, item 3, attempt 1). Hosts use it as the
idempotency key for the work they create and the record they upsert.

### The host contract

1. **Persist atomically.** Store the new state, the step records and the
   commands (an outbox) in the same transaction as the event. Serialize
   events per run: lock the run row.
2. **Act after commit.** Carry out commands only after the transaction
   commits. A crash between commit and dispatch loses nothing: the outbox
   still holds the command.
3. **Deliver at least once.** Report every result as an event and retry
   until it is applied. Duplicates and late results are ignored, so a
   result may arrive any number of times.
4. **Run each attempt idempotently.** A dispatch can be carried out more
   than once (a worker dies after doing the work and before reporting).
   Key side effects on the StepID.
5. **Keep the definition.** A state records the hash of its definition;
   `Apply` refuses another. Store definitions by hash and give a run the
   version it started with.

`Apply` guarantees in return: the same state and event give the same result;
completions applied in any order reach the same final state and the same
dispatches; nothing that settled is dispatched again; a step retries only if
it declares `retry` and the error is retryable; cancellation cancels
everything in flight and skips `on_failure`; and a step whose output exceeds
the state limits fails with a clear error.

## Packages

| Package | What it is |
| --- | --- |
| `workflow` | `Parse`, `Validate`, `Apply`, `Outstanding`, and the `State`, `Event`, `Command` types. Pure. |
| `local` | An in-process driver: runs commands as goroutines calling Go handlers. For tests, CLIs and single-process programs. `Save` + `Resume` give it crash recovery. |
| `postgres` (module) | A durable host: state, step records, event log and outbox in Postgres; a `Worker` claims tasks with `SKIP LOCKED`, heartbeats leases and fences late results. |
| `cmd/workflow` | `workflow validate` and `workflow run` for JSON definitions. |

Hosts that already have durable work queues (leases, wake-ups, reaping) use
only the root package and keep their own runtime. See
[documentation/hosts.md](documentation/hosts.md).

## Running locally

```go
r := &local.Runner{Handlers: map[string]local.Handler{
    "agent": func(ctx context.Context, c workflow.Command) (any, error) {
        return runAgentTurn(ctx, c.Config["agent"], c.Params)
    },
}}
st, err := r.Run(ctx, def, inputs, trigger)
```

```sh
go run ./examples/apply      # Apply by hand, no runner
go run ./examples/typed      # what Validate catches
go run ./examples/retry      # retry, timeout, catch, on_failure
go run ./examples/prreview   # the PR review
```

[examples/](examples/README.md) lists them all, including a Postgres run that
waits for a human approval.

## Development

```sh
make test       # root module
make test-pg    # postgres module; needs WORKFLOW_PG_DSN
make test-all   # both, plus go vet
```
