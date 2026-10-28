# workflow

> [!NOTE]
> The API may still change before v1.0. Under semver, a 0.x minor release
> can break it; the release notes say what changed. The earlier,
> goroutine-based engine is `github.com/deepnoodle-ai/workflow@v0.0.5`.

A deterministic progression engine for step graphs, built for event-driven
hosts. Go 1.27+. The root module depends only on the standard library and
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

Definitions are JSON, or YAML decoded by the host and passed to `LoadMap`:

```yaml
name: Triage issue
input: {type: object, required: [title], properties: {title: {type: string}}}
steps:
  - name: classify
    agent: triager                  # a host kind; the host runs it
    with: {title: '${{ input.title }}'}
    output: {type: object, required: [kind], properties: {kind: {enum: [bug, feature]}}}
    retry: 3
    next: {switch: classify.kind, cases: {bug: file_bug, feature: label}}
  - {name: file_bug, action: tracker.create_issue, with: {title: '${{ input.title }}'}}
  - {name: label, action: tracker.add_label, with: {label: feature}}
```

A step's kind is the one key among the host's kinds (`agent`, `action`, ...)
or the library's control kinds: `join`, `set` and `sleep`. Everything about a
host kind except `with:` is passed to the host unevaluated. Steps can also fan
out (several matching edges), join, loop, run an `each` step's items in
parallel, time out, and route failures with `catch`; a run can have
`on_failure` and `finally` steps.

[examples/prreview/review.json](examples/prreview/review.json) is a complete
pull request review: three reviewers in parallel, a join, `each` with
concurrency over their findings, and a review posted with whichever verdict
step ran.

**Definitions are typed.** Steps declare output schemas (and hosts can declare
each kind's parameter and output schemas), so `Load` catches a misspelled
field (`it.confirmd`), a value outside an enum, a `with:` that does not fit a
tool's parameters, a `when` that is not a boolean, a `switch` that misses a
case, a read of a step that may not have run on every path to the reader, and
a join that names a step a path can skip. Apply enforces the same schemas at
run time. See [documentation/definitions.md](documentation/definitions.md)
for the full language.

## The host loop

```go
def, _ := workflow.Load(doc, workflow.KindMap{"agent": checkAgent, "action": checkAction})

st, cmds, _ := workflow.Apply(def, nil, workflow.Event{At: now, Kind: workflow.EventStart, Input: input})
// persist st and cmds in one transaction; after commit, carry out cmds:
//   DispatchStep   run the attempt, not before cmd.NotBefore
//   ScheduleTimer  deliver TimerFired for cmd.StepID at cmd.NotBefore
//   CancelStep     stop that work if you can; a late result is ignored
//   RunCompleted / RunFailed / RunCancelled   the run is over (after finally)

st, cmds, _ = workflow.Apply(def, st, workflow.Event{
    At: now, Kind: workflow.EventStepCompleted, StepID: "main/classify#1", Output: out,
})
```

Every attempt has a readable `StepID` such as `main/verify[3]#1` (path `main`,
step `verify`, item 3, attempt 1). Hosts key two things on it, with the run
ID: the attempt (the whole StepID: jobs, step records, deduplicating results)
and the unit (`StepID.Unit()`, `main/verify[3]`, which a retry keeps: the
idempotency key for side effects outside the host, such as a payment).

In short, the host contract: persist the state and commands in the event's
transaction, act after commit, deliver results at least once, and keep each
run's definition. Apply is deterministic, and ignores duplicate and late
events. [documentation/hosts.md](documentation/hosts.md) is the full contract.

## Packages

| Package | What it is |
| --- | --- |
| `workflow` | `Load`, `Apply`, `Abort`, `Outstanding`, the `State`, `Event` and `Command` types, and the `Handler` and `Job` types both hosts call. Pure. |
| `local` | An in-process host: runs commands as goroutines calling handlers. For tests, CLIs and single-process programs. `Save` + `Resume` give it crash recovery. |
| `postgres` (module) | A durable host: state, step records, event log and outbox in Postgres; a `Worker` claims jobs with `SKIP LOCKED`, heartbeats leases and fences late results. |
| `cmd/workflow` | `workflow validate` and `workflow run` for JSON definitions. |

Hosts that already have durable work queues (leases, wake-ups, reaping) use
only the root package and keep their own runtime.

## Running locally

```go
r := &local.Runner{Handlers: map[string]workflow.Handler{
    "agent": func(ctx context.Context, job workflow.Job) (any, error) {
        return runAgentTurn(ctx, job.Config["agent"], job.Params)
    },
    "action": func(ctx context.Context, job workflow.Job) (any, error) {
        // Hand the work off; its result arrives later through Run.Deliver.
        enqueue(job.RunID, job.StepID, job.Params)
        return nil, workflow.ErrDeferred
    },
}}
run, err := r.Start(ctx, "issue-7", def, input)
// ... when the action's result arrives:
err = run.Deliver(workflow.Event{Kind: workflow.EventStepCompleted, StepID: id, Output: out})
st, err := run.Wait()   // r.Run is Start then Wait, for runs with nothing deferred
```

The same handlers run on the postgres `Worker`:

```go
store, _ := postgres.New(pool, kinds)
_ = store.Migrate(ctx)
go (&postgres.Worker{Store: store, Handlers: handlers}).Run(ctx)
res, err := store.Start(ctx, postgres.NewRun{ID: "issue-7", Definition: def, Input: input})
```

```sh
go run ./examples/apply      # Apply by hand, no runner
go run ./examples/typed      # what Load catches
go run ./examples/retry      # retry, timeout, catch, on_failure, finally
go run ./examples/prreview   # the PR review
```

[examples/](examples/README.md) lists them all, including a Postgres run that
waits for a human approval.

## Not yet supported

- Operator pause and resume of a run.
- Signals: waiting on named external events, other than a deferred step.
- Child workflows. A host kind can start another run.
- Run listing, search and retention in postgres. There is a GIN index on
  run metadata, but no query API yet.
- Jitter in retry backoff.

## Development

```sh
make test       # root module
make test-pg    # postgres module; needs WORKFLOW_PG_DSN
make test-all   # both, plus go vet
```
