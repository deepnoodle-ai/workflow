// Package testutil provides shared test utilities and mocks.
package testutil

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// ErrNotFound is returned when a requested resource doesn't exist.
var ErrNotFound = errors.New("not found")

// MockExecutionRepository is a controllable mock for execution operations.
type MockExecutionRepository struct {
	mu         sync.Mutex
	executions map[string]*domain.ExecutionRecord

	// Error injection
	CreateErr error
	GetErr    error
	UpdateErr error
	ListErr   error
}

// NewMockExecutionRepository creates a new mock execution repository.
func NewMockExecutionRepository() *MockExecutionRepository {
	return &MockExecutionRepository{
		executions: make(map[string]*domain.ExecutionRecord),
	}
}

func (m *MockExecutionRepository) CreateExecution(ctx context.Context, record *domain.ExecutionRecord) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executions[record.ID] = record
	return nil
}

func (m *MockExecutionRepository) GetExecution(ctx context.Context, id string) (*domain.ExecutionRecord, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.executions[id]
	if !ok {
		return nil, ErrNotFound
	}
	// Return a copy to prevent mutation
	copy := *rec
	return &copy, nil
}

func (m *MockExecutionRepository) UpdateExecution(ctx context.Context, record *domain.ExecutionRecord) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.executions[record.ID]; !ok {
		return ErrNotFound
	}
	m.executions[record.ID] = record
	return nil
}

func (m *MockExecutionRepository) ListExecutions(ctx context.Context, filter domain.ExecutionFilter) ([]*domain.ExecutionRecord, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.ExecutionRecord
	for _, rec := range m.executions {
		if filter.WorkflowName != "" && rec.WorkflowName != filter.WorkflowName {
			continue
		}
		if len(filter.Statuses) > 0 {
			found := false
			for _, s := range filter.Statuses {
				if rec.Status == s {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		copy := *rec
		result = append(result, &copy)
	}
	return result, nil
}

// GetAll returns all executions (for test assertions).
func (m *MockExecutionRepository) GetAll() []*domain.ExecutionRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*domain.ExecutionRecord, 0, len(m.executions))
	for _, rec := range m.executions {
		copy := *rec
		result = append(result, &copy)
	}
	return result
}

// MockTaskRepository is a controllable mock for task operations.
type MockTaskRepository struct {
	mu    sync.Mutex
	tasks map[string]*domain.TaskRecord

	// Error injection
	CreateErr    error
	ClaimErr     error
	CompleteErr  error
	ReleaseErr   error
	HeartbeatErr error
	GetErr       error
	ListStaleErr error
	ResetErr     error
}

// NewMockTaskRepository creates a new mock task repository.
func NewMockTaskRepository() *MockTaskRepository {
	return &MockTaskRepository{
		tasks: make(map[string]*domain.TaskRecord),
	}
}

func (m *MockTaskRepository) CreateTask(ctx context.Context, t *domain.TaskRecord) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[t.ID] = t
	return nil
}

func (m *MockTaskRepository) ClaimTask(ctx context.Context, workerID string) (*domain.TaskClaimed, error) {
	if m.ClaimErr != nil {
		return nil, m.ClaimErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.Status == domain.TaskStatusPending && (t.VisibleAt.IsZero() || time.Now().After(t.VisibleAt)) {
			t.Status = domain.TaskStatusRunning
			t.WorkerID = workerID
			t.StartedAt = time.Now()
			t.LastHeartbeat = time.Now()
			return &domain.TaskClaimed{
				ID:                t.ID,
				ExecutionID:       t.ExecutionID,
				PathID:            t.PathID,
				StepName:          t.StepName,
				ActivityName:      t.ActivityName,
				Attempt:           t.Attempt,
				Input:             t.Input,
				HeartbeatInterval: 30 * time.Second,
				LeaseExpiresAt:    time.Now().Add(2 * time.Minute),
			}, nil
		}
	}
	return nil, nil
}

