package workflow

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// StepStatus represents the execution state of a step.
type StepStatus string

const (
	StepStatusPending   StepStatus = "pending"
	StepStatusRunning   StepStatus = "running"
	StepStatusCompleted StepStatus = "completed"
	StepStatusFailed    StepStatus = "failed"
	StepStatusSkipped   StepStatus = "skipped"
)

// StepProgress describes the current state of a step within an execution.
type StepProgress struct {
	// StepName identifies the step.
	StepName string

	// PathID identifies the execution path this step is running on.
	PathID string

	// Status is the current step status.
	Status StepStatus

	// ActivityName is the activity bound to this step.
	ActivityName string

	// Attempt is the current attempt number (1-based). Increments on retries.
	Attempt int

	// Detail is optional progress information set by activities via
	// workflow.ReportProgress(). Nil unless the activity reports intra-step
	// progress.
	Detail *ProgressDetail

	// StartedAt is when the step began executing. Zero for pending steps.
	StartedAt time.Time

	// FinishedAt is when the step completed or failed. Zero for running steps.
	FinishedAt time.Time

	// Error is the error message if the step failed. Empty otherwise.
	Error string
}

// StepProgressStore persists step progress updates. Implement this interface
// to write step progress to your backend (database, cache, API, etc.).
//
// UpdateStepProgress is called asynchronously on every step state transition
// and on intra-activity progress reports. Errors are logged but do not fail
// the workflow — step progress is observability, not correctness.
type StepProgressStore interface {
	UpdateStepProgress(ctx context.Context, executionID string, progress StepProgress) error
}

// stepKey is a compound key for step progress tracking.
type stepKey struct {
	stepName string
	pathID   string
}

// stepProgressTracker listens to execution callbacks and derives step
// state transitions. It calls the StepProgressStore asynchronously on
// each transition.
type stepProgressTracker struct {
	BaseExecutionCallbacks
	executionID string
	store       StepProgressStore
	logger      *slog.Logger
	mu          sync.Mutex
	steps       map[stepKey]*StepProgress
}

func newStepProgressTracker(executionID string, store StepProgressStore, logger *slog.Logger) *stepProgressTracker {
	return &stepProgressTracker{
		executionID: executionID,
		store:       store,
		logger:      logger,
		steps:       make(map[stepKey]*StepProgress),
	}
}

func (t *stepProgressTracker) dispatch(_ context.Context, progress StepProgress) {
	go func() {
		if err := t.store.UpdateStepProgress(context.Background(), t.executionID, progress); err != nil {
			t.logger.Error("step progress update failed",
				"step", progress.StepName,
				"path", progress.PathID,
				"error", err,
			)
		}
	}()
}

func (t *stepProgressTracker) BeforeActivityExecution(ctx context.Context, event *ActivityExecutionEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := stepKey{stepName: event.StepName, pathID: event.PathID}
	existing := t.steps[key]
	attempt := 1
	if existing != nil && existing.Status != StepStatusCompleted {
		attempt = existing.Attempt + 1
	}

	progress := StepProgress{
		StepName:     event.StepName,
		PathID:       event.PathID,
		Status:       StepStatusRunning,
		ActivityName: event.ActivityName,
		Attempt:      attempt,
		StartedAt:    event.StartTime,
	}
	t.steps[key] = &progress
	t.dispatch(ctx, progress)
}

func (t *stepProgressTracker) AfterActivityExecution(ctx context.Context, event *ActivityExecutionEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := stepKey{stepName: event.StepName, pathID: event.PathID}
	existing := t.steps[key]
	if existing == nil {
		return
	}

	if event.Error != nil {
		existing.Status = StepStatusFailed
		existing.Error = event.Error.Error()
	} else {
		existing.Status = StepStatusCompleted
	}
	existing.FinishedAt = event.EndTime

	t.dispatch(ctx, *existing)
}

// reportProgress is called from the execution context to report intra-activity progress.
func (t *stepProgressTracker) reportProgress(ctx context.Context, stepName, pathID string, detail ProgressDetail) {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := stepKey{stepName: stepName, pathID: pathID}
	existing := t.steps[key]
	if existing == nil {
		return
	}

	existing.Detail = &detail
	t.dispatch(ctx, *existing)
}
