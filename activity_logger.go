package workflow

import (
	"context"
	"time"
)

// ActivityLogEntry represents a single operation log entry
type ActivityLogEntry struct {
	ID          string                 `json:"id"`
	ExecutionID string                 `json:"execution_id"`
	Activity    string                 `json:"activity"`
	StepName    string                 `json:"step_name"`
	PathID      string                 `json:"path_id"`
	Parameters  map[string]interface{} `json:"parameters"`
	Result      interface{}            `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	StartTime   time.Time              `json:"start_time"`
	Duration    float64                `json:"duration"`
}

// ActivityLogger defines simple operation logging interface
type ActivityLogger interface {
	// LogActivity logs a completed activity
	LogActivity(ctx context.Context, entry *ActivityLogEntry) error

	// GetActivityHistory retrieves activity log for an execution
	GetActivityHistory(ctx context.Context, executionID string) ([]*ActivityLogEntry, error)
}