func (m *MockTaskRepository) CompleteTask(ctx context.Context, taskID, workerID string, output *domain.TaskOutput) error {
	if m.CompleteErr != nil {
		return m.CompleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	if t.WorkerID != workerID {
		return ErrNotFound
	}
	if output.Success {
		t.Status = domain.TaskStatusCompleted
	} else {
		t.Status = domain.TaskStatusFailed
	}
	t.Output = output
	t.CompletedAt = time.Now()
	return nil
}

func (m *MockTaskRepository) ReleaseTask(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error {
	if m.ReleaseErr != nil {
		return m.ReleaseErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	if t.WorkerID != workerID {
		return ErrNotFound
	}
	t.Status = domain.TaskStatusPending
	t.WorkerID = ""
	t.VisibleAt = time.Now().Add(retryAfter)
	t.Attempt++
	return nil
}

func (m *MockTaskRepository) HeartbeatTask(ctx context.Context, taskID, workerID string) error {
	if m.HeartbeatErr != nil {
		return m.HeartbeatErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	if t.WorkerID != workerID {
		return ErrNotFound
	}
	t.LastHeartbeat = time.Now()
	return nil
}

func (m *MockTaskRepository) GetTask(ctx context.Context, id string) (*domain.TaskRecord, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	copy := *t
	return &copy, nil
}

func (m *MockTaskRepository) ListStaleTasks(ctx context.Context, heartbeatCutoff time.Time) ([]*domain.TaskRecord, error) {
	if m.ListStaleErr != nil {
		return nil, m.ListStaleErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*domain.TaskRecord
	for _, t := range m.tasks {
		if t.Status == domain.TaskStatusRunning && t.LastHeartbeat.Before(heartbeatCutoff) {
			copy := *t
			result = append(result, &copy)
		}
	}
	return result, nil
}

func (m *MockTaskRepository) ResetTask(ctx context.Context, taskID string) error {
	if m.ResetErr != nil {
		return m.ResetErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	t.Status = domain.TaskStatusPending
	t.WorkerID = ""
	t.Attempt++
	return nil
}

// GetAll returns all tasks (for test assertions).
func (m *MockTaskRepository) GetAll() []*domain.TaskRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*domain.TaskRecord, 0, len(m.tasks))
	for _, t := range m.tasks {
		copy := *t
		result = append(result, &copy)
	}
	return result
}

// AddTask adds a task directly (for test setup).
func (m *MockTaskRepository) AddTask(t *domain.TaskRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks[t.ID] = t
}

// MockEventLog captures logged events for verification.
type MockEventLog struct {
	mu     sync.Mutex
	events []domain.Event

	// Error injection
	AppendErr error
	ListErr   error
}

// NewMockEventLog creates a new mock event log.
func NewMockEventLog() *MockEventLog {
	return &MockEventLog{
		events: make([]domain.Event, 0),
	}
}

func (m *MockEventLog) Append(ctx context.Context, event domain.Event) error {
	if m.AppendErr != nil {
		return m.AppendErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *MockEventLog) List(ctx context.Context, executionID string, filter domain.EventFilter) ([]domain.Event, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.Event
	for _, e := range m.events {
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
		result = append(result, e)
	}
	return result, nil
}

// GetEvents returns all captured events (for test assertions).
func (m *MockEventLog) GetEvents() []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]domain.Event, len(m.events))
	copy(result, m.events)
	return result
}

// GetEventsByType returns events of a specific type.
func (m *MockEventLog) GetEventsByType(eventType domain.EventType) []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []domain.Event
	for _, e := range m.events {
		if e.Type == eventType {
			result = append(result, e)
		}
	}
	return result
}

// Clear removes all captured events.
func (m *MockEventLog) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = m.events[:0]
}

// TestExecution creates a test execution record.
func TestExecution(id, workflowName string, status domain.ExecutionStatus) *domain.ExecutionRecord {
	return &domain.ExecutionRecord{
		ID:           id,
		WorkflowName: workflowName,
		Status:       status,
		Inputs:       make(map[string]any),
		CreatedAt:    time.Now(),
	}
}

// TestTask creates a test task record.
func TestTask(id, executionID, stepName string, status domain.TaskStatus) *domain.TaskRecord {
	return &domain.TaskRecord{
		ID:           id,
		ExecutionID:  executionID,
		PathID:       "main",
		StepName:     stepName,
		ActivityName: stepName + "_activity",
		Attempt:      1,
		Status:       status,
		Input:        &domain.TaskInput{Type: "inline"},
		CreatedAt:    time.Now(),
	}
}
