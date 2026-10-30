# workflow

**Durable workflows in one function call.**

```go
next, commands, err := workflow.Apply(def, state, event)
```

That's the whole engine. You tell it where a run is and what just happened.
It tells you where the run is now and what to do next. It does no I/O, starts
no goroutines and never looks at a clock. The same input always gives the same
output.

That small core gets you a lot. A run can wait three days for someone to click
**Approve**, live through forty deploys, and finish on a different machine
than it started on. No process holds the run while it waits. **A run is just
a row.**

> [!NOTE]
> Pre-1.0: a 0.x minor release can still change the API, and the release
> notes say what changed. Go 1.27+. Looking for the old goroutine-based
> engine? That's `github.com/deepnoodle-ai/workflow@v0.0.5`.

## Why you might like it

- **The core is a pure function.** You can test it with a table of inputs,
  replay a run from its event log, or reorder parallel results and get the
  same answer. [examples/replay](examples/replay) builds a time-travel
  debugger on it.
- **Mistakes show up at load time, not at 3am.** Typed definitions catch a
  misspelled field, a `switch` that misses a case, or a read of a step that
  might not have run.
- **Bring your own runtime.** Run in-process, on SQLite, on Postgres, or on
  the job queue you already have.
- **Built for agents.** Steps can be agent turns and tool calls. An agent can
  write a workflow too: `Load` checks it like a compiler and sends the errors
  back to the model.
- **Scales to zero.** It works on Cloud Run with a scheduled endpoint or
  Cloud Tasks. Nothing needs to stay running between events.
- **Only two dependencies:** the standard library and
  [`expr`](https://github.com/deepnoodle-ai/expr).

## How it works

```
   event ──▶ ┌─────────┐ ──▶ new state    save it
             │  Apply  │
   state ──▶ └─────────┘ ──▶ commands     carry them out
                                │
     ▲                          ▼
     └──── result ◀──── your code runs the step
                        (call an agent, hit an API,
                         set a timer, wait for a human)
```

An event is one of: the run started, a step finished or failed, a timer
fired, or the run was cancelled. Apply turns each event into new state plus
a short to-do list. Your host saves both in one transaction and then works
through the list. Duplicate and late events are ignored, so retrying a
delivery is always safe.

## A workflow

You write workflows in JSON, or in YAML that your host decodes:

```yaml
name: Triage issue

input:
  type: object
  required: [title]
  properties:
    title:
      type: string

steps:
  - name: classify
    agent: triager # your kind; your code runs it
    with:
      title: ${{ input.title }}
    output:
      type: object
      required: [kind]
      properties:
        kind:
          enum: [bug, feature]
    retry: 3
    next:
      switch: classify.kind
      cases:
        bug: file_bug
        feature: label

  - name: file_bug
    action: tracker.create_issue
    with:
      title: ${{ input.title }}

  - name: label
    action: tracker.add_label
    with:
      label: feature
```

```
               ┌─ bug ─────▶ file_bug
   classify ───┤
               └─ feature ─▶ label
```

`agent` and `action` aren't built in. They're kinds that you define, and you
can add as many as you like. The library supplies the control flow:

| You want to… | Use |
| --- | --- |
| Do several things at once | more than one matching `next` edge (fan out) |
| Wait for all of them | a `join` step |
| Run a step for each item in a list, N at a time | `each` with `concurrency` |
| Branch on a value | `switch`, or `when` on an edge |
| Loop until done | an edge back to an earlier step, bounded by `max_visits` |
| Wait for a while | `sleep` |
| Retry flaky work | `retry`, with backoff and a `timeout` |
| Handle a failure | `catch` to route it, `on_failure` to clean up, `finally` to always run |
| Compute a value | `set` |

The full language is in
[documentation/definitions.md](documentation/definitions.md). For a real
example, [examples/prreview/review.json](examples/prreview/review.json) is a
complete PR review: three reviewers in parallel, a join, a fan-out over their
findings, and a posted verdict.

## Try it

```sh
go run ./examples/apply      # drive the engine by hand, one event at a time
go run ./examples/typed      # see what Load catches before anything runs
go run ./examples/retry      # retries, timeouts, catch, on_failure, finally
go run ./examples/prreview   # the PR review, with fake reviewers
```

There are more in [examples/](examples/README.md): sagas, map-reduce, canary
deploys, on-call escalation, an agent that writes its own plan, and a
Postgres run that waits for a human.

## Pick a host

Apply only decides what to do. A **host** does the work: it stores state,
runs steps and fires timers. Use one of ours, or write your own.

| Host | Good for |
| --- | --- |
| [`local`](local) | Tests, CLIs and single-process programs. `Save` and `Resume` add crash recovery. |
| [`sqlite`](sqlite) | Development, or a single machine. Same API as Postgres. |
| [`postgres`](postgres) | Production. Workers claim jobs with `SKIP LOCKED`, heartbeat their leases and fence off late results. |
| Your own | You already have durable queues. Use the root package and keep your runtime. |

In-process, a handler is just a function:

```go
def, _ := workflow.Load(doc, workflow.KindMap{"agent": nil, "action": nil})

r := &local.Runner{Handlers: map[string]workflow.Handler{
    "agent": func(ctx context.Context, job workflow.Job) (any, error) {
        return runAgentTurn(ctx, job.Config["agent"], job.Params)
    },
    "action": func(ctx context.Context, job workflow.Job) (any, error) {
        // Hand the work off. The result arrives later through run.Deliver.
        enqueue(job.RunID, job.StepID, job.Params)
        return nil, workflow.ErrDeferred
    },
}}
run, _ := r.Start(ctx, "issue-7", def, input)
```

The same handlers run on Postgres, or on SQLite with the same code:

```go
store, _ := postgres.New(pool, kinds)
_ = store.Migrate(ctx)
go (&postgres.Worker{Store: store, Handlers: handlers}).Run(ctx)
res, _ := store.Start(ctx, postgres.NewRun{ID: "issue-7", Definition: def, Input: input})
```

You can also start or advance a run in your own transaction (`StartTx`,
`DeliverSQLTx`, ...), so it commits atomically with your own writes.
[documentation/hosts.md](documentation/hosts.md) covers all of this,
including scale to zero and the full host contract.

### Step IDs you can read

Every attempt has an ID that you can read:

```
main/verify[3]#1
 │     │    │  └─ attempt 1
 │     │    └──── item 3 of an each
 │     └───────── step "verify"
 └─────────────── path "main"
```

Drop the `#1` (`StepID.Unit()`) and you have the idempotency key for side
effects. A retry keeps it, so a retried payment doesn't charge anyone twice.

## Going deeper

- [documentation/definitions.md](documentation/definitions.md): the workflow
  language
- [documentation/hosts.md](documentation/hosts.md): the host contract, and
  how to run on Postgres, SQLite and Cloud Run
- [llms.txt](llms.txt): the API reference, in a form your coding agent can
  read
- [examples/](examples/README.md): small programs that each show one or two
  ideas

## Not yet

- Pausing and resuming a run by hand
- Signals: waiting on a named external event, other than a deferred step
- Child runs kept inside the parent run (today a child is a separate run; see
  [Generated workflows](documentation/hosts.md#generated-workflows))
- Jitter in retry backoff

## Development

```sh
make test               # root module
make test-long          # the randomized engine tests at full size
make test-pg            # postgres module; needs WORKFLOW_PG_DSN
make test-sqlite        # sqlite module, on modernc.org/sqlite
make test-sqlite-mattn  # sqlite module on mattn/go-sqlite3 too; needs cgo
make test-all           # root, postgres, sqlite and the parity check, plus go vet
```
