# Hosts

`workflow.Apply` decides; a host does. This guide is the host contract: what
Apply returns, what a host must do with it, and the two hosts in this
repository. The terms (step, visit, unit, attempt) are defined in
[definitions.md](definitions.md).

## The contract

A host keeps, per run: the `State` (JSON), the definition it started with,
and whatever records it wants for people to read. For each event:

```text
BEGIN
  SELECT state FROM runs WHERE id = $1 FOR UPDATE    -- serialize events per run
  next, commands := Apply(def, state, event)
  UPDATE runs SET state = next
  upsert step records for the event and the commands
  INSERT the commands into an outbox
COMMIT
-- after commit: carry out the outbox
```

1. **Persist atomically.** State, step records and commands commit together
   with the event. Your own writes can join the same transaction: start a
   run in the transaction that records the webhook, or deliver an approval
   in the transaction that stores it.
2. **Act after commit.** Never dispatch before commit. The outbox makes the
   crash window between commit and dispatch harmless.
3. **Deliver at least once.** Retry delivering a result until it is applied.
   Apply ignores duplicates and results for attempts that are no longer
   wanted.
4. **Run attempts idempotently.** A worker can die after doing the work and
   before reporting it, so an attempt can be dispatched again. Key side
   effects outside the host on the unit key (below), not on the StepID.
5. **Pin the definition.** Store definitions by `Definition.Hash()` and give
   each run the one it started with. `Apply` returns `ErrDefinitionMismatch`
   for a state from another definition.

A run starts with `Event{Kind: EventStart, Input: ...}`. The engine has no
notion of what caused the run: that is your metadata. If a run is started by
an event, put the event into the input under a convention of your own, such
as `{event: <provider payload>, meta: {type, source}}`, which the definition
reads as `input.event.pull_request.number`. A scheduled or manual run may have
no input.

### What Apply returns

`Apply(def, st, ev)` returns the new state and the commands for the host. It
never modifies `st`. For a duplicate or late event (a result for an attempt
that settled, timed out, was cancelled or was retried, or any event on a
finished run) it returns the same `st` pointer and no commands: skip the
write when `next == st`.

It returns an error only for host mistakes, and the state unchanged: a
definition not made by `Load` (`ErrNotValidated`), a state from another
definition (`ErrDefinitionMismatch`), an event before the start
(`ErrNotStarted`), a malformed or unknown StepID (`ErrUnknownStep`), a
malformed event (`ErrInvalidEvent`), an input that does not match the
definition's input schema (`ErrInvalidInput`). Treat them as permanent for
that event. The run's own failures are `RunFailed` commands.

| Command | Host action |
| --- | --- |
| `DispatchStep` | Run the attempt not before `NotBefore` (retry backoff). Report `StepCompleted` with the output, or `StepFailed` with a `StepError`. |
| `ScheduleTimer` | Deliver `TimerFired` for `StepID` at `NotBefore`. Used by `sleep` and `timeout`. |
| `CancelStep` | The attempt's result is no longer wanted. Stop it if you can and drop its pending timer. A late result is ignored. |
| `RunCompleted`, `RunFailed`, `RunCancelled` | The run is over, its `on_failure` and `finally` steps included. Notify whoever waits on it. Alert on `FinallyErrors`: cleanup that failed or did not run. |

A step's `timeout` timer shares its attempt's StepID. Once that attempt
settles, the timer is obsolete: drop it if you can. A host that lets it fire
anyway is safe, since Apply ignores a `TimerFired` for a settled attempt.

### Identity keys

A `StepID` names one attempt:
`<path>/<step>[@<visit>][[<index>]]#<attempt>`, such as `main/verify[3]#1`
or `main.tests/lint@2#1`. on_failure steps run on the path `failure`
(`failure/page#1`) and finally steps on the path `finally`
(`finally/release#1`). A StepID is unique within its run only, so every key
outside the run includes the run ID. A host needs two keys:

- **The attempt key**, run ID + `StepID`: the job, the step record and the
  deduplication of results. A retry is a new attempt with a new StepID.
- **The unit key**, run ID + `StepID.Unit()` (`main/verify[3]`): the same for
  every attempt at the unit, so a retry keeps it. Use it as the idempotency
  key for side effects outside the host, such as a payment provider's: an
  attempt that charged a card and then timed out must not charge it again
  on the retry.

`StepID.Step()` is the step's name and `StepID.Attempt()` the attempt
number.

### Status and time

A run is `running` until its outcome is decided, then `completing`,
`failing` or `cancelling` while its `on_failure` and `finally` steps run (a
run without them goes straight to the terminal status), then `completed`,
`failed` or `cancelled`. Only those three are `Terminal()`; `Outcome()` gives
the one a finishing run is headed for. Keep carrying out commands until the
status is terminal: a cancelled run still dispatches its finally steps.
`State.InFlight()` and `Outstanding` include them. A failing finally step
never changes the status; it is in `State.FinallyErrors` and the terminal
command's `FinallyErrors`.

`Event.At` is the event's time, but a run's clock never goes back. Apply uses
the later of `Event.At` and `State.UpdatedAt`, so a result stamped by a
worker with a slow clock cannot settle a step before it started or schedule
a timer early.

### Cancel and Abort

A cancel (`EventCancel` with a `Reason`) cancels every attempt in flight,
skips `on_failure`, and runs the finally steps. A cancel that arrives while
on_failure steps run cancels them, and the run is cancelled.

