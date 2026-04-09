package workflowtest

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/deepnoodle-ai/workflow"
)

// MemoryCheckpointer is an in-memory Checkpointer for use in tests.
// It is safe for concurrent use.
type MemoryCheckpointer struct {
	mu          sync.RWMutex
	checkpoints map[string]*workflow.Checkpoint
}

// NewMemoryCheckpointer returns a new in-memory checkpointer.
func NewMemoryCheckpointer() *MemoryCheckpointer {
	return &MemoryCheckpointer{
		checkpoints: make(map[string]*workflow.Checkpoint),
	}
}

func (m *MemoryCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *workflow.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkpoints[checkpoint.ExecutionID] = deepCopyCheckpoint(checkpoint)
	return nil
}

func (m *MemoryCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*workflow.Checkpoint, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cp, ok := m.checkpoints[executionID]
	if !ok {
		return nil, nil // Follows existing convention: nil, nil = not found
	}
	return deepCopyCheckpoint(cp), nil
}

func (m *MemoryCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.checkpoints, executionID)
	return nil
}

// Checkpoints returns a snapshot of all stored checkpoints, keyed by execution ID.
// Useful for test assertions.
func (m *MemoryCheckpointer) Checkpoints() map[string]*workflow.Checkpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*workflow.Checkpoint, len(m.checkpoints))
	for k, v := range m.checkpoints {
		result[k] = deepCopyCheckpoint(v)
	}
	return result
}

// deepCopyCheckpoint creates a deep copy of a checkpoint via JSON round-trip.
func deepCopyCheckpoint(cp *workflow.Checkpoint) *workflow.Checkpoint {
	data, err := json.Marshal(cp)
	if err != nil {
		panic("workflowtest: failed to marshal checkpoint: " + err.Error())
	}
	var copy workflow.Checkpoint
	if err := json.Unmarshal(data, &copy); err != nil {
		panic("workflowtest: failed to unmarshal checkpoint: " + err.Error())
	}
	return &copy
}
