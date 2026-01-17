package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation of ExecutionStore for testing and single-process deployments.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]*ExecutionRecord
}

// NewMemoryStore creates a new in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		records: make(map[string]*ExecutionRecord),
	}
}

// Create persists a new execution record.
func (s *MemoryStore) Create(ctx context.Context, record *ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.records[record.ID]; exists {
		return fmt.Errorf("execution %q already exists", record.ID)
	}

	// Make a copy to prevent external modifications
	s.records[record.ID] = s.copyRecord(record)
	return nil
}

// Get retrieves an execution record by ID.
func (s *MemoryStore) Get(ctx context.Context, id string) (*ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, exists := s.records[id]
	if !exists {
		return nil, fmt.Errorf("execution %q not found", id)
	}

	return s.copyRecord(record), nil
}

// List retrieves execution records matching the filter.
func (s *MemoryStore) List(ctx context.Context, filter ListFilter) ([]*ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*ExecutionRecord
	for _, record := range s.records {
		if s.matchesFilter(record, filter) {
			results = append(results, s.copyRecord(record))
		}
	}

	// Apply offset and limit
	if filter.Offset > 0 {
		if filter.Offset >= len(results) {
			return nil, nil
		}
		results = results[filter.Offset:]
	}
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[:filter.Limit]
	}

	return results, nil
}

// ClaimExecution atomically updates status from pending to running if the
// current attempt matches.
func (s *MemoryStore) ClaimExecution(ctx context.Context, id string, workerID string, expectedAttempt int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[id]
	if !exists {
		return false, fmt.Errorf("execution %q not found", id)
	}

	// Fencing: only claim if status=pending AND attempt matches
	if record.Status != EngineStatusPending || record.Attempt != expectedAttempt {
		return false, nil
	}

	record.Status = EngineStatusRunning
	record.WorkerID = workerID
	record.StartedAt = time.Now()
	record.LastHeartbeat = time.Now()

	return true, nil
}

// CompleteExecution atomically updates to completed/failed status if the attempt matches.
func (s *MemoryStore) CompleteExecution(ctx context.Context, id string, expectedAttempt int, status EngineExecutionStatus, outputs map[string]any, lastError string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[id]
	if !exists {
		return false, fmt.Errorf("execution %q not found", id)
	}

	// Fencing: only complete if attempt matches and status is running
	if record.Attempt != expectedAttempt || record.Status != EngineStatusRunning {
		return false, nil
	}

	record.Status = status
	record.Outputs = copyMapAny(outputs)
	record.LastError = lastError
	record.CompletedAt = time.Now()

	return true, nil
}

// MarkDispatched sets dispatched_at timestamp for dispatch mode tracking.
func (s *MemoryStore) MarkDispatched(ctx context.Context, id string, attempt int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[id]
	if !exists {
		return fmt.Errorf("execution %q not found", id)
	}

	if record.Attempt != attempt {
		return fmt.Errorf("attempt mismatch: expected %d, got %d", attempt, record.Attempt)
	}

	record.DispatchedAt = time.Now()
	return nil
}

// Heartbeat updates the last_heartbeat timestamp for liveness tracking.
func (s *MemoryStore) Heartbeat(ctx context.Context, id string, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, exists := s.records[id]
	if !exists {
		return fmt.Errorf("execution %q not found", id)
	}

	// Only update if this worker owns the execution
	if record.WorkerID != workerID {
		return fmt.Errorf("worker %q does not own execution %q", workerID, id)
	}

	record.LastHeartbeat = time.Now()
	return nil
}

// ListStaleRunning returns executions in running state with heartbeat older than cutoff.
func (s *MemoryStore) ListStaleRunning(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*ExecutionRecord
	for _, record := range s.records {
		if record.Status == EngineStatusRunning && !record.LastHeartbeat.IsZero() && record.LastHeartbeat.Before(cutoff) {
			results = append(results, s.copyRecord(record))
		}
	}
	return results, nil
}

// ListStalePending returns executions in pending state with dispatched_at older than cutoff.
func (s *MemoryStore) ListStalePending(ctx context.Context, cutoff time.Time) ([]*ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var results []*ExecutionRecord
	for _, record := range s.records {
		if record.Status == EngineStatusPending && !record.DispatchedAt.IsZero() && record.DispatchedAt.Before(cutoff) {
			results = append(results, s.copyRecord(record))
		}
	}
	return results, nil
}

// Update updates an execution record.
func (s *MemoryStore) Update(ctx context.Context, record *ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.records[record.ID]; !exists {
		return fmt.Errorf("execution %q not found", record.ID)
	}

	s.records[record.ID] = s.copyRecord(record)
	return nil
}

// matchesFilter checks if a record matches the given filter.
func (s *MemoryStore) matchesFilter(record *ExecutionRecord, filter ListFilter) bool {
	if filter.WorkflowName != "" && record.WorkflowName != filter.WorkflowName {
		return false
	}
	if len(filter.Statuses) > 0 {
		found := false
		for _, status := range filter.Statuses {
			if record.Status == status {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// copyRecord creates a deep copy of an execution record.
func (s *MemoryStore) copyRecord(record *ExecutionRecord) *ExecutionRecord {
	return &ExecutionRecord{
		ID:            record.ID,
		WorkflowName:  record.WorkflowName,
		Status:        record.Status,
		Inputs:        copyMapAny(record.Inputs),
		Outputs:       copyMapAny(record.Outputs),
		Attempt:       record.Attempt,
		WorkerID:      record.WorkerID,
		LastHeartbeat: record.LastHeartbeat,
		DispatchedAt:  record.DispatchedAt,
		CreatedAt:     record.CreatedAt,
		StartedAt:     record.StartedAt,
		CompletedAt:   record.CompletedAt,
		LastError:     record.LastError,
		CheckpointID:  record.CheckpointID,
	}
}

// copyMapAny creates a shallow copy of a map[string]any.
func copyMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
