# PRD: Signals, Waits, and Pausing

| Field         | Content                                                                 |
|---------------|-------------------------------------------------------------------------|
| Title         | Signals, Waits, and Pausing for the Workflow Library                    |
| Author        | Curtis Myzie                                                            |
| Status        | Draft                                                                   |
| Last Updated  | 2026-04-10 (revised after implementation spike)                         |
| Branch        | `signals-pause` (worktree at `.claude/worktrees/signals-pause`)         |
| Stakeholders  | DeepNoodle engineering                                                  |

## 1. Problem & Opportunity

The workflow library is a solid in-process execution engine for step-graph workflows, but it has a blind spot that's becoming urgent for AI-agent use cases: **it can't durably wait for external events.**

Today, the only blocking primitives are:

- **Joins**, which wait for sibling execution paths to complete — useful for fan-in, useless for anything external.
- The `wait` built-in activity, which is just `time.Sleep` — holds a goroutine, dies with the process, loses state on restart.

This is fine for classical workflow patterns (ETL, fan-out/fan-in, retry chains) but breaks the moment a workflow needs to coordinate with anything outside itself. Concretely, the use cases now driving this work:

1. **AI agent callbacks.** An activity calls an LLM, the LLM asks for human input or an external tool result, and the agent needs to park for **seconds to days or weeks** until a signal arrives with a UUID-keyed payload. The UUID isn't known at workflow-definition time — it's generated at runtime inside the activity.
2. **Operator pause.** An operator watching a running workflow needs to freeze a specific path mid-execution to investigate an anomaly, then either unpause it or abort. Today there is no way to do this without killing the process.
3. **Declarative hold-points.** Workflow authors want a step that says "stop here until a human acknowledges" — e.g., a gate before a production deploy, or an approval step before a destructive operation. No such primitive exists; authors have to leave the library and build it themselves.
4. **Long durable sleeps.** "Wait 24 hours, then retry" is impossible today without a consumer-side scheduler.

Without these primitives, every consumer has to build the same machinery on top of the library — external queue, external timer, a way to mutate checkpoint state from outside — and reinvent the same race conditions and edge cases each time. The library loses its leverage as the thing that "makes workflows durable" at exactly the moment the agent use case demands that durability most.

**What happens if we do nothing:** Consumers ship with parallel home-grown pause/signal mechanisms glued onto the side of the workflow library, the library's checkpoint invariants get violated by that glue code, and the abstraction rots. Every downstream project that wants AI-agent workflows repeats the work.

## 2. Goals & Success Metrics

**Primary goal:** Make the library the *correct* place to express "pause a path," "wait for a signal," and "sleep durably," with semantics that survive process death for arbitrarily long durations.

**Success criteria:**

- **P1:** Consumers can replace any bespoke pause/signal logic with library primitives, with no feature regressions.
- **P2:** An AI agent activity can call `workflow.Wait(ctx, uuid, 7*24*time.Hour)`, the host process can be killed and restarted, and the wait still resolves correctly when the signal eventually arrives.
- **P3:** A workflow author can declaratively mark a step as a signal-wait or a durable sleep in YAML, and the library handles all durability concerns.
- **P4:** Existing workflows (joins, retries, child workflows, catch/retry handlers) continue to work unchanged — no breaking changes to `Run()`, `Resume()`, or existing interfaces.

**Guardrail metrics** (things that must NOT get worse):

- Existing test suite passes unchanged.
- No new dependencies added to the root `workflow` module.
- Checkpoint size does not grow meaningfully for workflows that don't use signals/waits/pause.
- Orchestrator loop complexity stays manageable — adding signals must not require rewriting path coordination.

## 3. Target Users

**Primary persona — AI-agent activity author (Go developer).** Writes imperative Go inside an activity; calls LLMs; needs to park for callbacks; expects durability for long-duration waits; willing to handle replay-safety in exchange for durability.

**Secondary persona — Workflow author (YAML or Go).** Defines workflows as step graphs; wants declarative primitives: "this step waits for a signal on topic X," "this step sleeps for 24 hours," "this step is a manual hold-point." Expects to describe intent, not implement durability.

**Secondary persona — Operator (consumer-built UI).** Observes running workflows; clicks Pause on a specific path; inspects state; clicks Unpause or Abort. Never touches code or YAML.

## 4. User Stories

### US-001: Declarative signal wait with dynamic topic

**Description:** As a workflow author, I want a step that waits for an externally-delivered signal on a topic computed from path state, so I can coordinate with systems that hand back a UUID.

