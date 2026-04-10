package workflow

import (
	"context"
	"sync"
)

// Signal is a single delivered event for a (executionID, topic) pair.
type Signal struct {
	ExecutionID string
	Topic       string
	Payload     any
}

// SignalStore is the rendezvous point for delivering external events to
// paused workflow executions. Implementations must provide FIFO ordering
// per (executionID, topic) and exactly-once consumption on Receive.
//
// Spike scope: Subscribe is intentionally omitted.
type SignalStore interface {
	// Send delivers a signal for the given execution + topic. Signals queue
	// in the store even if no path is currently waiting.
	Send(ctx context.Context, executionID, topic string, payload any) error

	// Receive pops the oldest pending signal for the given execution + topic.
	// Returns (nil, nil) if no signal is pending — callers treat that as
	// "not yet, unwind and wait".
	Receive(ctx context.Context, executionID, topic string) (*Signal, error)
}

// MemorySignalStore is an in-memory implementation of SignalStore suitable
// for tests and development.
type MemorySignalStore struct {
	mu      sync.Mutex
	signals map[string][]*Signal // key: executionID + "\x00" + topic
}

// NewMemorySignalStore returns a new empty in-memory store.
func NewMemorySignalStore() *MemorySignalStore {
	return &MemorySignalStore{signals: map[string][]*Signal{}}
}

// signalKey builds the composite map key for an (executionID, topic)
// pair. The NUL byte separator relies on the assumption that neither
// executionID nor topic contains a NUL character — true for all
// current producers (execution IDs are typeid strings, topics are
// Risor-evaluated strings from workflow authors). If NULs ever become
// possible as input, switch to a separator that can't collide or
// escape it in both fields.
func signalKey(executionID, topic string) string {
	return executionID + "\x00" + topic
}

// Send implements SignalStore.
func (m *MemorySignalStore) Send(ctx context.Context, executionID, topic string, payload any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := signalKey(executionID, topic)
	m.signals[key] = append(m.signals[key], &Signal{
		ExecutionID: executionID,
		Topic:       topic,
		Payload:     payload,
	})
	return nil
}

// Receive implements SignalStore. Returns (nil, nil) when no signal is pending.
func (m *MemorySignalStore) Receive(ctx context.Context, executionID, topic string) (*Signal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := signalKey(executionID, topic)
	queue := m.signals[key]
	if len(queue) == 0 {
		return nil, nil
	}
	sig := queue[0]
	m.signals[key] = queue[1:]
	return sig, nil
}
