package workflow

import (
	"errors"
	"fmt"
	"time"
)

// ErrWaitTimeout is returned from Context.Wait when the wait's
// deadline has passed and no signal was delivered. Catch handlers
// can match on ErrorTypeTimeout to route timeouts to a recovery step.
var ErrWaitTimeout = errors.New("workflow: wait timed out")

// waitUnwindError is the sentinel returned from Context.Wait (and from
// the declarative WaitSignal step handler) when no signal is pending.
// The branch execution layer intercepts it, sends a waitRequest
// snapshot, and the orchestrator persists the Wait state before
// hard-suspending. On resume, the activity re-runs from its entry
// point; the second call to Wait finds the signal in the store and
// returns the payload.
//
// waitUnwindError bypasses retry and catch handlers (see
// MatchesErrorType and executeStepWithRetry / executeCatchHandler).
// A wait-unwind is not a failure — it's a suspension — so it must
// never consume retry budget or trigger a catch route.
type waitUnwindError struct {
	Wait *WaitState
}

func (e *waitUnwindError) Error() string {
	if e.Wait == nil {
		return "workflow: wait-unwind (no wait state)"
	}
	return fmt.Sprintf("workflow: unwinding for %s wait on %q", e.Wait.Kind, e.Wait.Topic)
}

// asWaitUnwind extracts the waitUnwindError from err if present.
func asWaitUnwind(err error) (*waitUnwindError, bool) {
	var wu *waitUnwindError
	if errors.As(err, &wu) {
		return wu, true
	}
	return nil, false
}

// isWaitUnwind reports whether err is an internal wait-unwind sentinel.
func isWaitUnwind(err error) bool {
	_, ok := asWaitUnwind(err)
	return ok
}

// Wait durably waits for a signal on the given topic. See Context.Wait
// for the replay-safety contract and the multi-wait pattern.
//
// Behavior:
//   - If a signal is already pending in the SignalStore, returns its
//     payload immediately.
//   - If the branch is being replayed after a resume and the original
//     deadline has passed, returns ErrWaitTimeout.
//   - Otherwise returns a sentinel error that unwinds the activity,
//     causing the branch to checkpoint and the execution to suspend
//     with status ExecutionStatusSuspended. On resume, the entire
//     activity re-executes from its entry point; the second call to
//     Wait either finds the signal, returns ErrWaitTimeout, or
//     unwinds again.
//
// The replay-safety contract — what is and is not guaranteed safe
// across replays — is documented in §9 of
// planning/prds/002-signals-waits-pausing.md ("Replay-safety contract
// (authoritative)"). Any code that runs before a Wait call may
// execute more than once. Wrap non-idempotent work in the History
// cache returned by Context.History or use idempotency keys.
//
// Multiple waits in one activity. An activity may call Wait more than
// once. Each Wait MUST be wrapped in History.RecordOrReplay so the
// result of an earlier wait is cached across replays. Without the
// wrapper, a second-wait suspension causes the activity to replay
// from the top, which re-calls the first Wait against an empty
// SignalStore — the first signal has already been consumed and is
// gone.
//
//	h := ctx.History()
//	v1, err := h.RecordOrReplay("wait1", func() (any, error) {
//	    return ctx.Wait(topic1, time.Hour)
//	})
//	if err != nil { return nil, err }
//	v2, err := h.RecordOrReplay("wait2", func() (any, error) {
//	    return ctx.Wait(topic2, time.Hour)
//	})
func (w *executionContext) Wait(topic string, timeout time.Duration) (any, error) {
	if err := w.Err(); err != nil {
		return nil, err
	}
	if w.signalStore == nil {
		return nil, fmt.Errorf("workflow: Context.Wait: no SignalStore configured on execution")
	}
	store := w.signalStore
	executionID := w.executionID

	// Consult the store first so "signal already present" wins and
	// replayed calls after delivery return immediately.
	sig, err := store.Receive(w, executionID, topic)
	if err != nil {
		return nil, fmt.Errorf("workflow: Context.Wait: receive failed: %w", err)
	}
	if sig != nil {
		return sig.Payload, nil
	}

	// Build / reuse the wait state. On a replay after resume the
	// branch already has a WaitState on its checkpoint with the
	// original deadline; reuse it so the clock doesn't restart.
	pending := w.pendingWait
	var ws *WaitState
	if pending != nil && pending.Kind == WaitKindSignal && pending.Topic == topic {
		reused := *pending
		ws = &reused
	} else {
		ws = newSignalWait(topic, timeout)
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
