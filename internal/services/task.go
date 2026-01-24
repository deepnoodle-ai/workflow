package services

import (
	"context"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// TaskRepository defines the task operations needed by TaskService.
type TaskRepository interface {
	CreateTask(ctx context.Context, task *workflow.TaskRecord) error
	ClaimTask(ctx context.Context, workerID string) (*workflow.ClaimedTask, error)
	CompleteTask(ctx context.Context, taskID, workerID string, result *workflow.TaskResult) error
	ReleaseTask(ctx context.Context, taskID, workerID string, retryAfter time.Duration) error
	HeartbeatTask(ctx context.Context, taskID, workerID string) error
	GetTask(ctx context.Context, id string) (*workflow.TaskRecord, error)
	ListStaleTasks(ctx context.Context, cutoff time.Time) ([]*workflow.TaskRecord, error)
	ResetTask(ctx context.Context, taskID string) error
}

// TaskService coordinates task operations with event logging.
type TaskService struct {
	tasks  TaskRepository
	events EventRepository
}

// TaskServiceOptions configures a TaskService.
type TaskServiceOptions struct {
	Tasks  TaskRepository
	Events EventRepository
}

// NewTaskService creates a new task service.
func NewTaskService(opts TaskServiceOptions) *TaskService {
	return &TaskService{
		tasks:  opts.Tasks,
		events: opts.Events,
	}
}

// Create persists a new task record.
func (s *TaskService) Create(ctx context.Context, task *workflow.TaskRecord) error {
	return s.tasks.CreateTask(ctx, task)
}

// Claim atomically claims the next available task and logs a step_started event.
func (s *TaskService) Claim(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
	task, err := s.tasks.ClaimTask(ctx, workerID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}

	if s.events != nil {
		_ = s.events.AppendEvent(ctx, workflow.Event{
			ID:          workflow.NewExecutionID(),
			ExecutionID: task.ExecutionID,
			Timestamp:   time.Now(),
			Type:        workflow.EventStepStarted,
			StepName:    task.StepName,
			Attempt:     task.Attempt,
			Data: map[string]any{
				"task_id":       task.ID,
				"worker_id":     workerID,
				"activity_name": task.ActivityName,
			},
		})
	}

	return task, nil
}

// Complete marks a task as completed and logs a step_completed or step_failed event.
func (s *TaskService) Complete(ctx context.Context, taskID, workerID string, result *workflow.TaskResult) error {
	// Get task info for event logging before completing
	var task *workflow.TaskRecord
	if s.events != nil {
		task, _ = s.tasks.GetTask(ctx, taskID)
	}

	if err := s.tasks.CompleteTask(ctx, taskID, workerID, result); err != nil {
		return err
	}

	if s.events != nil && task != nil {
		eventType := workflow.EventStepCompleted
		if !result.Success {
			eventType = workflow.EventStepFailed
		}

		_ = s.events.AppendEvent(ctx, workflow.Event{
			ID:          workflow.NewExecutionID(),
			ExecutionID: task.ExecutionID,
			Timestamp:   time.Now(),
			Type:        eventType,
			StepName:    task.StepName,
			Attempt:     task.Attempt,
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
	var task *workflow.TaskRecord
	if s.events != nil {
		task, _ = s.tasks.GetTask(ctx, taskID)
	}

	if err := s.tasks.ReleaseTask(ctx, taskID, workerID, retryAfter); err != nil {
		return err
	}

	if s.events != nil && task != nil {
		_ = s.events.AppendEvent(ctx, workflow.Event{
			ID:          workflow.NewExecutionID(),
			ExecutionID: task.ExecutionID,
			Timestamp:   time.Now(),
			Type:        workflow.EventStepRetrying,
			StepName:    task.StepName,
			Attempt:     task.Attempt,
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
func (s *TaskService) Get(ctx context.Context, id string) (*workflow.TaskRecord, error) {
	return s.tasks.GetTask(ctx, id)
}

// ListStale returns tasks that haven't heartbeated since the cutoff.
func (s *TaskService) ListStale(ctx context.Context, cutoff time.Time) ([]*workflow.TaskRecord, error) {
	return s.tasks.ListStaleTasks(ctx, cutoff)
}

// Reset resets a task to pending state for recovery.
func (s *TaskService) Reset(ctx context.Context, taskID string) error {
	return s.tasks.ResetTask(ctx, taskID)
}
