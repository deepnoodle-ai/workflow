# PRD: Signals, Waits, and Pausing

| Field         | Content                                                                 |
|---------------|-------------------------------------------------------------------------|
| Title         | Signals, Waits, and Pausing for the Workflow Library                    |
| Author        | Curtis Myzie                                                            |
| Status        | Draft                                                                   |
| Last Updated  | 2026-04-10                                                              |
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
- [ ] Documentation clearly states the replay-safety contract

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
- [ ] `ExecutionState.GetStatus()` returns a value computed from `pathStates` (Failed > Paused > Waiting > Running > Completed precedence for mixed sets)
- [ ] `ExecutionStatusPaused` is a new status value
- [ ] Existing code paths that set execution status directly are migrated to instead update path state and let the getter derive
- [ ] Final workflow completion/failure reporting is unchanged for consumers
- [ ] `ExecutionResult` exposes a `SuspensionInfo` field (reason, optional `WakeAt`, optional topics) when status is `Waiting` or `Paused`

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
- **FR-5:** If the store has no matching signal, the path must either soft-suspend (goroutine parks on a resume channel) or hard-suspend (exits the execution loop, returns with `SuspensionInfo`) based on the Runner's `SuspendAfter` policy.
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
- **FR-25:** `PausePathInCheckpoint(ctx, checkpointer, execID, pathID)` helper must load a checkpoint, mutate the specified path's flag, and save. Same for unpause.
- **FR-26:** When all active paths in an execution are paused or waiting (none running), the execution loop must exit cleanly and the execution's derived status must be `Paused` or `Waiting`.

### Aggregated status

- **FR-27:** `ExecutionState.GetStatus()` must compute execution status from `pathStates` using this precedence for mixed sets: `Failed` > `Paused` > `Waiting` > `Running` > `Completed`. A purely `Completed` set yields `Completed`.
- **FR-28:** Direct mutations of execution status (`SetStatus`) should be removed from call sites; only final success/failure transitions on execution completion remain explicit.
- **FR-29:** `ExecutionResult` must include a `SuspensionInfo` field set when the execution ends in `Paused` or `Waiting` state, containing `Reason` (`paused | waiting_signal | sleeping`), `PathID`, optional `WakeAt`, and optional `Topics`.

### Checkpoint model

- **FR-30:** `ExecutionState` must add a `waitStates map[string]*WaitState` field keyed by path ID (not step name, since a path can wait at any step).
- **FR-31:** `WaitState` must carry enough information for resume without re-running templates: resolved topic(s), `WakeAt`, `Timeout`, and kind (`signal | sleep`).
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

## 8. Assumptions & Constraints

**Assumptions:**
- Consumers will provide durable `SignalStore` implementations (likely Postgres-backed) for production use.
- Activities using `workflow.Wait` will honor the replay-safety contract or use `ActivityHistory`.
- Clock skew between process restarts is negligible for sleep correctness (sub-second).
- Path IDs are stable across checkpoint/resume (already true).

**Constraints:**
- **No breaking changes** to existing `Run()`, `Resume()`, `Execution`, `Workflow`, `Step`, `Checkpointer`, `Activity`, or `Context` interfaces. All additions must be additive.
- **No new dependencies** in the root `workflow` module. The scripting engine split in `CLAUDE.md` established the dep-hygiene bar; signals/waits must not lower it.
- **Existing tests must pass unchanged.** Any migration of status-setting code paths must preserve current behavior for workflows that don't use new primitives.
- **Checkpoints must round-trip** old and new formats without data loss.
- **Go workspace structure** (root + activities + script + scriptengines + workflowtest + examples + cmd): new code lives in the root `workflow` module; `MemorySignalStore` lives there too.

## 9. Design Considerations

### Key architectural patterns

- **Reuse the snapshot-channel orchestration.** Signal requests, pause requests, and sleep registration all flow through the existing `PathSnapshot` channel used by joins. This keeps all path coordination on a single goroutine and avoids new concurrency surface area.
- **Rendezvous via the store, not via goroutines.** The `SignalStore` is the authoritative rendezvous point. Path goroutines never deliver signals to each other directly. This eliminates arrived-early races and makes the store the single place to reason about delivery.
- **Status is derived, not stored.** Removing direct `SetStatus` calls in favor of a computed getter eliminates an entire class of drift bugs. The execution status becomes a pure function of path states.
- **Replay scope is a single activity.** The step graph bounds the replay scope — earlier steps never re-run, only the activity that was mid-wait replays from its entry point. This is a stronger property than whole-workflow replay for the library's use cases.
- **Two trigger modes, one mechanism (pause).** External `PausePath` and internal `Pause` step both flip the same flag. Not two separate features — one primitive with two callers.

### Author-facing contracts

The library must ship with crystal-clear docs on two contracts:

1. **`workflow.Wait` replay contract** — one paragraph, in the `Wait` godoc, explaining that pre-wait activity code may execute multiple times and how to handle it (idempotency, `ActivityHistory`, step decomposition).
2. **`SignalStore` interface contract** — document exactly-once delivery semantics, FIFO ordering within a topic, queue-on-send behavior, and the optional `Subscribe` side interface.

### Interaction with existing features

- **Joins + waits:** A path can be at either a join or a wait, never both. No interaction.
- **Retries + `workflow.Wait`:** Retries handle activity failures; `Wait` handles activity suspension. They're orthogonal. A `Wait`-unwind is not a failure, so it doesn't count against retry budget.
- **Catch + waits:** `OnTimeout` routes a timed-out wait to a successor step, similar to catch routing. Same machinery, different trigger.
- **Child workflows + signals:** Out of scope for this branch. Signals are scoped per-execution; a child workflow is a separate execution. Parent/child signal bridging can come later if needed.

## 10. Open Questions

- **Default `SuspendAfter` threshold for the Runner.** Proposal: hard-suspend for any wait with `Timeout >= 5 * time.Minute` and soft-suspend otherwise. Needs validation against real consumer usage patterns.
- **Should `workflow.Wait` participate in the `ctx.Done()` path like joins do?** Proposal: yes — cancellation of the execution context aborts the wait with `ctx.Err()`, same as join.
- **`ActivityHistory` entry lifetime.** Cleared on step advance (FR-16) — but should it also be retrievable after the fact for debugging? Proposal: no, keep it scoped to live execution; use activity logging for post-hoc debugging.
- **Does `WaitState` live on `PathState` or on `ExecutionState`?** Proposal: `ExecutionState.waitStates[pathID]` parallels `joinStates[stepName]`. Either works; choice affects checkpoint layout.
- **Error type for `workflow.Wait` timeout** — reuse existing `"timeout"` type, or introduce a new `"wait_timeout"`? Proposal: reuse `"timeout"` — consistent with retry timeout semantics.

---

## Implementation Phases

For sequencing during build-out (not part of the requirements spec):

1. **Phase 1 — Pause/unpause.** Smallest self-contained slice. `Paused` status, `PausePath`/`UnpausePath`, step-boundary check, `Pause` step type, `PausePathInCheckpoint`, aggregated status getter. Validates the step-boundary + suspension-exit machinery.
2. **Phase 2 — Durable Sleep.** Single-path wait with `WakeAt`, exercises hard-suspend. No external dependency.
3. **Phase 3 — SignalStore + WaitSignal step + workflow.Wait.** Full signal surface: interface, `MemorySignalStore`, declarative step, imperative call with activity unwind/replay.
4. **Phase 4 — ActivityHistory.** Replay-safe agent-loop helper.
5. **Phase 5 — Runner integration.** `SuspendAfter` policy, `SuspensionInfo` surfacing, operator-facing ergonomics.
