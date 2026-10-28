// Package workflow is a deterministic progression engine for step graphs.
//
// A workflow is a [Definition]: a list of steps joined by edges, with
// conditions, parallel fan-out, joins, `each` loops, retries, catch routes and
// a run-level failure handler. The package does not run steps. It decides what
// should happen next.
//
// The whole engine is one pure function:
//
//	next, commands, err := workflow.Apply(def, state, event)
//
// [Apply] takes the run's current [State] and one [Event] (the run started, a
// step completed or failed, a timer fired, the run was cancelled) and returns
// the new state plus the [Command]s the host must carry out: dispatch a step,
// cancel a step, schedule a timer, or record that the run finished. Apply does
// no I/O, starts no goroutines and reads no clock except [Event.At], so the
// same state and event always give the same result.
//
// That makes the host loop small and durable:
//
//  1. In one transaction: load the state, call Apply, store the new state and
//     the commands (an outbox).
//  2. After commit: carry out the commands. Each dispatched step is identified
//     by a stable [StepID] that the host uses as its idempotency key.
//  3. When the work finishes, deliver a StepCompleted or StepFailed event,
//     at least once. Duplicates are ignored.
//
// No process holds a run. A run can wait for days between events, survive
// any number of restarts, and advance on whichever worker receives the next
// event.
//
// Steps the host executes (an agent turn, an HTTP call, a human approval)
// are host kinds, declared through [Kinds]. The library owns only the control
// kinds: join, set and sleep.
//
// Subpackage local is an in-process driver for tests, CLIs and single-process
// programs. The postgres module is a durable host for event-driven services.
package workflow
