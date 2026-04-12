package workflow

import "sync"

// History is the per-activity-invocation persisted cache returned by
// Context.History. Activities use it to cache the result of expensive
// or non-idempotent operations across wait-unwind replays: any value
// recorded once survives the suspension + resume cycle, so a replayed
// activity can pick up cached work without re-executing it.
//
// Entries are scoped to a single activity invocation. Once the step
// completes and the branch advances to its successor, the history is
// cleared — no cross-step leakage.
//
// Use cases:
//   - Caching LLM calls before a Context.Wait so replays don't re-bill.
//   - Caching non-idempotent HTTP writes that can't be keyed off
//     natural idempotency tokens.
//   - Memoizing expensive computation inside an agent loop.
//
// Thread-safety: safe for concurrent use within a single activity
// invocation. Different activity invocations own different History
// instances.
type History struct {
	mu      sync.Mutex
	entries map[string]any
	// commit, when non-nil, is called with a snapshot of the current
	// entries after each successful mutation. The engine uses it to
	// persist the cache into BranchState so it survives checkpoints.
	commit func(snapshot map[string]any)
}

// newHistory constructs a History seeded with the given entries and a
// commit callback for persistence.
func newHistory(initial map[string]any, commit func(map[string]any)) *History {
	entries := make(map[string]any, len(initial))
	for k, v := range initial {
		entries[k] = v
	}
	return &History{entries: entries, commit: commit}
}

// NewHistoryForTest returns a fresh, non-persistent History for use
// by test helpers (e.g. workflowtest.FakeContext).
func NewHistoryForTest() *History {
	return newHistory(nil, nil)
}

// RecordOrReplay runs fn on the first call for the given key and
// caches its result. Subsequent calls for the same key — including
// calls from activity replays after a wait-unwind — return the cached
// value without invoking fn. If fn returns an error, no cache entry
// is written and the error is returned unchanged.
//
// Concurrency: fn runs outside the history's lock so independent
// RecordOrReplay calls can proceed in parallel. If two goroutines
// race on the same key, both run fn, but only the first result is
// cached; the second caller sees the cached first result.
func (h *History) RecordOrReplay(key string, fn func() (any, error)) (any, error) {
	h.mu.Lock()
	if v, ok := h.entries[key]; ok {
		h.mu.Unlock()
		return v, nil
	}
	h.mu.Unlock()

	v, err := fn()
	if err != nil {
		return nil, err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.entries[key]; ok {
		// Another goroutine won the race; return its value.
		return existing, nil
	}
	h.entries[key] = v
	if h.commit != nil {
		h.commit(copyMap(h.entries))
	}
	return v, nil
}

// Get returns the cached value for key, if any, and whether it was
// present. Exposed for testing and introspection; activities should
// prefer History.RecordOrReplay for the normal cache-or-compute flow.
func (h *History) Get(key string) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.entries[key]
	return v, ok
}

// Len returns the number of entries currently cached.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}
