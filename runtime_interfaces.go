package workflow

import (
	"context"
	"log/slog"
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
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowName string                 `json:"workflow_name"`
	Status       string                 `json:"status"`
	Inputs       map[string]any         `json:"inputs"`
	Outputs      map[string]any         `json:"outputs"`
	Variables    map[string]any         `json:"variables"`
	PathStates   map[string]*PathState  `json:"path_states"`
	JoinStates   map[string]*JoinState  `json:"join_states"`
	PathCounter  int                    `json:"path_counter"`
	Error        string                 `json:"error,omitempty"`
	StartTime    time.Time              `json:"start_time,omitzero"`
	EndTime      time.Time              `json:"end_time,omitzero"`
	CheckpointAt time.Time              `json:"checkpoint_at"`
}

// Checkpointer defines the checkpoint interface.
type Checkpointer interface {
	SaveCheckpoint(ctx context.Context, checkpoint *Checkpoint) error
	LoadCheckpoint(ctx context.Context, executionID string) (*Checkpoint, error)
	DeleteCheckpoint(ctx context.Context, executionID string) error
}

// WorkflowFormatter interface for pretty output.
type WorkflowFormatter interface {
	PrintStepStart(stepName string, activityName string)
	PrintStepOutput(stepName string, content any)
	PrintStepError(stepName string, err error)
}

// PathLocalState provides activities with access to workflow state.
type PathLocalState struct {
	inputs    map[string]any
	variables map[string]any
}

// NewPathLocalState creates a new path local state.
func NewPathLocalState(inputs, variables map[string]any) *PathLocalState {
	return &PathLocalState{
		inputs:    copyMapAny(inputs),
		variables: copyMapAny(variables),
	}
}

// ListInputs returns all input keys.
func (s *PathLocalState) ListInputs() []string {
	keys := make([]string, 0, len(s.inputs))
	for key := range s.inputs {
		keys = append(keys, key)
	}
	return keys
}

// GetInput returns an input value.
func (s *PathLocalState) GetInput(key string) (any, bool) {
	value, exists := s.inputs[key]
	return value, exists
}

// SetVariable sets a variable value.
func (s *PathLocalState) SetVariable(key string, value any) {
	s.variables[key] = value
}

// DeleteVariable deletes a variable.
func (s *PathLocalState) DeleteVariable(key string) {
	delete(s.variables, key)
}

// ListVariables returns all variable keys.
func (s *PathLocalState) ListVariables() []string {
	keys := make([]string, 0, len(s.variables))
	for key := range s.variables {
		keys = append(keys, key)
	}
	return keys
}

// GetVariable returns a variable value.
func (s *PathLocalState) GetVariable(key string) (any, bool) {
	value, exists := s.variables[key]
	return value, exists
}

// Variables returns a copy of all variables.
func (s *PathLocalState) Variables() map[string]any {
	return copyMapAny(s.variables)
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

// NewLogger returns a default logger.
func NewLogger() *slog.Logger {
	return slog.Default()
}