**Acceptance Criteria:**
- [ ] `WaitSignalConfig.Topic` supports `${state.*}` template expressions
- [ ] Topic is evaluated at step-entry time and the resolved value is persisted in checkpoint `WaitState`
- [ ] Signal with matching `(executionID, resolvedTopic)` wakes the path and stores payload in the configured variable
- [ ] If the signal arrives before the path parks, the path consumes it immediately with no wait
- [ ] Explicit timeout is required (no indefinite waits)
- [ ] On timeout, path routes to the configured `OnTimeout` step if set; otherwise the step fails

### US-002: Durable imperative wait inside an activity

**Description:** As an AI-agent activity author, I want to call `workflow.Wait(ctx, topic, timeout)` from imperative Go code and have the wait survive process restarts.

**Acceptance Criteria:**
- [ ] `workflow.Wait(ctx, topic, timeout)` is callable from any activity via `workflow.Context`
- [ ] If no signal is in the store, the call unwinds the activity via a sentinel error and the path checkpoints + suspends
- [ ] On resume, the activity re-runs from its entry point; the second call to `Wait` finds the signal already in the store and returns immediately with the payload
- [ ] If the signal is already in the store on the first call, it returns immediately without unwinding
- [ ] Timeout semantics match the declarative variant; a timeout with no `OnTimeout` returns `ErrWaitTimeout` to the activity
- [ ] The replay-safety contract in §9 is reflected verbatim (or by reference) in the `workflow.Wait` godoc

### US-003: Replay-safe intra-activity event recording

**Description:** As an AI-agent activity author running a loop with multiple LLM calls interleaved with multiple waits, I want to cache expensive operations across replays so restarts don't burn tokens.

**Acceptance Criteria:**
- [ ] `workflow.ActivityHistory(ctx)` returns a persisted event log scoped to the current activity invocation
- [ ] `history.RecordOrReplay(key, fn)` runs `fn` on first execution and returns the cached value on subsequent replays
- [ ] History is persisted in the path state so it survives checkpoint/resume
- [ ] History is cleared when the step advances past the activity (not leaked into later steps)
- [ ] A simple agent loop (`plan → wait → act → wait → summarize`) works correctly across process restarts with LLM calls executed only once per logical step

### US-004: Durable sleep step

**Description:** As a workflow author, I want a `Sleep` step that waits for a wall-clock duration and survives process restarts.

**Acceptance Criteria:**
- [ ] `SleepConfig{Duration}` blocks the path until `WakeAt` is reached
- [ ] `WakeAt` is persisted in `WaitState` in the checkpoint
- [ ] If the process dies mid-sleep and resumes before `WakeAt`, the path keeps waiting; if it resumes after `WakeAt`, the path wakes immediately
- [ ] Short sleeps can run in-process (soft-suspend); long sleeps can hard-suspend the execution and return `WakeAt` in the result so the consumer can schedule resume
- [ ] Sleep participates correctly with pause (see US-006)

### US-005: Operator-triggered path pause

**Description:** As an operator, I want to pause a specific path in a running workflow, investigate, and either resume it or abort.

**Acceptance Criteria:**
- [ ] `execution.PausePath(pathID)` on an in-memory execution sets a pause flag on the path's state
- [ ] The path exits cleanly at its next step boundary with `PathState.Status = Paused`
- [ ] When all remaining active paths are paused or waiting, the execution loop exits and `Run()` returns without error (status derivable from path states)
- [ ] `execution.UnpausePath(pathID)` clears the flag
- [ ] `PausePathInCheckpoint(ctx, checkpointer, execID, pathID)` helper mutates a non-loaded execution's checkpoint so operators can pause workflows that aren't currently running in a host process
- [ ] A `Resume()`/`ExecuteOrResume()` of a paused workflow with an unpaused flag continues execution from the checkpoint

### US-006: Declarative pause step

**Description:** As a workflow author, I want a `Pause` step that unconditionally halts a path until an operator unpauses it — a built-in manual-hold gate.

**Acceptance Criteria:**
- [ ] A step with `Pause: true` (or a dedicated `Pause` step type) triggers the same pause flag as `PausePath`
- [ ] The path exits cleanly with `Paused` status when the step is reached
- [ ] Unpausing resumes execution past the pause step along its `Next` edges

### US-007: Aggregated execution status

**Description:** As a library consumer, I want the execution's status to be a consistent derived view of its paths, not a separately-maintained field that can drift.

