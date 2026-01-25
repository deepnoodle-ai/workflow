package services

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
	"github.com/google/uuid"
)

// TaskService coordinates task operations with event logging.
type TaskService struct {
	tasks  domain.TaskRepository
	events domain.EventLog
}

// TaskServiceOptions configures a TaskService.
type TaskServiceOptions struct {
	Tasks  domain.TaskRepository
	Events domain.EventLog
}

// NewTaskService creates a new task service.
func NewTaskService(opts TaskServiceOptions) *TaskService {
	return &TaskService{
		tasks:  opts.Tasks,
		events: opts.Events,
	}
}

// Create persists a new task record.
func (s *TaskService) Create(ctx context.Context, t *domain.TaskRecord) error {
	return s.tasks.CreateTask(ctx, t)
}

// Claim atomically claims the next available task and logs a step_started event.
func (s *TaskService) Claim(ctx context.Context, workerID string) (*domain.TaskClaimed, error) {
	claimed, err := s.tasks.ClaimTask(ctx, workerID)
	if err != nil {
		return nil, err
	}
	if claimed == nil {
		return nil, nil
	}

	if s.events != nil {
		_ = s.events.Append(ctx, domain.Event{
			ID:          "event_" + uuid.New().String(),
			ExecutionID: claimed.ExecutionID,
			Timestamp:   time.Now(),
			Type:        domain.EventTypeStepStarted,
			StepName:    claimed.StepName,
			Attempt:     claimed.Attempt,
			Data: map[string]any{
				"task_id":       claimed.ID,
				"worker_id":     workerID,
				"activity_name": claimed.ActivityName,
			},
		})
	}

	return claimed, nil
}

// Complete marks a task as completed and logs a step_completed or step_failed event.
func (s *TaskService) Complete(ctx context.Context, taskID, workerID string, result *domain.TaskOutput) error {
	// Get task info for event logging before completing
	var t *domain.TaskRecord
	if s.events != nil {
		t, _ = s.tasks.GetTask(ctx, taskID)
	}

	if err := s.tasks.CompleteTask(ctx, taskID, workerID, result); err != nil {
		return err
	}

	if s.events != nil && t != nil {
		eventType := domain.EventTypeStepCompleted
		if !result.Success {
			eventType = domain.EventTypeStepFailed
		}

		_ = s.events.Append(ctx, domain.Event{
			ID:          "event_" + uuid.New().String(),
			ExecutionID: t.ExecutionID,
			Timestamp:   time.Now(),
			Type:        eventType,
			StepName:    t.StepName,
			Attempt:     t.Attempt,
			Data: map[string]any{
				"task_id":   taskID,
				"worker_id": workerID,
				"output":    result.Data,
			},
			Error: result.Error,
		})
	}

	return nil
}

// Release returns a task to pending state for retry and logs a step_retrying event.
func (s *TaskService) Release(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error {
	// Get task info for event logging before releasing
	var t *domain.TaskRecord
	if s.events != nil {
		t, _ = s.tasks.GetTask(ctx, taskID)
	}

	if err := s.tasks.ReleaseTask(ctx, taskID, workerID, retryAfter); err != nil {
		return err
	}

	if s.events != nil && t != nil {
		_ = s.events.Append(ctx, domain.Event{
			ID:          "event_" + uuid.New().String(),
			ExecutionID: t.ExecutionID,
			Timestamp:   time.Now(),
			Type:        domain.EventTypeStepRetrying,
			StepName:    t.StepName,
			Attempt:     t.Attempt,
			Data: map[string]any{
				"task_id":     taskID,
				"worker_id":   workerID,
				"retry_after": retryAfter.String(),
			},
		})
	}

	return nil
}

// Heartbeat updates the heartbeat timestamp for a running task.
func (s *TaskService) Heartbeat(ctx context.Context, taskID, workerID string) error {
	return s.tasks.HeartbeatTask(ctx, taskID, workerID)
}

// Get retrieves a task by ID.
func (s *TaskService) Get(ctx context.Context, id string) (*domain.TaskRecord, error) {
	return s.tasks.GetTask(ctx, id)
}

// ListStale returns tasks that haven't heartbeated since the cutoff.
func (s *TaskService) ListStale(ctx context.Context, cutoff time.Time) ([]*domain.TaskRecord, error) {
	return s.tasks.ListStaleTasks(ctx, cutoff)
}

// Reset resets a task to pending state for recovery.
func (s *TaskService) Reset(ctx context.Context, taskID string) error {
	return s.tasks.ResetTask(ctx, taskID)
}