Cancel is idempotent. A cancel decides the outcome once: delivering it again,
as a durable host may, changes nothing, and the finally steps it started run
to the end. A cancel that arrives while finally steps run after the run
completed or failed is also a no-op. To stop cleanup, deliver
`Event{Kind: EventCancel, Force: true}`: it stops the finally step that is
running, or skips the finally steps if they have not started, and reports
each in `FinallyErrors` with type `cancelled`. The outcome stays as it was
(a completed run stays completed, with its outputs).

`Abort(st, err, at)` fails a run a host cannot advance with Apply, such as one
whose definition no longer loads. It needs no definition, so it runs neither
`on_failure` nor `finally`. It returns a `CancelStep` for each attempt in
flight (with its `Attempt`, without `StepKind`) and a `RunFailed` with `err`,
which must not be nil. It clears `Outputs` and `Reason`: a run aborted while
its finally steps run has neither completed nor been cancelled. A run that
has not started or has finished is returned unchanged, with no commands.
Prefer delivering an event; Abort is the last resort of a host that has
retried and given up.

### State

The exported fields are the supported read view: `DefinitionHash`, `Status`,
`Input`, `StartedAt`, `FinishedAt`, `UpdatedAt`, `Outputs`, `Error`,
`Reason`, `FinallyErrors` and `Bytes`, plus `State.InFlight()` and
`State.Unit(id)`. The rest (paths, visits, joins) is the engine's
bookkeeping: serialized with the state, but not API. State is not a history.
Keep your own record of attempts.

