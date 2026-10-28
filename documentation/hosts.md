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
mismatched state, a malformed or unknown StepID, inputs that do not match the
definition. The run's own failures are `RunFailed` commands. Treat Apply
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
go func() { st, err = runner.Run(ctx, def, inputs, nil) }()
cancel(local.Cancel("superseded"))   // st.Status == cancelled, err == nil
```

The runner applies the cancel event, saves the cancelled state, cancels the
handlers' contexts and returns once they have all returned.

## postgres

The `postgres` module is a complete durable host.

```go
store, _ := postgres.New(pool, kinds)          // tables live in schema "workflow"
_ = store.Migrate(ctx)

w := &postgres.Worker{Store: store, Handlers: map[string]postgres.Handler{
    "agent":  runAgent,
    "action": runAction,
}}
go w.Run(ctx)                                  // run as many as you like

_, err := store.Start(ctx, postgres.NewRun{ID: "pr-42-abc123", Definition: def, Trigger: ev})
```

Tables: `definitions` (by hash), `runs` (state jsonb plus queryable copies of
status, error, outputs and metadata), `steps` (one record per StepID),
`events` (every applied event, in order) and `tasks` (the outbox).

`StartTx`, `DeliverTx` take your `pgx.Tx`. A handler that hands work to
another system returns `postgres.ErrDeferred`; the task is done, the step
stays in flight, and whoever finishes the work calls `store.Deliver` with the
StepID.

Workers claim due tasks with `FOR UPDATE SKIP LOCKED` and a lease, and wake on
`LISTEN`/`NOTIFY` or a poll. A worker claims only the step kinds it has
handlers for (and any timer), so agent workers and action workers can share
one outbox. A heartbeat extends the lease while the handler
runs; if the task disappears (the run was cancelled) or another worker
reclaimed it, the handler's context is cancelled. Results are delivered in a
transaction that deletes the task fenced by its claim count, so a worker
whose lease expired cannot apply a stale result. A step claimed `MaxClaims`
times without finishing fails with type `abandoned`.