**Acceptance Criteria:**
- [ ] `ExecutionState.GetStatus()` returns a value computed from `pathStates` (Failed > Paused > Suspended > Waiting > Running > Completed precedence for mixed sets)
- [ ] `ExecutionStatusPaused` and `ExecutionStatusSuspended` are new status values; `ExecutionStatusWaiting` remains for join-in-progress only
- [ ] Existing code paths that set execution status directly are migrated to instead update path state and let the getter derive
- [ ] Final workflow completion/failure reporting is unchanged for consumers
- [ ] `ExecutionResult` exposes a `SuspensionInfo` field (reason, optional `WakeAt`, optional topics) when status is `Suspended` or `Paused`

### US-008: Signal delivery API

**Description:** As a library consumer, I want a stable library-level API for delivering a signal to a running or suspended workflow.

**Acceptance Criteria:**
- [ ] `SignalStore` interface is defined with `Send`, `Receive`, and optional `Subscribe` methods
- [ ] `MemorySignalStore` is shipped for dev and tests
- [ ] `execution.Signal(topic, payload)` is a convenience for delivering to the current execution
- [ ] Signals are persisted FIFO per `(executionID, topic)` with exactly-once delivery semantics
- [ ] Signals sent to a paused path queue in the store and deliver on unpause
- [ ] Consumers can implement Postgres-backed stores without modifying the library

## 5. Functional Requirements

### Wait & signals

- **FR-1:** The library must expose a `SignalStore` interface with `Send(ctx, executionID, topic, payload) error`, `Receive(ctx, executionID, topic) (*Signal, error)`, and an optional `Subscribe(ctx, executionID, topic) (<-chan *Signal, func(), error)` side interface.
- **FR-2:** The library must ship a `MemorySignalStore` implementation suitable for tests and development.
- **FR-3:** `WaitSignalConfig` step fields: `Topic` (templated string, required), `Timeout` (duration, required), `Store` (variable name for payload, optional), `OnTimeout` (edge target, optional).
- **FR-4:** When a path reaches a `WaitSignal` step, it must evaluate the topic template against current path state, persist the resolved topic in `WaitState`, and attempt an immediate `Receive` from the store before blocking.
- **FR-5:** If the store has no matching signal, the path must **hard-suspend**: the path goroutine exits, the orchestrator removes the path from `activePaths`, and when no running paths remain the execution loop exits cleanly with `ExecutionResult.Status = Suspended` and a populated `SuspensionInfo`. Hard-suspend is the default and the only required mode for Phase 3 — the spike confirmed it falls out of the existing orchestrator with ~45 lines of new code. Soft-suspend (parking the goroutine on a resume channel, join-style) is a Phase-5 optimization for short waits under a Runner-configured `SuspendAfter` threshold; it does not change the externally visible contract, so it can be added later without API churn.
- **FR-6:** On signal delivery, the resolved payload must be written to the configured `Store` variable in the path's state before the path advances.
- **FR-7:** On timeout, if `OnTimeout` is set the path routes there; otherwise the step fails with a `WorkflowError` of type `"timeout"`.
- **FR-8:** `workflow.Wait(ctx, topic, timeout) (any, error)` must be callable from activity code via `workflow.Context`.
- **FR-9:** `workflow.Wait` must first attempt `Receive` from the store; if a signal exists, return it immediately.
- **FR-10:** If no signal exists, `workflow.Wait` must return a sentinel error (e.g., `ErrWaitUnwind`) that the step execution layer intercepts, checkpoints the path, and marks the step for replay on resume.
- **FR-11:** On resume, the library must re-execute the activity from its entry point; the second call to `workflow.Wait` with the same topic must find the signal in the store and return immediately.
- **FR-12:** Signals sent to an execution must be persisted in the store even if no path is currently waiting — they queue until a matching `Receive`.

### Activity history

- **FR-13:** `workflow.ActivityHistory(ctx)` must return a per-activity-invocation persisted event log.
- **FR-14:** `history.RecordOrReplay(key, fn)` must execute `fn` on the first call and cache its return value; subsequent calls (including after a resume) must return the cached value without invoking `fn`.
- **FR-15:** Activity history must be persisted in the path state and included in checkpoints.
- **FR-16:** Activity history must be cleared from path state when the step advances past the activity (no cross-step leakage).

### Sleep

- **FR-17:** `SleepConfig{Duration}` step must compute `WakeAt = time.Now() + Duration` at step-entry time and persist it in `WaitState`.
- **FR-18:** A sleeping path must be resumable after process death: on reload, if `time.Now() >= WakeAt`, the path wakes immediately; otherwise it resumes waiting.
- **FR-19:** On pause of a sleeping path, the library must record the remaining duration and, on unpause, recompute `WakeAt = time.Now() + remaining`. The sleep clock does not tick during pause.

