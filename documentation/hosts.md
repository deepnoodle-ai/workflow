# Hosts

`workflow.Apply` decides; a host does. This guide covers what a host must do,
and the two hosts in this repository.

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
   wanted, and returns the state unchanged with no commands. Skip the write
   when `next == state`.
4. **Run attempts idempotently.** A worker can die after doing the work and
   before reporting it; the next claim runs the attempt again. Key side
   effects on `Command.StepID`.
5. **Pin the definition.** Store definitions by `Definition.Hash()`.
   `Apply` returns `ErrDefinitionMismatch` for a state from another version.

A run starts with `Event{Kind: EventStart, Input: ...}`. The engine has no
notion of what caused the run: that is your metadata. If a run is started by
an event, put the event into the input under a convention of your own, such
as `{event: <provider payload>, meta: {type, source}}`, which the definition
reads as `input.event.pull_request.number` (see
[Input](definitions.md#input)). A scheduled or manual run may have no input.

Commands:

| Command | Host action |
| --- | --- |
| `DispatchStep` | Run the step not before `NotBefore` (retry backoff). Report `StepCompleted` with the output, or `StepFailed` with a `StepError`. |
| `ScheduleTimer` | Deliver `TimerFired` for `StepID` at `NotBefore`. Used by `sleep` and `timeout`. |
| `CancelStep` | The attempt's result is no longer wanted. Stop it if you can and drop its pending timer. |
| `RunCompleted`, `RunFailed`, `RunCancelled` | The run is over. Notify whoever waits on it. |

A step's `timeout` timer shares its attempt's StepID. Once that attempt
settles (its result is applied, or it gets `CancelStep`), the timer is
obsolete: drop it if you can. A host that lets it fire anyway is safe, since
Apply ignores a `TimerFired` for a settled attempt and returns the state
unchanged.

Time: `Event.At` is the event's time, but a run's clock never goes back.
Apply uses the later of `Event.At` and `State.UpdatedAt`, so a result stamped
by a worker with a slow clock cannot settle a step before it started or
schedule a timer early.

`Apply` returns an error only for host mistakes: a definition not made by `Load`, a
mismatched state, a malformed or unknown StepID, an input that does not match
the definition (`ErrInvalidInput`). The run's own failures are `RunFailed` commands. Treat Apply
errors as permanent for that event.

Size: outputs live in the state because expressions read them. One output is
capped at 256 KiB and what a run holds at once at 8 MiB by default
(`Definition.Limits`). A step past the cap fails with
`output_too_large` or `state_too_large`. Keep large artifacts elsewhere and
return a reference. Apply drops step visits a loop has superseded, so the
state holds a loop's latest passes, not its history (see
[Limits](definitions.md#limits)); `State.Unit` returns nil for a dropped
visit. Keep history in your own step records.

## Kinds and their types

`Load(data, kinds)` asks the host which step kinds exist (`Known`) and checks
each step's configuration (`ValidateConfig`). A host that also knows what a
kind takes and returns implements the optional `KindSchemas`:

```go
func (k mobiusKinds) Schemas(kind string, cfg map[string]any) (params, output *workflow.Schema, err error) {
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

## Hosts with their own runtime

A host that already runs durable work (agent turns, action jobs, leases,
wake-ups) uses only the root package. Map each `DispatchStep` to a job keyed
by its StepID, each job's completion to an event, and `ScheduleTimer` to a
wake-up. `Outstanding(def, state)` rebuilds the commands for in-flight work
if a host ever needs to re-drive it.

Long-running and external steps need nothing special: a step is in flight
until its result is delivered. An `ask` step that waits for a person, a
`wait` step that waits for a webhook, or a child workflow are all host kinds
whose completion event arrives later. Give them a `timeout`.

## local

`local.Runner` carries out commands in-process: a goroutine per dispatch,
`time.AfterFunc` per timer, a context cancel per `CancelStep`. Its checkpoint
is the `State`: set `Save` to persist each state that changed and call
`Resume` to continue from one. Use it for tests, CLIs and single-process
programs.

Cancelling the context passed to `Run` or `Resume` pauses the run: it
returns the running state with `ctx.Err()`, ready for `Resume`. To cancel the
run instead, cancel the context with `local.Cancel(reason)` as the cause:

```go
ctx, cancel := context.WithCancelCause(ctx)
go func() { st, err = runner.Run(ctx, def, input) }()
cancel(local.Cancel("superseded"))   // st.Status == cancelled, err == nil
```

The runner applies the cancel event, saves the cancelled state, cancels the
handlers' contexts and returns once they have all returned.

## postgres

The `postgres` module is a complete durable host.

```go
store, _ := postgres.New(pool, kinds)          // tables live in schema "workflow"
_ = store.Migrate(ctx)                         // safe from every instance at startup

w := &postgres.Worker{Store: store, Handlers: map[string]postgres.Handler{
    "agent":  runAgent,
    "action": runAction,
}}
go w.Run(ctx)                                  // run as many as you like

res, err := store.Start(ctx, postgres.NewRun{ID: "pr-42-abc123", Definition: def,
    Input: map[string]any{"event": payload, "meta": map[string]any{"type": "pull_request.opened"}}})
```

Tables: `definitions` (by hash), `runs` (state jsonb plus queryable copies of
status, error, outputs and metadata), `steps` (one record per StepID),
`events` (every applied event, in order), `jobs` (the outbox) and
`schema_migrations`. `Migrate` applies numbered migrations once each, in one
transaction under an advisory lock, so concurrent calls are safe; it refuses
a database newer than the package.

### Starting runs and delivering events

`StartTx`, `DeliverTx` take your `pgx.Tx`. A handler that hands work to
another system returns `postgres.ErrDeferred`; the job is done, the step
stays in flight, and whoever finishes the work calls `store.Deliver` with the
StepID.

`Start`, `Deliver` and `Cancel` (and the `Tx` forms) return a `Result`: the
new `State`, the `Commands`, and `Applied`. The commands are already in the
outbox, committed with the state; workers carry them out, so do not run them
yourself. `Applied` is false when the event changed nothing: a duplicate, a
result for an attempt that timed out, was cancelled or was retried, an event
on a finished run, or a Start of an existing run. An endpoint that records an
approval answers 200 either way, and can say "already decided" when
`Applied` is false.

To learn that a run finished, either:

- pass `postgres.WithOnFinished(fn)`: `fn` runs inside the transaction that
  completes, fails or cancels the run, once per run, with its outputs, error
  or reason. Write your own records or outbox rows there. An error from `fn`
  rolls the event back.
- `LISTEN` on `store.RunsChannel()`: after each commit that changes a run's
  status, the store notifies `{"run_id": "...", "status": "..."}`.
  Notifications are best effort; read the run with `GetRun`.

The store runs every definition with the limits from `postgres.WithLimits`
(defaults otherwise), whether the definition came from `Start` or was loaded
from the database by another process; it ignores `Definition.Limits` and
never modifies the definition you pass.

An output Postgres cannot store as jsonb (a NUL character, or a value JSON
cannot encode such as NaN) fails the step with `invalid_output` instead of
rolling back and running the handler again; NUL characters in error messages
and cancel reasons become U+FFFD. `Start` returns `ErrUnstorable` for an input
or metadata with a NUL.

### Workers

Workers claim due jobs with `FOR UPDATE SKIP LOCKED` and a lease, and wake on
`LISTEN`/`NOTIFY` or a poll. A worker claims only the step kinds it has
handlers for (and any timer), so agent workers and action workers can share
one outbox.

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
definition uses, as in a deploy that has not reached every instance) is a
configuration problem. `Deliver` returns `ErrDefinitionUnavailable`. A worker
runs nothing for such a run: it logs an error and waits a lease, doubling,
for the configuration to be fixed, and after `MaxClaims` claims it fails the
run with type `definition_unavailable`, drops its jobs and records an
`aborted` entry in the event log.

Connections: each worker holds one dedicated connection for `LISTEN`,
outside the pool, and closes it when `Run` returns. Everything else
(claims, heartbeats, settles) borrows pool connections briefly, never across
a handler, so a pool of any size works; size it for `Concurrency` plus your
application's own queries to avoid waiting. `LISTEN` needs a session: behind
a transaction-mode pooler (Neon's `-pooler` endpoint, PgBouncer) set
`Worker.ListenConfig` to the direct endpoint, or the worker only wakes every
`Poll`. On Cloud Run, keep `Lease` within the instance's shutdown grace
period so in-flight results are recorded before it stops.
