package workflow

import (
	"encoding/json"
	"fmt"
	"time"
)

// WaitKind identifies the kind of durable wait a path is parked on.
//
// Only the constants defined in this file are valid. JSON unmarshaling
// rejects any other value. A new kind may not be added without bumping
// checkpoint compatibility and teaching every resume/replay site how to
// handle it.
type WaitKind string

const (
	// WaitKindSignal is a wait for an external signal delivered via a
	// SignalStore — the Phase 3 primitive.
	WaitKindSignal WaitKind = "signal"
	// WaitKindSleep is a wait for a wall-clock deadline. Phase 2 will
	// implement the step handler; the kind is defined now so checkpoints
	// written in Phase 3 can round-trip Phase 2 state without a schema
	// migration.
	WaitKindSleep WaitKind = "sleep"
)

func (k WaitKind) valid() bool {
	switch k {
	case WaitKindSignal, WaitKindSleep:
		return true
	}
	return false
}

// UnmarshalJSON enforces that only defined WaitKind values round-trip.
func (k *WaitKind) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	kind := WaitKind(s)
	if !kind.valid() {
		return fmt.Errorf("workflow: unknown wait kind %q", s)
	}
	*k = kind
	return nil
}

// WaitState carries the information needed to resume a hard-suspended
// path without re-running templates: the resolved topic (for signal
// waits), the absolute deadline, the original timeout, and the kind.
//
// WaitState is inlined on PathState (nullable). Consumers should treat
// an absent or nil Wait as "no pending wait".
type WaitState struct {
	// Kind identifies the wait variant. Required; must be one of the
	// defined WaitKind constants.
	Kind WaitKind `json:"kind"`
	// Topic is the resolved rendezvous topic. Set when Kind == Signal.
	Topic string `json:"topic,omitempty"`
	// WakeAt is the absolute wall-clock deadline at which the wait times
	// out (for signal waits) or wakes (for sleeps). Zero means no
	// deadline; for a Sleep kind wait, a zero WakeAt with a positive
	// Remaining means the sleep clock is frozen by a pause.
	WakeAt time.Time `json:"wake_at,omitzero"`
	// Timeout is the original timeout duration as specified by the caller.
	// Recorded for observability; WakeAt is the authoritative deadline.
	Timeout time.Duration `json:"timeout,omitzero"`
	// Remaining is the amount of sleep time left when a Sleep-kind
	// wait was paused mid-sleep. Only populated while the owning path
	// is paused; cleared on unpause, at which point WakeAt is
	// recomputed as now + Remaining. See FR-19.
	Remaining time.Duration `json:"remaining,omitzero"`
}

// NewSignalWait constructs a WaitState for a signal-kind wait. If
// timeout is positive, WakeAt is set to time.Now() + timeout.
func NewSignalWait(topic string, timeout time.Duration) *WaitState {
	ws := &WaitState{
		Kind:    WaitKindSignal,
		Topic:   topic,
		Timeout: timeout,
	}
	if timeout > 0 {
		ws.WakeAt = time.Now().Add(timeout)
	}
	return ws
}

// NewSleepWait constructs a WaitState for a durable sleep. Duration
// must be positive; WakeAt is set to time.Now() + duration.
func NewSleepWait(duration time.Duration) *WaitState {
	return &WaitState{
		Kind:    WaitKindSleep,
		Timeout: duration,
		WakeAt:  time.Now().Add(duration),
	}
}
