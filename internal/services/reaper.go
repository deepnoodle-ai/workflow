package services

import (
	"context"
	"log/slog"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// ReaperTaskRepository defines the task operations needed by ReaperService.
type ReaperTaskRepository interface {
	ListStaleTasks(ctx context.Context, cutoff time.Time) ([]*domain.TaskRecord, error)
	ResetTask(ctx context.Context, taskID string) error
}

// ReaperService handles detection and recovery of stale tasks.
type ReaperService struct {
	tasks            ReaperTaskRepository
	heartbeatTimeout time.Duration
	logger           *slog.Logger
}

// ReaperServiceOptions configures a ReaperService.
type ReaperServiceOptions struct {
	Tasks            ReaperTaskRepository
	HeartbeatTimeout time.Duration
	Logger           *slog.Logger
}

// NewReaperService creates a new reaper service.
func NewReaperService(opts ReaperServiceOptions) *ReaperService {
	if opts.HeartbeatTimeout == 0 {
		opts.HeartbeatTimeout = 2 * time.Minute
	}
	return &ReaperService{
		tasks:            opts.Tasks,
		heartbeatTimeout: opts.HeartbeatTimeout,
		logger:           opts.Logger,
	}
}

// ReapStaleTasks finds and resets tasks that have timed out.
// Returns the number of tasks that were reset.
func (s *ReaperService) ReapStaleTasks(ctx context.Context) (int, error) {
	cutoff := time.Now().Add(-s.heartbeatTimeout)
	staleTasks, err := s.tasks.ListStaleTasks(ctx, cutoff)
	if err != nil {
		return 0, err
	}

	resetCount := 0
	for _, task := range staleTasks {
		if s.logger != nil {
			s.logger.Info("resetting stale task", "task_id", task.ID, "worker_id", task.WorkerID)
		}
		if err := s.tasks.ResetTask(ctx, task.ID); err != nil {
			if s.logger != nil {
				s.logger.Warn("failed to reset task", "task_id", task.ID, "error", err)
			}
			continue
		}
		resetCount++
	}

	return resetCount, nil
}

// RecoverStaleTasks is called at startup to recover any stale tasks.
// Returns the number of tasks that were reset.
func (s *ReaperService) RecoverStaleTasks(ctx context.Context) (int, error) {
	return s.ReapStaleTasks(ctx)
}
