package workflow

import "time"

// Checkpoint contains a complete snapshot of execution state
type Checkpoint struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowName string                 `json:"workflow_name"`
	Status       string                 `json:"status"`
	Inputs       map[string]interface{} `json:"inputs"`
	Outputs      map[string]interface{} `json:"outputs"`
	Variables    map[string]interface{} `json:"variables"`
	PathStates   map[string]*PathState  `json:"path_states"`
	JoinStates   map[string]*JoinState  `json:"join_states"`
	PathCounter  int                    `json:"path_counter"`
	Error        string                 `json:"error,omitempty"`
	StartTime    time.Time              `json:"start_time,omitzero"`
	EndTime      time.Time              `json:"end_time,omitzero"`
	CheckpointAt time.Time              `json:"checkpoint_at"`
}