### Pause

- **FR-20:** `PathState` must include a `pauseRequested` flag and a `Paused` status value.
- **FR-21:** `Path.Run()` must check the pause flag after each completed step and, if set, emit a `Paused` snapshot and exit cleanly without starting the next step.
- **FR-22:** `execution.PausePath(pathID)` must flip the flag on the specified path's state.
- **FR-23:** `execution.UnpausePath(pathID)` must clear the flag and, for a loaded execution, the path goroutine must resume execution (for suspended executions, the flag is cleared in the checkpoint and takes effect on `Resume`).
- **FR-24:** A `Pause: true` step (or dedicated `Pause` step type) must set the pause flag at step-entry, causing the path to exit at that step.
- **FR-25:** `PausePathInCheckpoint(ctx, checkpointer, execID, pathID)` helper must load a checkpoint, mutate the specified path's flag, and save. Same for unpause. The helper is a non-atomic load-modify-write against the existing `Checkpointer` interface (`LoadCheckpoint` → mutate → `SaveCheckpoint`); concurrent writes from a host process running the same execution may therefore race. Consumers that need strict atomicity must use a `Checkpointer` implementation that serializes writes (e.g., Postgres row-level locking, optimistic concurrency via version column). The helper is idempotent: pausing an already-paused path is a no-op. It returns an error if the execution is not found, the path is not found, or the save fails.
- **FR-26:** When all active paths in an execution are paused or waiting (none running), the execution loop must exit cleanly and the execution's derived status must be `Paused` or `Waiting`.

### Aggregated status

- **FR-27:** `ExecutionState.GetStatus()` must compute execution status from `pathStates` using this precedence for mixed sets: `Failed` > `Paused` > `Suspended` > `Waiting` > `Running` > `Completed`. A purely `Completed` set yields `Completed`.
- **FR-27a:** Introduce a new `ExecutionStatusSuspended` distinct from the existing `ExecutionStatusWaiting`. Today `Waiting` is overloaded by joins: a join-blocked path stays in `activePaths` with its goroutine parked on a channel, still inside the live execution. A signal-waiting or sleeping path is qualitatively different — its goroutine has exited, it lives only in the checkpoint, and the execution cannot make progress without external input (signal delivery, wall-clock advance, or unpause). Reusing `Waiting` for both makes it impossible for consumers to tell "execution is mid-run, just waiting on a sibling" from "execution is dormant, pokes from outside only." Split them. `Waiting` stays for intra-execution blocks (joins). `Suspended` covers hard-suspended signal-waits, sleeps, and pauses. The spike confirmed this ambiguity is real and will confuse observability code.
- **FR-28:** Direct mutations of execution status (`SetStatus`) should be removed from call sites; only final success/failure transitions on execution completion remain explicit.
- **FR-29:** `ExecutionResult` must include a `SuspensionInfo` field set when the execution ends in `Paused` or `Suspended` state, containing `Reason` (`paused | waiting_signal | sleeping`), `PathID`, optional `WakeAt`, and optional `Topics`. (The `Waiting` state — join in progress — is a mid-run status, not a terminal one, and never produces a `SuspensionInfo`.)

### Checkpoint model

- **FR-30:** `PathState` must carry a nullable `Wait *WaitState` field, populated when the path is parked on a signal-wait or durable sleep. The spike confirmed that inlining the wait on `PathState` is simpler than a separate `waitStates map[string]*WaitState` on `ExecutionState` — the path is already the durable coordination unit, resume logic already iterates over `pathStates`, and there is no cross-path wait coordination to speak of. Do **not** add a separate map.
- **FR-31:** `WaitState` must carry enough information for resume without re-running templates: resolved topic (for signal), `WakeAt` (for sleep), `Timeout`, and `Kind`. The kind field must be a typed Go enum:

  ```go
  type WaitKind string

  const (
      WaitKindSignal WaitKind = "signal"
      WaitKindSleep  WaitKind = "sleep"
  )

  type WaitState struct {
      Kind    WaitKind      `json:"kind"`
      Topic   string        `json:"topic,omitempty"`   // set when Kind == WaitKindSignal
      WakeAt  time.Time     `json:"wake_at,omitzero"`  // absolute deadline
      Timeout time.Duration `json:"timeout,omitzero"`
  }
  ```

  Constructors and JSON unmarshalers must reject any `Kind` value other than the defined constants.
