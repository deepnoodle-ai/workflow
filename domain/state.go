package domain

import "time"

// PathState tracks the state of an execution path. This struct is designed to
// be fully JSON serializable.
type PathState struct {
	ID           string          `json:"id"`
	Status       ExecutionStatus `json:"status"`
	CurrentStep  string          `json:"current_step"`
	StartTime    time.Time       `json:"start_time,omitzero"`
	EndTime      time.Time       `json:"end_time,omitzero"`
	ErrorMessage string          `json:"error_message,omitempty"`
	StepOutputs  map[string]any  `json:"step_outputs"`
	Variables    map[string]any  `json:"variables"`
}

// JoinState tracks paths waiting at a join step.
type JoinState struct {
	StepName     string      `json:"step_name"`
	WaitingPaths []string    `json:"waiting_paths"` // All paths that have arrived at this join
	Config       *JoinConfig `json:"config"`
	CreatedAt    time.Time   `json:"created_at"`
}

// Copy returns a shallow copy of the path state.
func (p *PathState) Copy() *PathState {
	return &PathState{
		ID:           p.ID,
		Status:       p.Status,
		CurrentStep:  p.CurrentStep,
		StartTime:    p.StartTime,
		EndTime:      p.EndTime,
		ErrorMessage: p.ErrorMessage,
		StepOutputs:  copyMapAny(p.StepOutputs),
		Variables:    copyMapAny(p.Variables),
	}
}

func copyMapAny(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
