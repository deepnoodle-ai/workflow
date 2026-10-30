// Package workflow is a deterministic progression engine for step graphs.
//
// A workflow is a [Definition]: a list of steps joined by edges, with
// conditions, parallel fan-out, joins, `each` loops, retries, catch routes,
// on_failure and finally steps. The package does not run steps. It decides
// what should happen next.
//
// The whole engine is one pure function:
//
//	next, commands, err := workflow.Apply(def, state, event)
//
// [Apply] takes the run's current [State] and one [Event] (the run started, a
// step completed or failed, a timer fired, the run was paused, resumed or
// cancelled) and returns the new state plus the [Command]s the host must
// carry out: dispatch a step, cancel a step, schedule a timer, or record that
// the run finished. Apply does no I/O, starts no goroutines and reads no
// clock except [Event.At], so the same state and event always give the same
// result.
//
// The host contract, in short: persist the new state and the commands in the
// event's transaction, carry the commands out after commit, and deliver each
// result as an event, at least once; duplicates are ignored. A [StepID] names
// one attempt; key side effects outside the host on the run ID and
// [StepID.Unit], which a retry keeps. The full contract is in
// documentation/hosts.md in the repository.
//
// No process holds a run. A run can wait for days between events, survive
// any number of restarts, and advance on whichever worker receives the next
// event.
//
// Steps the host executes (an agent turn, an HTTP call, a human approval)
// are host kinds, declared through [Kinds]. The library owns only the control
// kinds: join, set and sleep. Hosts run host kinds with a [Handler].
//
// Subpackage local is an in-process host for tests, CLIs and single-process
// programs. The postgres module is a durable host for event-driven services,
// and the sqlite module is the same host on SQLite, for development and
// single-host services. All call the same handlers. Subpackage durable holds
// the types every durable host backend shares, such as durable.Result.
package workflow