- **FR-32:** Checkpoints must round-trip all new state (`waitStates`, `pauseRequested`, `ActivityHistory` entries) without breaking existing checkpoint readers (backward-compatible JSON).

## 6. Non-Goals (Out of Scope)

- **Cron / scheduled workflow execution.** Belongs in the consumer. This branch only surfaces `WakeAt` in `SuspensionInfo` so consumers can enqueue their own delayed resumes.
- **Cross-execution signaling.** Signals are scoped to a single `executionID`. No correlation keys across executions, no "signal-with-start."
- **Predicate-based waits.** Waits are explicit topic-based, not predicate-based. Authors use `WaitSignal` / `workflow.Wait`, not arbitrary boolean expressions over state.
- **Queries and synchronous updates.** Only async signals. No synchronous reads of workflow state or synchronous writes with validation.
- **Streaming / multi-delivery signals.** Each signal is consumed once by a single wait. No broadcast, no pub-sub, no signal replay to multiple consumers.
- **Whole-workflow replay.** The step graph model is kept. Replay is scoped to a single activity invocation, not the whole workflow.
- **Pause granularity below path level.** No pausing mid-activity from outside.
- **Distributed signal routing.** The library hands signals to the `SignalStore`; distribution (e.g., wake the right worker process) is the consumer's concern.
- **A built-in UUID generator or signal authentication.** Consumers generate UUIDs in their own activity code and authenticate signal senders in their own `SignalStore` implementation.

**Future considerations (deferred but worth designing for):**
- Signal ACLs / authorization hooks on `SignalStore`
- Query/update surface if async signals prove insufficient
- Consumer-facing tooling for inspecting queued signals and active waits

## 7. Dependencies & Risks

| Risk / Dependency | Impact | Mitigation |
|-------------------|--------|------------|
| Activity replay semantics surprise authors | Silent double-execution of side effects before a `Wait` call | Documented contract in `workflow.Wait` docs; provide `ActivityHistory` as the escape hatch; recommend step decomposition as the simplest pattern |
| Dynamic topic races (signal arrives before wait registers) | Lost signals; workflows stuck indefinitely | Store-first delivery: `Send` always writes to store, `Wait` always `Receive`s before blocking. Rendezvous through the store, not through goroutines |
| Checkpoint backward compatibility | Existing consumers fail to load checkpoints after upgrade | All new state fields are additive with zero-value defaults; existing checkpoint readers must tolerate unknown fields (already the case via JSON round-trip) |
| Pause-during-sleep time accounting bugs | Sleeps end at wrong wall-clock times, breaking SLAs | Record remaining duration at pause, not absolute `WakeAt`; recompute on unpause; cover with tests |
| Hard-suspend vs soft-suspend policy ambiguity | Consumer confusion about when process can die | Default to hard-suspend for `Timeout > SuspendAfter` (configurable); soft otherwise. Clear docs on both modes |
| Orchestrator loop complexity creep | Adding signals + pause + sleep bloats the core select loop, hurts maintainability | Build on the existing `PathSnapshot` channel pattern. Signal delivery and pause requests go through the same snapshot path as joins. Factor the snapshot handler into per-case methods |
| Signal-store fragmentation across consumers | Every consumer implements their own Postgres store, subtly wrong | Ship `MemorySignalStore`; document the interface contract carefully; consider a reference Postgres implementation in a sibling repo once the interface stabilizes |
| Activity replay burns LLM tokens | `workflow.Wait` inside agent loops is expensive on restarts | `ActivityHistory.RecordOrReplay` caches LLM calls across replays. Documented as the recommended pattern for agent activities |
| Execution ID regenerated on `Resume` breaks signal lookup (spike-confirmed) | Resumed workflows silently fail to find their own signals; appear to hang or re-suspend forever | `Resume()` / `loadCheckpoint` must preserve the prior execution ID rather than overwriting with a fresh one. `NewExecution` must accept an `ExecutionID` option so callers can pin identity. Covered by a resume-after-signal integration test |
| Activity logger reports wait-unwind as a failure | Every suspension looks like an error in activity logs, poisoning dashboards and alerting | Treat `waitUnwindError` as a distinct outcome in `executeActivity`. Either skip the log entry or emit a new `ActivityLogEntry` variant (`Outcome: "suspended"`) that is not an error |

## 8. Assumptions & Constraints

