package workflow

import (
	"context"
	"log/slog"

	"github.com/deepnoodle-ai/workflow/domain"
)

// ActivityLogEntry represents a single activity log entry.
type ActivityLogEntry = domain.ActivityLogEntry

// ActivityLogger defines the activity logging interface.
type ActivityLogger = domain.ActivityLogger

// Checkpoint contains a complete snapshot of execution state.
type Checkpoint = domain.Checkpoint

// Checkpointer defines the checkpoint interface.
type Checkpointer = domain.Checkpointer

// PathLocalState provides activities with access to workflow state.
type PathLocalState = domain.PathLocalState

// NewPathLocalState creates a new path local state.
func NewPathLocalState(inputs, variables map[string]any) *PathLocalState {
	return domain.NewPathLocalState(inputs, variables)
}

// WorkflowFormatter interface for pretty output.
type WorkflowFormatter interface {
	PrintStepStart(stepName string, activityName string)
	PrintStepOutput(stepName string, content any)
	PrintStepError(stepName string, err error)
}

// NullActivityLogger is a no-op implementation.
type NullActivityLogger struct{}

// NewNullActivityLogger creates a new null activity logger.
func NewNullActivityLogger() *NullActivityLogger {
	return &NullActivityLogger{}
}

func (l *NullActivityLogger) LogActivity(ctx context.Context, entry *ActivityLogEntry) error {
	return nil
}

func (l *NullActivityLogger) GetActivityHistory(ctx context.Context, executionID string) ([]*ActivityLogEntry, error) {
	return nil, nil
}

// NullCheckpointer is a no-op implementation.
type NullCheckpointer struct{}

// NewNullCheckpointer creates a new null checkpointer.
func NewNullCheckpointer() *NullCheckpointer {
	return &NullCheckpointer{}
}

func (c *NullCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error {
	return nil
}

func (c *NullCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return nil, nil
}

func (c *NullCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	return nil
}

// MemoryCheckpointer is an in-memory implementation for testing.
type MemoryCheckpointer struct {
	checkpoints map[string]*Checkpoint
}

// NewMemoryCheckpointer creates a new in-memory checkpointer.
func NewMemoryCheckpointer() *MemoryCheckpointer {
	return &MemoryCheckpointer{
		checkpoints: make(map[string]*Checkpoint),
	}
}

func (c *MemoryCheckpointer) SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error {
	c.checkpoints[checkpoint.ExecutionID] = checkpoint
	return nil
}

func (c *MemoryCheckpointer) LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error) {
	return c.checkpoints[executionID], nil
}

func (c *MemoryCheckpointer) DeleteCheckpoint(ctx context.Context, executionID string) error {
	delete(c.checkpoints, executionID)
	return nil
}

// NewLogger returns a default logger.
func NewLogger() *slog.Logger {
	return slog.Default()
}
