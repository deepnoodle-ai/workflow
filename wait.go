package workflow

import (
	"errors"
	"fmt"
	"time"
)

// ErrWaitTimeout is returned from workflow.Wait when the wait's deadline
// has passed and no signal was delivered. Catch handlers can match on
// ErrorTypeTimeout to route timeouts to a recovery step.
var ErrWaitTimeout = errors.New("workflow: wait timed out")

// waitUnwindError is a sentinel returned from workflow.Wait (and from the
// declarative WaitSignal step handler) when no signal is pending. The
// path execution layer intercepts it, sends a WaitRequest snapshot, and
// the orchestrator persists the Wait state before hard-suspending. On
// resume, the activity re-runs from its entry point; the second call to
// workflow.Wait finds the signal in the store and returns the payload.
//
// waitUnwindError bypasses retry and catch handlers (see MatchesErrorType
// and executeStepWithRetry / executeCatchHandler). A wait-unwind is not a
// failure — it's a suspension — so it must never consume retry budget or
// trigger a catch route.
type waitUnwindError struct {
	Wait *WaitState
}

func (e *waitUnwindError) Error() string {
	if e.Wait == nil {
		return "workflow: wait-unwind (no wait state)"
	}
	return fmt.Sprintf("workflow: unwinding for %s wait on %q", e.Wait.Kind, e.Wait.Topic)
}

// isWaitUnwind reports whether err is a waitUnwindError, unwrapping as needed.
func isWaitUnwind(err error) (*waitUnwindError, bool) {
	var wu *waitUnwindError
	if errors.As(err, &wu) {
		return wu, true
	}
	return nil, false
}

// IsWaitUnwind reports whether err is an internal wait-unwind sentinel.
// Consumers that implement custom step executors or error handlers can
// use this to short-circuit their own retry / logging logic for
// suspensions. The engine itself already bypasses retry and catch for
// these errors.
func IsWaitUnwind(err error) bool {
	_, ok := isWaitUnwind(err)
	return ok
}

// SignalAware is the side interface that lets activity code reach the
// signal infrastructure without depending on the concrete
// *executionContext type. workflow.Wait calls into this rather than
// type-asserting against the private context, so consumers can wrap or
// decorate workflow.Context without breaking waits.
//
// The library's executionContext is the only built-in implementation.
// Tests and middleware that want to forward Wait to a real execution can
// embed or delegate to it.
type SignalAware interface {
	SignalStore() SignalStore
	ExecutionID() string
	// PendingWait returns the wait state the path was parked on before
	// the current activity invocation, if any. It is non-nil when the
	// activity is being replayed after a resume from a hard-suspended
	// checkpoint, so workflow.Wait can reuse the original deadline
	// rather than starting the clock over.
	PendingWait() *WaitState
}

// Wait durably waits for a signal on the given topic. Call from activity
// code via a workflow.Context that implements [SignalAware] (the
// execution-provided context does).
//
// Behavior:
//   - If a signal is already pending in the SignalStore, returns its
//     payload immediately.
//   - If the path is being replayed after a resume and the original
//     deadline has passed, returns [ErrWaitTimeout].
//   - Otherwise returns a sentinel error that unwinds the activity,
//     causing the path to checkpoint and the execution to suspend with
//     status ExecutionStatusSuspended. On resume, the entire activity
//     re-executes from its entry point; the second call to Wait either
//     finds the signal, returns ErrWaitTimeout, or unwinds again.
//
// The replay-safety contract — what is and is not guaranteed safe across
// replays — is documented in §9 of planning/prds/002-signals-waits-pausing.md
// ("Replay-safety contract (authoritative)"). Any code that runs before
// a Wait call may execute more than once. Wrap non-idempotent work in
// [ActivityHistory] or use idempotency keys.
//
// Multiple waits in one activity. An activity may call Wait more than
// once (e.g. an agent loop that calls one tool, waits for the response,
// calls another tool, waits again). Each Wait MUST be wrapped in
// [History.RecordOrReplay] so the result of an earlier wait is cached
// across replays. Without the wrapper, a second-wait suspension causes
// the activity to replay from the top, which re-calls the first Wait
// against an empty SignalStore — the first signal has already been
// consumed and is gone. The result is undefined: the first Wait will
// re-suspend, or worse, consume a signal intended for a different wait.
//
// Correct multi-wait pattern:
//
//	history := workflow.ActivityHistory(ctx)
//	v1, err := history.RecordOrReplay("wait1", func() (any, error) {
//	    return workflow.Wait(ctx, topic1, time.Hour)
//	})
//	if err != nil { return nil, err }
//	v2, err := history.RecordOrReplay("wait2", func() (any, error) {
//	    return workflow.Wait(ctx, topic2, time.Hour)
//	})
func Wait(ctx Context, topic string, timeout time.Duration) (any, error) {
	// Respect context cancellation — an already-done context should
	// surface its error rather than silently unwind.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sa, ok := ctx.(SignalAware)
	if !ok {
		return nil, fmt.Errorf("workflow.Wait: context is not signal-aware (no SignalStore plumbing)")
	}
	store := sa.SignalStore()
	if store == nil {
		return nil, fmt.Errorf("workflow.Wait: no SignalStore configured on execution")
	}
	executionID := sa.ExecutionID()

	// First, always consult the store so "signal already present" wins
	// and replayed calls after delivery return immediately.
	sig, err := store.Receive(ctx, executionID, topic)
	if err != nil {
		return nil, fmt.Errorf("workflow.Wait: receive failed: %w", err)
	}
	if sig != nil {
		return sig.Payload, nil
	}

	// Build / reuse the wait state. On a replay after resume the path
	// already has a WaitState on its checkpoint with the original
	// deadline; reuse it so the clock doesn't restart.
	pending := sa.PendingWait()
	var ws *WaitState
	if pending != nil && pending.Kind == WaitKindSignal && pending.Topic == topic {
		// Reuse so the absolute deadline is preserved.
		reused := *pending
		ws = &reused
	} else {
		ws = NewSignalWait(topic, timeout)
	}

	// Belt-and-suspenders: a frozen wait (WakeAt zero, Remaining > 0)
	// can reach this handler if a checkpoint edit or partial unpause
	// left the deadline un-thawed. Rebase so the wait has a real
	// deadline rather than running forever.
	if ws.WakeAt.IsZero() && ws.Remaining > 0 {
		ws.WakeAt = time.Now().Add(ws.Remaining)
		ws.Remaining = 0
	}

	// Enforce the absolute deadline.
	if !ws.WakeAt.IsZero() && !time.Now().Before(ws.WakeAt) {
		return nil, fmt.Errorf("%w: topic %q", ErrWaitTimeout, topic)
	}

	return nil, &waitUnwindError{Wait: ws}
}