**Assumptions:**
- Consumers will provide durable `SignalStore` implementations (likely Postgres-backed) for production use.
- Activities using `workflow.Wait` will honor the replay-safety contract or use `ActivityHistory`.
- Clock skew between process restarts is negligible for sleep correctness (sub-second).
- Path IDs are stable across checkpoint/resume (already true).
- Execution IDs are stable across `Resume()` (see Constraints — **not yet true today**, but must become so in Phase 3).

**Constraints:**
- **No breaking changes** to existing `Run()`, `Resume()`, `Execution`, `Workflow`, `Step`, `Checkpointer`, `Activity`, or `Context` interfaces. All additions must be additive.
- **No new dependencies** in the root `workflow` module. The scripting engine split in `CLAUDE.md` established the dep-hygiene bar; signals/waits must not lower it.
- **Existing tests must pass unchanged.** Any migration of status-setting code paths must preserve current behavior for workflows that don't use new primitives.
- **Checkpoints must round-trip** old and new formats without data loss.
- **Go workspace structure** (root + activities + script + scriptengines + workflowtest + examples + cmd): new code lives in the root `workflow` module; `MemorySignalStore` lives there too.
- **Execution IDs must be stable across `Resume()`.** The current library regenerates the execution ID on `Resume`: `loadCheckpoint` saves the caller's fresh `thisID` and writes it back over the checkpoint's ID via `SetID(thisID)`. The `SignalStore` is keyed on `(executionID, topic)`, so a rotating ID silently breaks signal delivery to any resumed workflow — signals sent to the original ID are invisible to the resumed activity's context. This was the single blocking bug surfaced by the spike. Phase 3 must change `Resume()` / `loadCheckpoint` to preserve the prior execution ID, and `NewExecution` must accept an explicit `ExecutionID` override so consumers running under a worker pool can resume into the right identity.

## 9. Design Considerations

### Key architectural patterns

- **Reuse the snapshot-channel orchestration.** Signal requests, pause requests, and sleep registration all flow through the existing `PathSnapshot` channel used by joins. This keeps all path coordination on a single goroutine and avoids new concurrency surface area.
- **Rendezvous via the store, not via goroutines.** The `SignalStore` is the authoritative rendezvous point. Path goroutines never deliver signals to each other directly. This eliminates arrived-early races and makes the store the single place to reason about delivery.
- **Status is derived, not stored.** Removing direct `SetStatus` calls in favor of a computed getter eliminates an entire class of drift bugs. The execution status becomes a pure function of path states.
- **Replay scope is a single activity.** The step graph bounds the replay scope — earlier steps never re-run, only the activity that was mid-wait replays from its entry point. This is a stronger property than whole-workflow replay for the library's use cases.
- **Two trigger modes, one mechanism (pause).** External `PausePath` and internal `Pause` step both flip the same flag. Not two separate features — one primitive with two callers.

### Replay-safety contract (authoritative)

This is the single authoritative statement of what happens when an activity calls `workflow.Wait`. Every other mention in the PRD and future docs must reference this section.

**The rule.** When an activity calls `workflow.Wait(ctx, topic, timeout)` and no matching signal is in the `SignalStore`, the engine unwinds the activity, checkpoints the path with a `WaitState` referencing the resolved topic, and either soft- or hard-suspends the path. When the signal eventually arrives (or on resume after process restart), **the entire activity re-executes from its entry point.** The second invocation of `workflow.Wait` with the same topic finds the signal in the store and returns immediately.

**What this means for authors:** any code an activity runs *before* a `workflow.Wait` call may execute more than once across the lifetime of a single logical step. Authors must design their activity code accordingly.

**What is guaranteed replay-safe:**

- **`workflow.Wait` itself.** The signal is consumed exactly once from the `SignalStore` regardless of how many times the activity is replayed. A replayed activity will not re-consume a signal that was already delivered.
- **`workflow.ActivityHistory.RecordOrReplay(key, fn)`.** Values persisted via this helper are returned from history on replay without re-executing `fn`. This is the escape hatch for expensive or non-idempotent work.
- **Signal delivery (`SignalStore.Send`).** FIFO per-topic, exactly-once consumption per receiver.
- **Pause/unpause.** `PausePath` / `UnpausePath` operate on durable path state; they are not subject to activity replay semantics at all (they live on the path/checkpoint, not inside activity code).

**What is NOT automatically safe** (author must handle):

- Side effects performed before a `workflow.Wait` call: HTTP POSTs, database writes, message publishing, file creation, etc. These will re-fire on replay unless wrapped in `ActivityHistory.RecordOrReplay` or made idempotent by the caller (e.g., using idempotency keys).
- LLM or other expensive API calls before a `workflow.Wait`. These will re-bill on replay unless wrapped in `ActivityHistory.RecordOrReplay`.
- Mutations to path state (`ctx.SetVariable`) before a `workflow.Wait` — these *are* captured in the checkpoint taken at the unwind point, so they survive one replay, but any logic that depends on "have I run this once already?" must use `ActivityHistory`, not state variables.

