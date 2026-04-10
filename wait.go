package workflow

import (
	"fmt"
	"time"
)

// waitUnwindError is a sentinel returned from workflow.Wait when no signal
// is pending. The step execution layer intercepts it, checkpoints the path
// with the wait topic, and suspends the execution. On resume the activity
// re-runs from its entry point; the second Wait call finds the signal in
// the store and returns the payload.
type waitUnwindError struct {
	Topic   string
	Timeout time.Duration
}

func (e *waitUnwindError) Error() string {
	return fmt.Sprintf("workflow.Wait: unwinding activity to wait for signal %q", e.Topic)
}

// isWaitUnwind reports whether err is a waitUnwindError, unwrapping as needed.
func isWaitUnwind(err error) (*waitUnwindError, bool) {
	for err != nil {
		if w, ok := err.(*waitUnwindError); ok {
			return w, true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return nil, false
		}
		err = u.Unwrap()
	}
	return nil, false
}

// Wait durably waits for a signal on the given topic. Call from activity
// code via a workflow.Context.
//
// Behavior:
//   - If a signal is already pending in the SignalStore, returns its payload
//     immediately.
//   - Otherwise returns a sentinel error that unwinds the activity, causing
//     the path to checkpoint and the execution to suspend. On resume, the
//     entire activity re-executes from its entry point; the second call to
//     Wait finds the signal and returns the payload.
//
// REPLAY SAFETY: any code that runs before Wait may execute more than once.
// Wrap non-idempotent work in ActivityHistory or use idempotency keys.
//
// Spike scope: timeout is recorded but not enforced.
func Wait(ctx Context, topic string, timeout time.Duration) (any, error) {
	ec, ok := ctx.(*executionContext)
	if !ok {
		return nil, fmt.Errorf("workflow.Wait: context is not a workflow execution context")
	}
	if ec.signalStore == nil {
		return nil, fmt.Errorf("workflow.Wait: no SignalStore configured on execution")
	}
	sig, err := ec.signalStore.Receive(ctx, ec.executionID, topic)
	if err != nil {
		return nil, fmt.Errorf("workflow.Wait: receive failed: %w", err)
	}
	if sig != nil {
		return sig.Payload, nil
	}
	return nil, &waitUnwindError{Topic: topic, Timeout: timeout}
}
