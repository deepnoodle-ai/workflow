// Package memory provides an in-memory implementation of workflow.ExecutionStore.
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// Store is an in-memory implementation of workflow.ExecutionStore for testing.
type Store struct {
	mu         sync.RWMutex
	executions map[string]*workflow.ExecutionRecord
	tasks      map[string]*workflow.TaskRecord
	events     []workflow.Event
	config     workflow.StoreConfig
}

// StoreOptions configures a memory Store.
type StoreOptions struct {
	Config workflow.StoreConfig
}

// NewStore creates a new in-memory store.
func NewStore(opts ...StoreOptions) *Store {
	config := workflow.DefaultStoreConfig()
	if len(opts) > 0 && opts[0].Config.HeartbeatInterval > 0 {
		config = opts[0].Config
	}
	return &Store{
		executions: make(map[string]*workflow.ExecutionRecord),
		tasks:      make(map[string]*workflow.TaskRecord),
		events:     make([]workflow.Event, 0),
		config:     config,
	}
}

// CreateSchema is a no-op for memory store.
func (s *Store) CreateSchema(ctx context.Context) error {
	return nil
}

// CreateExecution persists a new execution record.
func (s *Store) CreateExecution(ctx context.Context, record *workflow.ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.executions[record.ID]; exists {
		return fmt.Errorf("execution %s already exists", record.ID)
	}

	s.executions[record.ID] = s.copyExecution(record)
	return nil
}

// GetExecution retrieves an execution by ID.
func (s *Store) GetExecution(ctx context.Context, id string) (*workflow.ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.executions[id]
	if !ok {
		return nil, fmt.Errorf("execution %s not found", id)
	}
	return s.copyExecution(record), nil
}

// UpdateExecution updates an existing execution record.
func (s *Store) UpdateExecution(ctx context.Context, record *workflow.ExecutionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.executions[record.ID]; !exists {
		return fmt.Errorf("execution %s not found", record.ID)
	}

	s.executions[record.ID] = s.copyExecution(record)
	return nil
}