**How to apply it — three patterns:**

1. **Idempotency keys.** Use when the side effect has a natural key (HTTP PUT, Postgres upsert, Stripe idempotency key). Simplest option when available.
2. **`ActivityHistory.RecordOrReplay`.** Use when the side effect doesn't have a natural key or is expensive. Cache the result on the first call; return from history on replay.
3. **Step decomposition.** Use when the code before the wait is complex. Move the side effect into a preceding step so the wait step has no pre-wait work to replay.

**Example — agent loop with a callback, using `ActivityHistory`:**

```go
func (a *AgentActivity) Execute(ctx workflow.Context, params map[string]any) (any, error) {
    history := workflow.ActivityHistory(ctx)
    callbackID := uuid.NewString()

    // Expensive LLM call — cache across replays
    plan, err := history.RecordOrReplay("plan", func() (any, error) {
        return a.llm.Plan(ctx, params)
    })
    if err != nil {
        return nil, err
    }

    // Non-idempotent side effect — also cache
    _, err = history.RecordOrReplay("post-callback", func() (any, error) {
        return nil, a.postCallbackRequest(ctx, callbackID, plan)
    })
    if err != nil {
        return nil, err
    }

    // Durable wait — may unwind and replay from the top. Replay hits cached
    // plan and cached post-callback, then returns here with the signal.
    reply, err := workflow.Wait(ctx, callbackID, 7*24*time.Hour)
    if err != nil {
        return nil, err
    }
    return a.llm.React(ctx, plan, reply)  // cache this too if needed
}
```

**Author checklist — before shipping an activity that calls `workflow.Wait`:**

- [ ] Every HTTP/database/filesystem side effect before the wait is either idempotent or wrapped in `RecordOrReplay`.
- [ ] Every expensive API call before the wait is wrapped in `RecordOrReplay`.
- [ ] The activity has been tested by running to the wait point, killing the process, restarting, and delivering the signal — confirming the pre-wait work is not duplicated.
- [ ] If the code before the wait is complex enough to be error-prone, it has been split into a preceding step instead.

### Other author-facing contracts

- **`SignalStore` interface contract** — document exactly-once delivery semantics, FIFO ordering within a topic, queue-on-send behavior, and the optional `Subscribe` side interface.

### Interaction with existing features

- **Joins + waits:** A path can be at either a join or a wait, never both. No interaction.
- **Retries + `workflow.Wait`:** Retries handle activity failures; `Wait` handles activity suspension. They're orthogonal. A `Wait`-unwind is not a failure, so it doesn't count against retry budget.
- **Catch + waits:** `OnTimeout` routes a timed-out wait to a successor step, similar to catch routing. Same machinery, different trigger.
- **Child workflows + signals:** Out of scope for this branch. Signals are scoped per-execution; a child workflow is a separate execution. Parent/child signal bridging can come later if needed.

## 10. Open Questions

**Resolved by the Phase 3 spike** (see commit on `signals-pause`; keeping for traceability):

- ~~Rendezvous via store vs. via goroutines~~ — Validated. `SignalStore.Send` always writes, `Wait` always `Receive`s before blocking. The store-first approach eliminates the "signal arrives before Wait registers" race entirely, and the spike's "signal-already-present" test passed with zero extra machinery.
- ~~Hard-suspend vs. soft-suspend default~~ — Settled in favor of hard-suspend as the required mode (see revised FR-5). The spike showed hard-suspend is the *natural* shape of the existing orchestrator, not a tradeoff.

**Still open:**

- **Should `workflow.Wait` participate in the `ctx.Done()` path like joins do?** Proposal: yes — cancellation of the execution context aborts the wait with `ctx.Err()`, same as join. Spike did not exercise this.
- **`ActivityHistory` entry lifetime.** Cleared on step advance (FR-16) — but should it also be retrievable after the fact for debugging? Proposal: no, keep it scoped to live execution; use activity logging for post-hoc debugging.
- **Error type for `workflow.Wait` timeout** — reuse existing `"timeout"` type, or introduce a new `"wait_timeout"`? Proposal: reuse `"timeout"` — consistent with retry timeout semantics.
- **Atomic checkpoint mutation for `PausePathInCheckpoint`.** FR-25 currently specifies non-atomic load-modify-write. Should the library introduce an optional `AtomicCheckpointer` side interface (e.g., `UpdateCheckpoint(ctx, execID, func(*Checkpoint) error) error`) so Postgres-backed stores can serialize concurrent pause/unpause requests against running executions? Deferring until Phase 1 implementation surfaces a concrete race in practice.
- **Default `SuspendAfter` threshold for the Runner** (Phase 5, not Phase 3). Only relevant once soft-suspend exists as an opt-in. Proposal: hard-suspend for any wait with `Timeout >= 5 * time.Minute`, soft-suspend otherwise. Needs validation against real consumer usage patterns.