Outputs live in the state because expressions read them. One output is capped
at 256 KiB and what a run holds at once at 8 MiB by default; set other limits
with `Load(data, kinds, workflow.WithLimits(...))` and read them with
`Definition.Limits()`. A step past the cap fails with `output_too_large` or
`state_too_large`. Keep large artifacts elsewhere and return a reference.
Apply drops step visits a loop has superseded, so the state holds a loop's
latest passes (see [Limits](definitions.md#limits)); `State.Unit` returns nil
for a dropped visit or a replaced attempt.

### What Apply guarantees

- **Determinism.** The same ordered event stream gives the same states and
  commands.
- **Success order does not matter.** Successful completions of steps in
  flight at the same time may arrive in any order, and the run reaches the
  same final state and dispatches the same steps. The property tests check
  this over random graphs. It does not hold for failures and timeouts: a
  failure that arrives before a sibling's success fails the run before that
  success can dispatch anything after it, and a timeout races its step's
  result.
- Nothing that settled is dispatched again.
- A step retries only if it declares `retry` and the error is retryable.
- `finally` steps run once whatever the outcome, unless a forced cancel stops
  them or the host aborts the run, and a failure in them never changes it.
- A loop's state holds only its latest passes, so a poll-and-sleep loop can
  run indefinitely.

## Handlers

Both hosts in this repository call the same handler type, from the root
package, so a handler tested with `local` runs on the postgres worker
unchanged:

```go
// package workflow
type Job struct {
    RunID  string // the host's ID for the run
    Claims int    // times a host has handed this attempt to a handler, this one included
    Command       // the DispatchStep: StepID, Step, StepKind, Params, Config, Item, ...
}
type Handler func(ctx context.Context, job Job) (any, error)
```

- Return the output, or an error. A `*StepError` sets the error type and
  whether it is retryable; any other error is a retryable error of type
  `error` (`StepErrorOf`). A step is retried only if it declares `retry`.
  A missing handler fails the attempt with `unknown_kind` and a panic with
  `panic`, neither retryable.
- Return `workflow.ErrDeferred` when the result arrives later: an agent turn
  on another service, a human approval, a webhook. The step stays in flight
  until someone delivers its `StepCompleted` or `StepFailed` (with
  `job.StepID`), or until its timeout fires. Give such a step a `timeout`.
- `ctx` is cancelled when the attempt is no longer wanted: the run was
  cancelled or failed, the step timed out, or the host lost its claim.
- `Claims > 1` means an earlier call may have done part of the work: postgres
  reclaimed the job of a worker that lost its lease, or local resumed a saved
  run. Key side effects on `job.RunID` and `job.StepID.Unit()`.

## Kinds and their types

`Load(data, kinds)` asks the host which step kinds exist (`Known`) and checks
each step's configuration (`ValidateConfig`). A host that also knows what a
kind takes and returns implements the optional `KindSchemas`:

```go
func (k hostKinds) Schemas(kind string, cfg map[string]any) (params, output *workflow.Schema, err error) {
    switch kind {
    case "action":
        tool := k.tools[cfg["action"].(string)]   // e.g. github.commit.set_status
        return tool.InputSchema, tool.OutputSchema, nil
    }
    return nil, nil, nil
}
```

Then a `with:` block that misses a required parameter, passes the wrong type,
uses a value outside an enum (`state: pendng`) or a value that may be nil
fails validation, and expressions that read an action's result are checked
against its output schema. A step's own `output:` overrides the host's. Build
schemas with `workflow.ParseSchema(jsonSchemaBytes)`.

At run time Apply validates params before it emits `DispatchStep` (a mismatch
fails the step with `invalid_params`, and the host never sees it) and the
reported output on `StepCompleted` (`invalid_output`, not retried). Hosts that
ask a model for structured output pass `Command.OutputSchema` along; it is
plain JSON Schema with refs inlined.

## Deploys and versioning

The definition hash covers the authored document only. Load options and the
host's kinds are not part of it, and kind schemas are not pinned: every load,
including a postgres reload after a deploy, uses the host's current
`KindSchemas`, because the host's handlers are its current code. So:

- A kind's params and output schema is the host's contract. Change it
  compatibly (add optional fields), or ship an incompatible change as a new
  kind name (`payments.charge.v2`) and keep the old kind until its runs
  finish.
- A definition that no longer loads against the current kinds makes its runs
  unavailable. In postgres that is `ErrDefinitionUnavailable`, then an abort
  after `MaxClaims`.
- Persisted State is JSON the engine reads back. Before v1.0 a minor release
  may change its format; the release notes will say so.
- Handlers must tolerate an attempt being dispatched again.

## Generated workflows

An agent can add work to a run while it runs: one step writes a workflow
document, and the next step runs it as a child run. The parent's
definition never changes, so everything `Load` proved about it still
holds, and the child is checked by `Load` like any other definition. A
`switch` or `when` on the child's outputs decides what the parent does
next, such as plan again or stop. Changing the graph of a running run is
not supported: its joins, loops and types are proved over the graph it
started with.

The host kind that runs a generated child:

1. Loads the document with the host's kinds and
   `workflow.RequireOutputs(job.OutputSchema)`. A child whose outputs
   cannot fill the step then fails to load, before any of its work runs.
2. On a load failure, fails the step with a `StepError` of a type of its
   own, such as `invalid_workflow`, with the problems as the message. A
   `catch` on that type can route back to the step that writes the plan,
   which reads the problems as `<step>.error.message`. Bound that loop with
   `max_visits`.
3. Starts the child with an ID made from `job.RunID` and
   `job.StepID.Unit()`. A retry or a reclaimed job then finds the same
   child, and a second Start of it changes nothing.
4. Completes the step with the child's outputs, or fails it when the child
   does not complete.

The child is a run of its own, with its own state, cancel and finally
steps. In local, the handler can run the child and return its outputs
([examples/planner](../examples/planner)). A durable host starts the
child, returns `ErrDeferred`, and delivers the step's result when the
child finishes ([examples/toolcall](../examples/toolcall) shows the
pattern in local). When the parent cancels the step, cancel the child.

On a durable host, deliver the result from the store's finish hook. It
runs in the transaction that finishes the child, on every path (a worker's
settle or abort, `Start`, `Deliver`, `Cancel`, and the SQL forms), so the
parent resumes atomically with the child's end. The host kind records the
child's parent in the application's own table, in the same database:

```go
resume := func(ctx context.Context, tx durable.SQLTx, f durable.Finished) error {
    parent, ok, err := lookupParent(ctx, tx, f.RunID) // the app's table, same tx
    if err != nil || !ok {
        return err
    }
    ev := workflow.Event{Kind: workflow.EventStepCompleted, StepID: parent.StepID, Output: f.Outputs}
    if f.Status != workflow.StatusCompleted {
        ev = workflow.Event{Kind: workflow.EventStepFailed, StepID: parent.StepID,
            Error: &workflow.StepError{Type: "child_" + string(f.Status)}}
    }
    _, err = store.DeliverSQLTx(ctx, tx, parent.RunID, ev)
    return err
}
```

Pass it with `WithOnFinishedSQL(resume)`. The postgres and sqlite modules
both take it, so the code is the same on both. An error from the hook rolls
the child's finish back, and a worker retries its settle.

## Hosts with their own runtime

A host that already runs durable work (agent turns, action jobs, leases,
wake-ups) uses only the root package. Map each `DispatchStep` to a job keyed
by run ID and StepID, each job's completion to an event, and `ScheduleTimer`
to a wake-up. `Outstanding(def, state)` rebuilds the commands for in-flight
work if a host ever needs to re-drive it; for a run that has not started or
has finished, it returns none.

Long-running and external steps need nothing special: a step is in flight
until its result is delivered. A step that waits for a person or a webhook,
or one that starts another run, is a host kind whose completion event
arrives later. Give it a `timeout`.

## local

`local` is for unit tests, CLIs and single-process programs. For
development that should behave like production, use the `sqlite` module.

`local.Runner{Handlers, Save}` carries out commands in-process: a
goroutine per dispatch, `time.AfterFunc` per timer, a context cancel per
`CancelStep`.

```go
run, err := runner.Start(ctx, "run-1", def, input)   // err: the start was rejected
// ... later, for a step whose handler returned workflow.ErrDeferred:
err = run.Deliver(workflow.Event{Kind: workflow.EventStepCompleted, StepID: id, Output: out})
st, err := run.Wait()
```

- `Start(ctx, runID, def, input)` returns a `*local.Run`;
  `Run(ctx, runID, def, input)` is `Start` then `Wait`.
- `Run.Wait()` returns the last state, with a nil error once the run has
  finished (read `Status` and `Error` for the outcome). `Run.Done()` is
  closed when it finishes or stops.
- `Run.Deliver(ev)` applies an event, such as a deferred step's result. A
  zero `At` is set to now. It returns Apply's error for a rejected event, and
  `local.ErrNotRunning` after the run has finished or stopped.
- `Run.Cancel(reason)` cancels the run. Its finally steps run to the end;
  deliver a cancel with `Force` to stop them. Cancelling again changes
  nothing.
- Cancelling `ctx` stops the run without cancelling it: `Wait` returns the
  running state with `ctx.Err()`.
- `Save`, if set, is called with every state that changed, before its
  commands are carried out. `Resume(ctx, runID, def, st)` continues a saved
  run and dispatches its in-flight work again, deferred steps included, with
  `Job.Claims == 2`.

The `workflow` CLI cancels the run on an interrupt; its finally steps still
run, and a second interrupt quits.

## postgres

The `postgres` module is a complete durable host.

```go
store, _ := postgres.New(pool, kinds)          // tables live in schema "workflow"
_ = store.Migrate(ctx)                         // safe from every instance at startup

w := &postgres.Worker{Store: store, Handlers: map[string]workflow.Handler{
    "agent":  runAgent,
    "action": runAction,
}}
go w.Run(ctx)                                  // run as many as you like

res, err := store.Start(ctx, postgres.NewRun{ID: "pr-42-abc123", Definition: def,
    Input: map[string]any{"event": payload, "meta": map[string]any{"type": "pull_request.opened"}}})
```

Tables: `definitions` (by hash), `runs` (state jsonb plus queryable copies of
status, error, outputs and metadata), `steps` (one record per attempt, keyed
by StepID; `kind` is the step kind), `events` (every applied event, in
order), `jobs` (the outbox) and `schema_migrations`. A job is the outbox row
for an attempt or a timer: `jobs.kind` says which (`step` or `timer`), and
`jobs.step_kind` holds the step kind. A claim is one worker's lease on a
job. `Migrate` applies numbered migrations once each, in one transaction
under an advisory lock, so concurrent calls are safe; it refuses a database
newer than the package.

The types and errors a durable host shares with every backend live in
package `durable` (in the root module), and `postgres` re-exports each one
under the same name: `postgres.Result` is `durable.Result`, and
`postgres.ErrRunNotFound` is `durable.ErrRunNotFound`. So `%T` prints
`*durable.Result`, and the errors' messages start with `workflow:`. Import
`durable` only for code that should not name a backend.

The `postgres` module builds on internal packages of the root module, so
upgrade the two together, to the same release. Updating only
`github.com/deepnoodle-ai/workflow` can stop `postgres` from compiling.

`Steps(ctx, runID)` returns `StepRecord`s with `StepKind` and a `Status` of
`StepDispatched`, `StepCompleted`, `StepFailed` or `StepCancelled`. `GetRun`
returns a `Run` with its `DefinitionHash`, `Status`, `State` and metadata;
`Events` lists the applied events.

### Starting runs and delivering events

`Start`, `Deliver`, `Cancel`, and their `Tx` forms (`StartTx`, `DeliverTx`,
`CancelTx`), which take your `pgx.Tx`, return a `Result` (for a
`database/sql` transaction, see the next section):

- `RunID`: the run's ID, `NewRun.ID` or the one Start generated: a UUIDv7
  by default, or from your function with `postgres.WithRunIDs(fn)`.
- `State` and `Commands`: the commands are already in the outbox, committed
  with the state; workers carry them out, so do not run them yourself.
- `Applied`: false when the event changed nothing: a duplicate, a result for
  an attempt that is no longer wanted, an event on a finished run, a Start of
  an existing run, or a second cancel. An endpoint that records an approval
  answers 200 either way, and can say "already decided" when `Applied` is
  false.

A handler that hands work to another system returns `workflow.ErrDeferred`;
the job is done, the step stays in flight, and whoever finishes the work
calls `store.Deliver` with the run ID and the StepID. `Cancel` is idempotent;
to stop the finally steps of a cancelled run, `Deliver` an `EventCancel` with
`Force`.

To learn that a run finished, either:

- pass `postgres.WithOnFinished(fn)`: `fn` runs inside the transaction that
  completes, fails or cancels the run, once per run, with its outputs, error
  or reason, and `FinallyErrors`. A run with `finally` steps finishes when
  they are done, not when its outcome is decided. Write your own records or
  outbox rows there. An error from `fn` rolls the event back.
- `LISTEN` on `store.RunsChannel()` (`<schema>_runs`): after each commit that
  changes a run's status, the store notifies
  `{"run_id": "...", "status": "..."}`. Notifications are best effort; read
  the run with `GetRun`.

`store.NotifyChannel()` (`<schema>_jobs`) is the channel the store notifies,
with the run ID, when it writes jobs; workers `LISTEN` on it.

The store loads every definition itself, from its document, with its kinds
and the limits from `postgres.WithLimits` (defaults otherwise), whether the
definition came from `Start` or from the database. That is the only source of
limits for postgres runs: the limits `NewRun.Definition` was loaded with do
not apply.

An output Postgres cannot store as jsonb (a NUL character, or a value JSON
cannot encode such as NaN) fails the step with `invalid_output` instead of
rolling back and running the handler again; NUL characters in error messages
and cancel reasons become U+FFFD. `Start` returns `ErrUnstorable` for an input
or metadata with a NUL.

### In a database/sql transaction

An application whose data goes through `database/sql` (an ORM such as Ent,
sqlc, or plain `*sql.Tx`) uses `StartSQLTx`, `DeliverSQLTx` and
`CancelSQLTx`. They behave like the `pgx.Tx` forms and return the same
`Result` and errors, in your transaction, so you need no outbox of your own
to start a run with your record:

```go
db := stdlib.OpenDBFromPool(pool) // database/sql on the store's pool
tx, err := db.BeginTx(ctx, nil)
_, err = tx.ExecContext(ctx, `INSERT INTO orders (id) VALUES ($1)`, id)
res, err := store.StartSQLTx(ctx, tx, postgres.NewRun{ID: "order-" + id, Definition: def})
err = tx.Commit() // the order and the run, or neither
```

They take a `postgres.SQLTx`: `ExecContext`, `QueryContext`, `Commit` and
`Rollback`. A `*sql.Tx` has them. So does an Ent `*Tx` generated with the
`sql/execquery` feature, so you pass Ent's transaction itself:
`store.StartSQLTx(ctx, tx, run)` after `tx, err := client.Tx(ctx)`. The
store never calls `Commit` or `Rollback`; a `*sql.DB`, which commits each
statement alone, does not compile.

The transaction must be on the store's Postgres database, through pgx's
stdlib driver (`github.com/jackc/pgx/v5/stdlib`), which is what the tests
cover. The store passes only plain argument values. If your application
opens the database with lib/pq, switch with one line:
`sql.Open("pgx", dsn)` (or `entsql.Open("pgx", dsn)`) with a blank import
of `github.com/jackc/pgx/v5/stdlib`. A transaction on another database
fails at the store's first statement.

A run can finish in your transaction: a cancel, or a result that ends it.
Use `postgres.WithOnFinishedSQL` as the store's one finish hook. It gets
your own transaction value when a SQL form finished the run (assert it back
to `*ent.Tx` to use Ent). A store with no `WithOnFinished` opens its own
transactions, a worker's included, through `database/sql` on its own pool,
so the hook runs on every path that finishes a run, with the `*sql.Tx` the
store opened. The `sqlite` module has the same option, so the same hook
runs in development and in production:

```go
store, err := postgres.New(pool, kinds,
	postgres.WithOnFinishedSQL(func(ctx context.Context, tx durable.SQLTx, f durable.Finished) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO outcomes (run_id, status) VALUES ($1, $2)`,
			f.RunID, string(f.Status))
		return err
	}))