// ListExecutions returns executions matching the filter.
func (s *Store) ListExecutions(ctx context.Context, filter workflow.ExecutionFilter) ([]*workflow.ExecutionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*workflow.ExecutionRecord
	for _, record := range s.executions {
		if filter.WorkflowName != "" && record.WorkflowName != filter.WorkflowName {
			continue
		}
		if len(filter.Statuses) > 0 {
			match := false
			for _, status := range filter.Statuses {
				if record.Status == status {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		result = append(result, s.copyExecution(record))
	}

	if filter.Offset > 0 {
		if filter.Offset >= len(result) {
			return nil, nil
		}
		result = result[filter.Offset:]
	}
	if filter.Limit > 0 && filter.Limit < len(result) {
		result = result[:filter.Limit]
	}

	return result, nil
}

// CreateTask creates a new task.
func (s *Store) CreateTask(ctx context.Context, task *workflow.TaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[task.ID]; exists {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	s.tasks[task.ID] = s.copyTask(task)
	return nil
}

// ClaimTask atomically claims the next available task.
// Returns nil if no task is available.
func (s *Store) ClaimTask(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Find first claimable task (oldest by creation time)
	var oldest *workflow.TaskRecord
	for _, task := range s.tasks {
		if task.Status != workflow.TaskStatusPending {
			continue
		}
		if task.VisibleAt.After(now) {
			continue
		}
		if oldest == nil || task.CreatedAt.Before(oldest.CreatedAt) {
			oldest = task
		}
	}

	if oldest == nil {
		return nil, nil
	}

	// Claim it
	oldest.Status = workflow.TaskStatusRunning
	oldest.WorkerID = workerID
	oldest.StartedAt = now
	oldest.LastHeartbeat = now

	return &workflow.ClaimedTask{
		ID:                oldest.ID,
		ExecutionID:       oldest.ExecutionID,
		StepName:          oldest.StepName,
		ActivityName:      oldest.ActivityName,
		Attempt:           oldest.Attempt,
		Spec:              s.copySpec(oldest.Spec),
		HeartbeatInterval: s.config.HeartbeatInterval,
		LeaseExpiresAt:    now.Add(s.config.LeaseTimeout),
	}, nil
}

// CompleteTask marks a task as completed with the given result.
func (s *Store) CompleteTask(ctx context.Context, taskID string, workerID string, result *workflow.TaskResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if task.WorkerID != workerID {
		return fmt.Errorf("task %s not owned by worker %s", taskID, workerID)
	}

	if task.Status != workflow.TaskStatusRunning {
		return fmt.Errorf("task %s not in running state", taskID)
	}

	if result.Success {
		task.Status = workflow.TaskStatusCompleted
	} else {
		task.Status = workflow.TaskStatusFailed
	}
	task.Result = s.copyResult(result)
	task.CompletedAt = time.Now()

	return nil
}

// ReleaseTask returns a task to pending state for retry.
func (s *Store) ReleaseTask(ctx context.Context, taskID string, workerID string, retryAfter time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if task.WorkerID != workerID {
		return fmt.Errorf("task %s not owned by worker %s", taskID, workerID)
	}

	task.Status = workflow.TaskStatusPending
	task.WorkerID = ""
	task.VisibleAt = time.Now().Add(retryAfter)
	task.Attempt++
	task.LastHeartbeat = time.Time{}
	task.StartedAt = time.Time{}

	return nil
}

// HeartbeatTask updates the heartbeat timestamp for a task.
func (s *Store) HeartbeatTask(ctx context.Context, taskID string, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if task.WorkerID != workerID {
		return fmt.Errorf("task %s not owned by worker %s", taskID, workerID)
	}

	if task.Status != workflow.TaskStatusRunning {
		return fmt.Errorf("task %s not in running state", taskID)
	}

	task.LastHeartbeat = time.Now()
	return nil
}

// GetTask retrieves a task by ID.
func (s *Store) GetTask(ctx context.Context, id string) (*workflow.TaskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	task, ok := s.tasks[id]
	if !ok {
		return nil, fmt.Errorf("task %s not found", id)
	}
	return s.copyTask(task), nil
}

// ListStaleTasks returns tasks that haven't heartbeated since the cutoff.
func (s *Store) ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*workflow.TaskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*workflow.TaskRecord
	for _, task := range s.tasks {
		if task.Status == workflow.TaskStatusRunning && task.LastHeartbeat.Before(heartbeatCutoff) {
			result = append(result, s.copyTask(task))
		}
	}
	return result, nil
}

// ResetTask resets a task to pending state for recovery.
func (s *Store) ResetTask(ctx context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	task.Status = workflow.TaskStatusPending
	task.WorkerID = ""
	task.VisibleAt = time.Now()
	task.Attempt++
	task.LastHeartbeat = time.Time{}
	task.StartedAt = time.Time{}

	return nil
}

// AppendEvent adds an event to the log.
func (s *Store) AppendEvent(ctx context.Context, event workflow.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

// ListEvents retrieves events for an execution matching the filter.
func (s *Store) ListEvents(ctx context.Context, executionID string, filter workflow.EventFilter) ([]workflow.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []workflow.Event
	for _, e := range s.events {
		if e.ExecutionID != executionID {
			continue
		}
		if len(filter.Types) > 0 {
			found := false
			for _, t := range filter.Types {
				if e.Type == t {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if !filter.After.IsZero() && !e.Timestamp.After(filter.After) {
			continue
		}
		if !filter.Before.IsZero() && !e.Timestamp.Before(filter.Before) {
			continue
		}
		result = append(result, e)
		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}
	return result, nil
}

// copyExecution creates a deep copy of an execution record.
func (s *Store) copyExecution(record *workflow.ExecutionRecord) *workflow.ExecutionRecord {
	if record == nil {
		return nil
	}
	cp := *record
	if record.Inputs != nil {
		cp.Inputs = copyMapAny(record.Inputs)
	}
	if record.Outputs != nil {
		cp.Outputs = copyMapAny(record.Outputs)
	}
	return &cp
}

// copyTask creates a deep copy of a task record.
func (s *Store) copyTask(task *workflow.TaskRecord) *workflow.TaskRecord {
	if task == nil {
		return nil
	}
	cp := *task
	cp.Spec = s.copySpec(task.Spec)
	cp.Result = s.copyResult(task.Result)
	return &cp
}

// copySpec creates a deep copy of a task spec.
func (s *Store) copySpec(spec *workflow.TaskSpec) *workflow.TaskSpec {
	if spec == nil {
		return nil
	}
	cp := *spec
	if spec.Command != nil {
		cp.Command = append([]string{}, spec.Command...)
	}
	if spec.Args != nil {
		cp.Args = append([]string{}, spec.Args...)
	}
	if spec.Env != nil {
		cp.Env = make(map[string]string)
		for k, v := range spec.Env {
			cp.Env[k] = v
		}
	}
	if spec.Headers != nil {
		cp.Headers = make(map[string]string)
		for k, v := range spec.Headers {
			cp.Headers[k] = v
		}
	}
	if spec.Input != nil {
		cp.Input = copyMapAny(spec.Input)
	}
	return &cp
}

// copyResult creates a deep copy of a task result.
func (s *Store) copyResult(result *workflow.TaskResult) *workflow.TaskResult {
	if result == nil {
		return nil
	}
	cp := *result
	if result.Data != nil {
		cp.Data = copyMapAny(result.Data)
	}
	return &cp
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

// Verify interface compliance.
var _ workflow.ExecutionStore = (*Store)(nil)