**Newly surfaced by the spike:**

- **Double-checkpoint on wait-unwind.** Today `executeActivity` checkpoints unconditionally after the activity returns. When the activity returned a `waitUnwindError`, the orchestrator's post-snapshot handler also checkpoints (with the final `Suspended` status and `WaitTopic` populated). The second write is the authoritative one; the first is wasted work and records an incomplete state. Proposal: short-circuit the `executeActivity` checkpoint when the returned error is a wait-unwind and let `processPathSnapshot` be the single source of truth. Minor but worth fixing before Phase 3 ships to keep checkpoint volume predictable for Postgres-backed stores.
- **Retry/catch bypass for wait-unwind should be explicit.** The spike showed that unwinds are currently ignored by `findMatchingRetryConfig` (no matching `ErrorType`) and by `executeCatchHandler` (no matching `ErrorEquals`), so retries and catches happen to skip them. Correct behavior, accidental mechanism. Proposal: add an explicit `errors.Is(err, waitUnwindSentinel)` guard at the top of both `executeStepWithRetry` and `executeCatchHandler`, mirroring how `ErrFenceViolation` already bypasses them (per CLAUDE.md). Failing to do so risks a future retry config with `ErrorEquals: ["ALL"]` accidentally retrying a suspended activity, burning through retries without ever delivering the signal.
- **Runner handling of a `Suspended` result.** The Runner integration section (Phase 5) talks about `SuspendAfter` but doesn't define the basic Phase-3 handshake: the Runner calls `Execute`, the result comes back `Suspended`, now what? Options: (a) Runner returns the result to caller, caller schedules resume externally; (b) Runner parks the execution internally and wakes on signal delivery via `SignalStore.Subscribe`. Proposal: (a) for Phase 3 (keeps Runner uninvolved), (b) as the Phase 5 enhancement. Needs an explicit decision before Phase 3 is considered done.
- **Context interface vs. type assertion for `workflow.Wait`.** The spike implemented `workflow.Wait` as a package function that type-asserts its `Context` argument to `*executionContext` to reach the private `signalStore` field. This works but means any consumer who wraps `Context` (e.g., for testing, middleware, or decorator patterns) breaks `Wait`. PRD says the `Context` interface must not be broken (FR constraint). Proposal: add a new exported side interface like `SignalAware interface { SignalStore() SignalStore; ExecutionID() string }` that `*executionContext` implements, and have `Wait` assert against that rather than the concrete type. Additive, non-breaking, and keeps `Context` clean.

---

## Implementation Phases

For sequencing during build-out (not part of the requirements spec):

1. **Phase 1 — Pause/unpause.** Smallest self-contained slice. `Paused` status, `PausePath`/`UnpausePath`, step-boundary check, `Pause` step type, `PausePathInCheckpoint`, aggregated status getter. Validates the step-boundary + suspension-exit machinery.
2. **Phase 2 — Durable Sleep.** Single-path wait with `WakeAt`, exercises hard-suspend. No external dependency.
3. **Phase 3 — SignalStore + WaitSignal step + workflow.Wait.** Full signal surface: interface, `MemorySignalStore`, declarative step, imperative call with activity unwind/replay. **Must also include**: execution-ID stability fix in `Resume()`/`loadCheckpoint` (see Constraints), `NewExecution` `ExecutionID` option, new `ExecutionStatusSuspended` value, activity-logger treatment of unwinds as non-errors, retry/catch bypass for wait-unwinds, and the `SignalAware` side interface for `workflow.Wait`. Spike (commit on `signals-pause`) proves the core unwind/replay mechanism works in ~45 lines of engine changes; estimate 4–6 engineering days for the full Phase 3 slice.
4. **Phase 4 — ActivityHistory.** Replay-safe agent-loop helper.
5. **Phase 5 — Runner integration.** `SuspendAfter` policy, `SuspensionInfo` surfacing, operator-facing ergonomics.
