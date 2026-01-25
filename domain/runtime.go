package domain

import (
	"context"
	"time"
)

// ActivityLogEntry represents a single activity log entry.
type ActivityLogEntry struct {
	ID          string         `json:"id"`
	ExecutionID string         `json:"execution_id"`
	Activity    string         `json:"activity"`
	StepName    string         `json:"step_name"`
	PathID      string         `json:"path_id"`
	Parameters  map[string]any `json:"parameters"`
	Result      any            `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
	StartTime   time.Time      `json:"start_time"`
	Duration    float64        `json:"duration"`
}

// ActivityLogger defines the activity logging interface.
type ActivityLogger interface {
	LogActivity(ctx context.Context, entry *ActivityLogEntry) error
	GetActivityHistory(ctx context.Context, executionID string) ([]*ActivityLogEntry, error)
}

// Checkpoint contains a complete snapshot of execution state.
type Checkpoint struct {
	ID           string                `json:"id"`
	ExecutionID  string                `json:"execution_id"`
	WorkflowName string                `json:"workflow_name"`
	Status       string                `json:"status"`
	Inputs       map[string]any        `json:"inputs"`
	Outputs      map[string]any        `json:"outputs"`
	Variables    map[string]any        `json:"variables"`
	PathStates   map[string]*PathState `json:"path_states"`
	JoinStates   map[string]*JoinState `json:"join_states"`
	PathCounter  int                   `json:"path_counter"`
	Error        string                `json:"error,omitempty"`
	StartTime    time.Time             `json:"start_time,omitzero"`
	EndTime      time.Time             `json:"end_time,omitzero"`
	CheckpointAt time.Time             `json:"checkpoint_at"`
}

// Checkpointer defines the checkpoint interface.
type Checkpointer interface {
	SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error
	LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error)
	DeleteCheckpoint(ctx context.Context, executionID string) error
}