```

Such a store refuses `StartTx`, `DeliverTx` and `CancelTx`, whose `pgx.Tx`
the hook cannot take. `WithOnFinished` with a `pgx.Tx` remains for code on
pgx alone. A store with it refuses the SQL forms until it also has
`WithOnFinishedSQL`; with both, a run that finishes in a `pgx.Tx` calls
`WithOnFinished`, and one that finishes in a `database/sql` transaction
calls `WithOnFinishedSQL`.

### Workers

Workers claim due jobs with `FOR UPDATE SKIP LOCKED` and a lease, and wake on
`NotifyChannel` or a poll. A worker claims only the step kinds it has
handlers for (and any timer), so agent workers and action workers can share
one outbox. A job of a kind no worker handles waits in the outbox; nothing
checks at startup that the workers cover every kind. Give such a step a
`timeout` to bound the wait: timers are claimed by every worker.

A heartbeat extends the lease while the handler runs. If the job disappears
(the run was cancelled or the step settled another way), another worker
reclaimed it, or no heartbeat has succeeded for a whole lease (the worker is
cut off from the database), the handler's context is cancelled and its result
dropped. Each claim takes a new fencing token (`jobs.fence`, which only
grows), and results are delivered in a transaction that deletes the job only
if it still holds that token, so a worker whose job was claimed again cannot
apply a stale result.

Delivering a result retries transient errors with backoff until the lease
runs out; past it another worker may claim the job, and the attempt runs
again. Cancelling `Run`'s context is a hard stop: every handler's context is
cancelled at once. A handler that returns a result, a hand-off or a
non-retryable error is still recorded, within the lease; one that returns
the context's error or a retryable error is given back for another worker
to run, without counting as a claim. So `Run` returns within about one lease
of cancellation. Timers that cannot fire during shutdown are given back too.

`Shutdown(ctx)` is the graceful stop, like `http.Server.Shutdown`. The
worker stops claiming at once, steps and timers alike, for every `Run` call
on it. Its active jobs (claimed and not yet settled or given back;
`Active()` counts them, and a claim query in progress as one) keep running
and heartbeating until `ctx` is done. Then their contexts are cancelled and
they end as on a hard stop, except that each write after that point takes at
most a second, and a result that cannot be recorded by then is given back. A
hand-off whose final write fails is left to its lease instead, because
giving it back would run its handler again at once. `Shutdown` returns when
every handler has returned and every claim is settled or given back. `Run`
then returns nil. `Shutdown` returns nil if every active job finished in
time, else `ctx`'s error. It waits without limit for a handler that ignores
its context. A claim query that hangs past the deadline is cancelled a
second later; if its claim had already committed, its jobs are left to their
lease, and the claim counts toward `MaxClaims`. Give `Run` a context of its
own, which you cancel only for a hard stop: if `Run`'s context ends before
`Shutdown` finishes (for example, because it is the context that `SIGTERM`
cancels), `Run` stops hard and cancels its active jobs at once, and
`Shutdown` logs a warning and returns `postgres.ErrHardStop`, as does every
later `Shutdown` on that worker. A worker that was shut down does not start
again.

`MayClaim func() bool` closes the same claim gate for a while. While it
returns false, the worker claims nothing, timers included, and gives back a
claim that returns after it closed; its active jobs keep running and
settle. Once the gate is closed, `Active() == 0` means the worker holds no
claim. Claiming resumes at the next wake-up or poll once it returns true.
An application that detects it was superseded by a newer deploy sets it,
so an old instance finishes its work but takes no more. Detection is the
application's: the worker has no notion of revisions. With the gate closed
on every worker, nothing runs, timers included.

A step claimed `MaxClaims` times without finishing fails with type
`abandoned`; a claim given back on graceful shutdown does not count. A timer
is bounded the same way: one that cannot be fired `MaxClaims` times fails the
run with `abandoned`.

A run whose definition the store cannot load (its kinds lack a kind the
definition uses, as in a deploy that has not reached every instance, or the
document no longer validates) is a configuration problem. `Start` and
`Deliver` return `ErrDefinitionUnavailable`, and `Start` starts nothing. A worker runs nothing for such a run: it logs an
error and waits a lease, doubling, for the configuration to be fixed, and
after `MaxClaims` claims it fails the run with type `definition_unavailable`,
drops its jobs and records an `aborted` entry in the event log. Such an abort
uses `workflow.Abort`, so it runs neither `on_failure` nor `finally` steps,
and so do the other aborts: a timer that could not be fired, or a step that
could not even be failed, after `MaxClaims` claims (type `abandoned`). A
step failed as `abandoned` the normal way is an ordinary step failure; in a
finally step it is reported in `FinallyErrors` and the next one runs.

Metrics: the worker imports no metrics library. Set the functions you
need on `Worker.Observer` and forward them to your own counters and
histograms. Each claimed job fires `Claimed`, then exactly one ending
event: `Settled` (the result was recorded; its `Settlement` has the
`Outcome` `completed`, `failed`, `deferred` or `fired`, the handler's
`Duration`, and the step error of a failure; a hand-off is `deferred` even
when its result arrived through `Store.Deliver` first, while the claim's
lease was live), `Cancelled` (the run no longer wants the attempt: a
cancel, a timeout, the run ended, its result arrived while the handler ran,
or the run rejected the result; a job found gone counts here only while
the claim's lease is live), `LeaseLost` (another worker took the claim,
even one that already finished the job, the job was found gone after the
lease ran out, no heartbeat succeeded for a whole lease, or a database
error left the job to its lease; it runs again or already ran),
`GivenBack` (shutdown gave the claim back, the claim gate closed while
the claim query ran, or the request that `RunOnce` or `Execute` ran in
ended) or `Abandoned` (on claim
`MaxClaims+1` the step failed as `abandoned` or the run failed; a run
whose definition cannot be loaded fails on claim `MaxClaims`). The one
exception is a claim postponed because the run's definition cannot be
loaded: it has no ending event, so `Claimed` minus the ending events is
not the work in flight; `Active()` is. `ClaimError` reports a failed claim
query, not one that shutdown cancelled. Label
with the job's `Kind` and `StepKind`. The functions run synchronously on
the worker's goroutines, concurrently, so keep them fast: a slow `Claimed`
delays the handler and its first heartbeat. A panic in one is not recovered.

`Store.QueueStats` reports each queue's lag: for every queue with a job in
the outbox (all timers, or the steps of one `StepKind`), the number of due
jobs (their time has come and no live lease holds them) and how long the
oldest has been due, by the database's clock. Alert on `OldestDue`: it
catches a stalled worker, and a step kind no worker handles. Its cost grows
with the number of due jobs (a million take a few hundred milliseconds), so
poll it from one process, every 15 seconds or so.

Connections: each worker holds one dedicated connection for `LISTEN`,
outside the pool, and closes it when `Run` returns. Everything else
(claims, heartbeats, settles) borrows pool connections briefly, never across
a handler, so a pool of any size works; size it for `Concurrency` plus your
application's own queries to avoid waiting. `LISTEN` needs a session: behind
a transaction-mode pooler (Neon's `-pooler` endpoint, PgBouncer) set
`Worker.ListenConfig` to the direct endpoint, or the worker only wakes every
`Poll`.

On Cloud Run, an instance gets `SIGTERM` and is killed 10 seconds later.
`Lease` is independent of that grace period; keep its 30 s default. On
`SIGTERM`, call `Shutdown` with a deadline of about 7 s. After the deadline,
the final writes take at most 2 s (one attempt to record a result, then a
give-back), which leaves 1 s for handlers to return and the process to
exit. A handler that takes more than about 1 s to return after its context
is cancelled pushes the exit past the kill, and its claim then counts
toward `MaxClaims`:

```go
w := &postgres.Worker{Store: store, Handlers: handlers}
go w.Run(context.Background()) // cancel only for a hard stop

