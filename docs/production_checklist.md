# Production checklist

The library is a pure execution engine: it runs workflows in-process
and provides interfaces for the things it doesn't own. Going to
production means wiring durable storage, observability, and
operational hygiene around it. This is the punch list.

## Storage

- [ ] **Checkpointer** is wired to durable storage (Postgres, Redis,
  S3, etc.). The bundled `MemoryCheckpointer` and `FileCheckpointer`
  are dev-only.
- [ ] **StepProgressStore** is wired if the consumer wants
  step-level progress visibility outside the activity log.
- [ ] **ActivityLogger** is wired to whatever activity log store the
  consumer uses. The bundled `FileActivityLogger` is dev-only.
- [ ] **WorkflowRegistry** is loaded with every workflow definition
  the worker needs to run, at startup. Late registration is not
  supported.
- [ ] **SignalStore** is durable if the consumer uses `Wait` or
  `WaitSignal`. In-memory signal stores lose pending signals on
  process restart.
- [ ] Checkpoint round-trip is tested against the consumer's
  encoder. The library uses `encoding/json`; if you swap encoders,
  ensure every `BranchState` field — especially `PauseRequested`,
  `Wait`, `Variables`, `ActivityHistory` — survives the round trip.
  See [`Checkpoint`](../checkpoint.go) godoc.

## Execution

- [ ] **Runner** (not bare `Execution.Execute`) is the production
  entry point. The Runner composes heartbeat, default timeout,
  resume, and the completion hook.
- [ ] `WithDefaultTimeout` is set on the Runner, or `WithRunTimeout`
  on every `Run` call. Untimed executions can wedge a worker
  indefinitely.
- [ ] `WithHeartbeat` is wired if the consumer uses worker leases.
  The heartbeat func should refresh the lease and return an error
  on lease loss; the Runner cancels the execution context, and the
  workflow aborts cooperatively.
- [ ] `WithCompletionHook` is wired if the consumer triggers
  follow-up workflows from completed executions. Returning an error
  from the hook does not change `result.Status`; the partial
  follow-up list is still attached to `result.FollowUps`.

## Activity authoring

- [ ] Activities are **idempotent**. The engine retries on
  classified errors and replays on wait-unwind; an activity that
  charges a credit card without an idempotency key will charge it
  twice.
- [ ] Activities that call `Context.Wait` use `Context.History` /
  `RecordOrReplay` to memoize side effects. See
  [`docs/suspension.md`](suspension.md) for the replay-safety
  contract.
- [ ] Activities propagate `ctx` to every blocking call (HTTP,
  database, child workflows). The engine cancels the context on
  heartbeat failure or caller cancel — activities that ignore it
  hang the worker.
- [ ] Errors that should NOT be retried are surfaced as a
  `WorkflowError` with `Type: workflow.ErrorTypeFatal`. Default
  classification routes to `ErrorTypeActivityFailed`, which IS
  retried by `RetryConfig{ErrorEquals: ["activity_failed"]}`.

## Suspension and resume

- [ ] The consumer subscribes to the `Suspension.Topics` slice for
  every signal-suspended execution and routes incoming signals to
  the right execution.
- [ ] The consumer schedules a wall-clock resume at
  `Suspension.WakeAt` for every sleep-suspended execution.
- [ ] Resume uses `runner.Run(ctx, exec, workflow.WithResumeFrom(priorID))`
  on a fresh `Execution` built from the same workflow + registry.
- [ ] Stale checkpoints (older than the consumer's TTL policy) are
  garbage-collected from the checkpointer. The library does not
  manage retention.

## Observability

- [ ] `WithLogger` (or the Runner's `WithRunnerLogger`) is set to a
  structured logger. The default is a discard logger.
- [ ] An ActivityLogger is wired so per-activity inputs / outputs /
  errors are auditable. Without it, debugging a failed execution
  means re-running it.
- [ ] StepProgressStore is wired if the consumer needs sub-step
  progress in dashboards (e.g., a long-running activity that
  reports `ctx.ReportProgress`).

## Worker hygiene

- [ ] Each worker process holds a **lease** on the executions it is
  running and refreshes via the Runner heartbeat. Two workers
  running the same execution at the same time will both write
  checkpoints and the last writer wins.
- [ ] The worker's shutdown path drains in-flight executions
  cleanly: cancel the parent context, wait for `runner.Run` calls
  to return, then exit. The engine writes a final checkpoint on
  context cancel.
- [ ] Workflow definitions and activity registrations are immutable
  per worker version. Hot-swapping a workflow under a running
  execution will produce undefined behavior; deploy a new worker
  version, drain the old one.
