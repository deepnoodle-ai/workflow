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
flight (without `StepKind`) and a `RunFailed` with `err`. Prefer delivering
an event; Abort is the last resort of a host that has retried and given up.

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

## Hosts with their own runtime

A host that already runs durable work (agent turns, action jobs, leases,
wake-ups) uses only the root package. Map each `DispatchStep` to a job keyed
by run ID and StepID, each job's completion to an event, and `ScheduleTimer`
to a wake-up. `Outstanding(def, state)` rebuilds the commands for in-flight
work if a host ever needs to re-drive it.

Long-running and external steps need nothing special: a step is in flight
until its result is delivered. A step that waits for a person or a webhook,
or one that starts another run, is a host kind whose completion event
arrives later. Give it a `timeout`.

## local

`local.Runner{Handlers, Save, Now}` carries out commands in-process: a
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

`Steps(ctx, runID)` returns `StepRecord`s with `StepKind` and a `Status` of
`StepDispatched`, `StepCompleted`, `StepFailed` or `StepCancelled`. `GetRun`
returns a `Run` with its `DefinitionHash`, `Status`, `State` and metadata;
`Events` lists the applied events.

### Starting runs and delivering events

`Start`, `Deliver`, `Cancel`, and their `Tx` forms (`StartTx`, `DeliverTx`,
`CancelTx`), which take your `pgx.Tx`, return a `Result`:

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
again. On shutdown (`ctx` cancelled) a handler that returns a result, a
hand-off or a non-retryable error is still recorded, within the lease; one
that returns the context's error or a retryable error is given back for
another worker to run, without counting as a claim. So `Run` returns within
about one lease of cancellation. Timers that cannot fire during shutdown are
given back too.

A step claimed `MaxClaims` times without finishing fails with type
`abandoned`; a claim given back on graceful shutdown does not count. A timer
is bounded the same way: one that cannot be fired `MaxClaims` times fails the
run with `abandoned`.

A run whose definition the store cannot load (its kinds lack a kind the
definition uses, as in a deploy that has not reached every instance, or the
document no longer validates) is a configuration problem. `Deliver` returns
`ErrDefinitionUnavailable`. A worker runs nothing for such a run: it logs an
error and waits a lease, doubling, for the configuration to be fixed, and
after `MaxClaims` claims it fails the run with type `definition_unavailable`,
drops its jobs and records an `aborted` entry in the event log. Such an abort
uses `workflow.Abort`, so it runs neither `on_failure` nor `finally` steps,
and so do the other aborts: a timer that could not be fired, or a step that
could not even be failed, after `MaxClaims` claims (type `abandoned`). A
step failed as `abandoned` the normal way is an ordinary step failure; in a
finally step it is reported in `FinallyErrors` and the next one runs.

Connections: each worker holds one dedicated connection for `LISTEN`,
outside the pool, and closes it when `Run` returns. Everything else
(claims, heartbeats, settles) borrows pool connections briefly, never across
a handler, so a pool of any size works; size it for `Concurrency` plus your
application's own queries to avoid waiting. `LISTEN` needs a session: behind
a transaction-mode pooler (Neon's `-pooler` endpoint, PgBouncer) set
`Worker.ListenConfig` to the direct endpoint, or the worker only wakes every
`Poll`. On Cloud Run, keep `Lease` within the instance's shutdown grace
period so in-flight results are recorded before it stops.