sigs, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
defer stop()
<-sigs.Done()
ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
defer cancel()
_ = w.Shutdown(ctx) // ctx's error if the deadline cut active jobs off
```

### Scale to zero

A job runs only while some process claims it, and `Run` is a loop that
must keep running. On a platform that scales to zero, or that gives CPU
only during requests (Cloud Run with request-based billing), it does not
run between requests: a `sleep: 24h` fires only when the next instance
happens to start, and an instance with no request in flight can be stopped
while a step runs. Pick one of three setups:

1. **A warm worker.** One instance with CPU always allocated (minimum
   instances 1, instance-based billing) runs `Run`. It has the lowest
   latency and runs steps of any length.
2. **A scheduled `RunOnce`.** `RunOnce(ctx)` runs the jobs that are due
   now and returns. Call it from an endpoint that Cloud Scheduler calls
   every minute, or run it in a Cloud Run job.
3. **One request per job.** Push each job to Cloud Tasks; the task's
   handler calls `Execute(ctx, ref)`. The platform sees each step as a
   request, and drains it on shutdown. Keep a scheduled `RunOnce` as the
   safety net for a push that was lost.

**What `RunOnce` runs.** The jobs due when it starts, and the steps those
jobs dispatch with no delay, so a run of several steps in a row finishes
in one call. A timer that comes due during the call, a run that another
caller starts, a result delivered from outside, and a child run started by
a handler wait for the next call. So `RunOnce` always returns, and with a
one-minute schedule a new run waits up to a minute before its first step.
It runs up to `Concurrency` jobs at once in each call; overlapping calls are
safe. It does not `LISTEN`. A job whose result could not be recorded, or
that waits for its run's definition, stays in the outbox and does not stop
the call. Once no job is in flight, `RunOnce` returns nil if `Shutdown`
stopped it, else `ctx`'s error if `ctx` ended, else the first claim or job
error, else nil. A job that another worker took over is not an error.

```go
http.HandleFunc("POST /tasks/drain", func(rw http.ResponseWriter, r *http.Request) {
    // Stop before the request's deadline. 9 minutes needs Cloud Scheduler's
    // attempt deadline and Cloud Run's request timeout raised to 10
    // minutes; with the defaults, stay under 3 minutes.
    ctx, cancel := context.WithTimeout(r.Context(), 9*time.Minute)
    defer cancel()
    if err := worker.RunOnce(ctx); err != nil && ctx.Err() == nil {
        http.Error(rw, err.Error(), http.StatusInternalServerError) // a database error
        return
    }
    rw.WriteHeader(http.StatusNoContent) // drained, or stopped at the deadline
})
```

**Request deadlines.** With `RunOnce` from an endpoint, or `Execute`, a step
runs inside a request, and the request has a deadline:

| Deadline | Default | Most |
| --- | --- | --- |
| Cloud Scheduler attempt deadline (HTTP target) | 3 minutes | 30 minutes |
| Cloud Tasks dispatch deadline (HTTP target) | 10 minutes | 30 minutes |
| Cloud Run request timeout | 5 minutes | 60 minutes |

When the context given to `RunOnce` or `Execute` ends, the handlers are
cancelled. A handler that then returns the context's error or a retryable
error has its claim given back at once, so the next call can take it.
Unlike a `Shutdown`, such a claim counts toward `MaxClaims`: a step that
always outlives the deadline fails as `abandoned` instead of running again
forever. A handler that ignores its context runs on after the request
ends, and the call waits for it. Its heartbeat extends the job's lease
until it returns, but only while the process gets CPU. With Cloud Run's
request-based billing, CPU is throttled once the request ends: the
heartbeat can stall, the lease can run out, and another claim can run the
step again beside the first; the fence then drops the first result. So
keep CPU allocated until the handler returns (instance-based billing), or
accept that such a step can run twice. Run a step longer than the
deadline on a warm worker, in a Cloud Run job (whose task can run for
hours), or hand it off with `workflow.ErrDeferred`. Give `RunOnce` a
deadline a little under the caller's, and treat reaching it as progress:
the next call goes on.

**One request per job.** A `JobRef` names one job: `RunID`, `StepID`, and
`Timer` (a timer and the attempt it guards share a StepID), plus
`StepKind`, to route a step to a service that handles it, and `RunAt`, the
time it is due (zero means at once). Send it as the task's JSON body, with
`RunAt` as the schedule time. Every call that writes jobs returns their
references; push them after the commit:

| Call | Jobs it writes | References |
| --- | --- | --- |
| `Start`, `StartTx` | the first steps | `Result.Jobs()` |
| `Deliver`, `DeliverTx` | the next steps, after a hand-off or an approval | `Result.Jobs()` |
| `Cancel`, `CancelTx` | the `finally` steps | `Result.Jobs()` |
| `Execute` | its run's next steps and timers | `Execution.Jobs` |
| `RunOnce` | the same, for each job it runs | none: it runs the steps itself, and a later call fires the timers it writes |
| a `DeliverTx` inside `WithOnFinished` | a parent's next steps, when its child run ends | none |

For a `Tx` method, push after your own commit. A `WithOnFinished` hook runs
inside the worker's transaction, so the jobs it writes are not returned: a
parent that waits on a child run resumes at the next scheduled `RunOnce`.
A step's `timeout` timer is a job too; its task usually finds the attempt
settled and answers 200.

```go
http.HandleFunc("POST /tasks/job", func(rw http.ResponseWriter, r *http.Request) {
    var ref postgres.JobRef
    if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
        http.Error(rw, err.Error(), http.StatusBadRequest)
        return
    }
    // Stop before the request's deadline, the lesser of the Cloud Tasks
    // dispatch deadline and Cloud Run's request timeout (with the defaults,
    // 5 minutes). Otherwise the step can outlive the request and run on
    // with throttled CPU; if its heartbeat stalls, the step can run twice.
    ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
    defer cancel()
    ex, err := worker.Execute(ctx, ref)
    if err != nil {
        http.Error(rw, err.Error(), http.StatusServiceUnavailable) // the task is retried
        return
    }
    for _, next := range ex.Jobs {
        enqueue(r.Context(), next) // your Cloud Tasks client
    }
    rw.WriteHeader(http.StatusOK)
})
```

`Execute` claims the job with a new fence, runs its handler with a
heartbeat, and records the result, as `Run` does. A nil error means done
(`Settled` is true) or nothing to do: the job is gone, because it settled,
its run finished, or a task was delivered twice. Any error means try again
later. It is `ErrNotClaimable` when another worker holds the job's lease,
the job is not due yet, this worker has no handler for its step kind, or
the worker is not claiming (`Shutdown`, `MayClaim`); `Execute` claims
nothing then. Postgres stays the source of truth: the task queue only
carries references, and a retried task converges on the fence.

`Execute` is not limited by `Concurrency`. Set Cloud Run's concurrency to
the number of handlers one instance can run (the default is 80), and size
the pool for that number plus your application's own queries. `Shutdown`
waits for `Execute` calls in flight and `Active()` counts their jobs, so
handle `SIGTERM` as above; a handler that the deadline cuts off is given
back without counting, and its `Execute` returns an error, so the task is
retried on another instance.

## sqlite

The `sqlite` module is the postgres host on SQLite, with the same API names
and behaviour: develop on a file, run on Postgres in production. It takes a
`*sql.DB` and imports no driver. It is tested with modernc.org/sqlite (pure
Go, driver name `sqlite`) and mattn/go-sqlite3 (cgo, `sqlite3`), and needs
SQLite 3.37 or newer.

```go
db, _ := sql.Open("sqlite", "file:dev.db?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate")
store, _ := sqlite.New(db, kinds)   // tables are workflow_runs, workflow_jobs, ...
_ = store.Migrate(ctx)

w := &sqlite.Worker{Store: store, Handlers: handlers}
go w.Run(ctx)

res, err := store.Start(ctx, sqlite.NewRun{ID: "order-42", Definition: def, Input: input})
```

The DSN works with both drivers. `_busy_timeout` is required: without it,
two writes that overlap fail with `SQLITE_BUSY`, so `Migrate` and
`Worker.Run` refuse to start. WAL lets processes on one host share the
file; never put it on a network file system. An in-memory database needs
`db.SetMaxOpenConns(1)`, since each connection would open its own. Open a
new file once (run `Migrate` in one process) before several processes open
it at the same moment: the file's first switch to WAL can fail with
`SQLITE_BUSY` then.

`Store` has `Migrate`, `Start`, `Deliver`, `Cancel`, `StartSQLTx`,
`DeliverSQLTx`, `CancelSQLTx`, `GetRun`, `Steps`, `Events` and
`QueueStats`; `New` takes `WithSchema` (a table prefix: `name_`),
`WithLimits`, `WithRunIDs` and `WithOnFinishedSQL`. `Worker` has the
postgres `Worker`'s fields except `ListenConfig`, and the same `Run`,
`Shutdown`, `Active`, `RunOnce` and `Execute`. Types and errors are package
`durable`'s, so code written against `durable` takes either store's values.
Upgrade the root and sqlite modules together, as for postgres.

A transaction you pass to `StartSQLTx` holds the database's write lock from
its first write until `Commit`, and that blocks every write, the workers'
claims, heartbeats and settles included. Keep it short and make no network
calls inside it. Open it with `_txlock=immediate`: a transaction that reads
before its first write can fail with `SQLITE_BUSY` when it writes. The
store's own transactions write first, so they do not depend on it.

Where the backends differ:

| | Postgres | SQLite |
| --- | --- | --- |
| Caller transactions | `StartTx` etc. with `pgx.Tx`, and the `SQLTx` forms | the `SQLTx` forms only |
| Finish hook | `WithOnFinishedSQL` (same code as SQLite), or `WithOnFinished` with a `pgx.Tx` | `WithOnFinishedSQL` |
| Run status notifications | `RunsChannel` | none; use the finish hook |
| Wake-up | `LISTEN` on `NotifyChannel`, else `Poll` | at once for jobs written through the same `Store`, else `Poll` |
| Clock for leases and due times | the database's | the process's (one host) |
| `WithSchema` | a Postgres schema | a table-name prefix |
| What a run lock blocks | that run's row | every write to the database |
| `Shutdown` while another connection holds a lock past the deadline | the result is given back; returns within the deadline plus 1 s per write | nothing can be written, so the claim is left to its lease and counts toward `MaxClaims`; returns within the deadline plus, per write, 1 s on modernc and `busy_timeout` on mattn |
| A cancelled context during a wait for a lock | ends the wait | modernc: ends the wait. mattn: does not; the write waits up to `busy_timeout`. With mattn, set `_busy_timeout=1000` or less if you rely on `Shutdown` deadlines |
| Where it runs | any number of hosts | processes on one host; not a network file system |

SQLite applies Postgres's storage rules: an output with a NUL character, or
one JSON cannot encode, fails the step with `invalid_output`, and `Start`
returns `ErrUnstorable` for such an input. A run that works in development
then does not fail in production for a value Postgres cannot store. SQLite
serializes all writes; it suits development and small single-host
services, and this package claims nothing about its throughput.

