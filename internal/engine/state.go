package engine

import (
	"encoding/json"
	"time"

	"github.com/deepnoodle-ai/workflow/domain"
)

// EngineExecutionState represents the serializable state of a multi-step workflow execution.
// This is stored as JSON in ExecutionRecord.StateData.
type EngineExecutionState struct {
	// PathStates tracks the state of each execution path
	PathStates map[string]*domain.PathState `json:"path_states"`

	// JoinStates tracks paths waiting at join steps
	JoinStates map[string]*domain.JoinState `json:"join_states"`

	// PathCounter for generating sequential path IDs
	PathCounter int `json:"path_counter"`
}

// NewEngineExecutionState creates a new empty execution state.
func NewEngineExecutionState() *EngineExecutionState {
	return &EngineExecutionState{
		PathStates:  make(map[string]*domain.PathState),
		JoinStates:  make(map[string]*domain.JoinState),
		PathCounter: 0,
	}
}

// LoadState deserializes execution state from a record.
// If StateData is empty, returns a new state with default path "main".
func LoadState(record *domain.ExecutionRecord) (*EngineExecutionState, error) {
	if len(record.StateData) == 0 {
		// No state data - create new state with default "main" path
		state := NewEngineExecutionState()
		state.PathStates["main"] = &domain.PathState{
			ID:          "main",
			Status:      domain.ExecutionStatusPending,
			StepOutputs: make(map[string]any),
			Variables:   make(map[string]any),
		}
		return state, nil
	}

	var state EngineExecutionState
	if err := json.Unmarshal(record.StateData, &state); err != nil {
		return nil, err
	}

	// Initialize nil maps
	if state.PathStates == nil {
		state.PathStates = make(map[string]*domain.PathState)
	}
	if state.JoinStates == nil {
		state.JoinStates = make(map[string]*domain.JoinState)
	}

	return &state, nil
}

// Save serializes execution state to the record.
func (s *EngineExecutionState) Save(record *domain.ExecutionRecord) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	record.StateData = data
	return nil
}

// GetPathState returns the state for a specific path.
func (s *EngineExecutionState) GetPathState(pathID string) *domain.PathState {
	return s.PathStates[pathID]
}

// SetPathState sets the state for a specific path.
func (s *EngineExecutionState) SetPathState(pathID string, state *domain.PathState) {
	s.PathStates[pathID] = state
}

// StoreStepOutput stores the output of a step in the path's state.
func (s *EngineExecutionState) StoreStepOutput(pathID, stepName string, output map[string]any) {
	pathState := s.PathStates[pathID]
	if pathState == nil {
		return
	}
	if pathState.StepOutputs == nil {
		pathState.StepOutputs = make(map[string]any)
	}
	pathState.StepOutputs[stepName] = output
}

// StoreVariable stores a variable in the path's state.
func (s *EngineExecutionState) StoreVariable(pathID, varName string, value any) {
	pathState := s.PathStates[pathID]
	if pathState == nil {
		return
	}
	if pathState.Variables == nil {
		pathState.Variables = make(map[string]any)
	}
	pathState.Variables[varName] = value
}

// GeneratePathID generates a new unique path ID.
func (s *EngineExecutionState) GeneratePathID(pathName string) string {
	if pathName != "" {
		return pathName
	}
	s.PathCounter++
	return "path-" + string(rune('0'+s.PathCounter))
}

// CreatePath creates a new path with the given ID.
func (s *EngineExecutionState) CreatePath(pathID, startStep string, parentVariables map[string]any) {
	variables := make(map[string]any)
	for k, v := range parentVariables {
		variables[k] = v
	}
	s.PathStates[pathID] = &domain.PathState{
		ID:          pathID,
		Status:      domain.ExecutionStatusRunning,
		CurrentStep: startStep,
		StartTime:   time.Now(),
		StepOutputs: make(map[string]any),
		Variables:   variables,
	}
}

// MarkPathComplete marks a path as completed.
func (s *EngineExecutionState) MarkPathComplete(pathID string) {
	if pathState := s.PathStates[pathID]; pathState != nil {
		pathState.Status = domain.ExecutionStatusCompleted
		pathState.EndTime = time.Now()
	}
}

// MarkPathFailed marks a path as failed with an error message.
func (s *EngineExecutionState) MarkPathFailed(pathID, errorMsg string) {
	if pathState := s.PathStates[pathID]; pathState != nil {
		pathState.Status = domain.ExecutionStatusFailed
		pathState.ErrorMessage = errorMsg
		pathState.EndTime = time.Now()
	}
}

// MarkPathWaiting marks a path as waiting (at a join step).
func (s *EngineExecutionState) MarkPathWaiting(pathID string) {
	if pathState := s.PathStates[pathID]; pathState != nil {
		pathState.Status = domain.ExecutionStatusWaiting
	}
}

// AddPathToJoin adds a path to a join step's waiting list.
func (s *EngineExecutionState) AddPathToJoin(stepName, pathID string, config *domain.JoinConfig) {
	s.JoinStates[stepName] = &domain.JoinState{
		StepName:      stepName,
		WaitingPathID: pathID,
		Config:        config,
		CreatedAt:     time.Now(),
	}
}

// IsJoinReady checks if a join step is ready to proceed.
func (s *EngineExecutionState) IsJoinReady(stepName string) bool {
	joinState := s.JoinStates[stepName]
	if joinState == nil {
		return false
	}

	config := joinState.Config
	if config == nil {
		return false
	}

	// If specific paths are specified, check if all are completed (excluding the waiting path)
	if len(config.Paths) > 0 {
		for _, requiredPath := range config.Paths {
			if requiredPath == joinState.WaitingPathID {
				continue
			}
			pathState := s.PathStates[requiredPath]
			if pathState == nil || pathState.Status != domain.ExecutionStatusCompleted {
				return false
			}
		}
		return true
	}

	// If count is specified, count completed paths (excluding the waiting path)
	if config.Count > 0 {
		completedCount := 0
		for pathID, pathState := range s.PathStates {
			if pathID != joinState.WaitingPathID && pathState.Status == domain.ExecutionStatusCompleted {
				completedCount++
			}
		}
		return completedCount >= config.Count
	}

	// Default: wait for at least one other path to complete
	for pathID, pathState := range s.PathStates {
		if pathID != joinState.WaitingPathID && pathState.Status == domain.ExecutionStatusCompleted {
			return true
		}
	}
	return false
}

// MergePathsAtJoin merges completed paths at a join step according to PathMappings.
// Returns the merged variables for the continuing path.
func (s *EngineExecutionState) MergePathsAtJoin(stepName string) map[string]any {
	joinState := s.JoinStates[stepName]
	if joinState == nil {
		return nil
	}

	waitingPath := s.PathStates[joinState.WaitingPathID]
	if waitingPath == nil {
		return nil
	}

	// Start with the waiting path's variables
	merged := make(map[string]any)
	for k, v := range waitingPath.Variables {
		merged[k] = v
	}

	config := joinState.Config
	if config == nil || len(config.PathMappings) == 0 {
		// No mappings - just merge step outputs from completed paths
		for pathID, pathState := range s.PathStates {
			if pathID == joinState.WaitingPathID {
				continue
			}
			if pathState.Status == domain.ExecutionStatusCompleted {
				for stepName, output := range pathState.StepOutputs {
					key := pathID + "." + stepName
					merged[key] = output
				}
			}
		}
	} else {
		// Apply path mappings
		for source, dest := range config.PathMappings {
			// Parse "pathID" or "pathID.variable" format
			pathID, varName := parsePathMapping(source)
			pathState := s.PathStates[pathID]
			if pathState == nil {
				continue
			}

			if varName == "" {
				// Store entire path's variables
				merged[dest] = pathState.Variables
			} else {
				// Store specific variable
				if value, ok := pathState.Variables[varName]; ok {
					merged[dest] = value
				} else if value, ok := pathState.StepOutputs[varName]; ok {
					merged[dest] = value
				}
			}
		}
	}

	// Clean up join state
	delete(s.JoinStates, stepName)

	return merged
}

// parsePathMapping parses "pathID" or "pathID.variable" into components.
func parsePathMapping(source string) (pathID, varName string) {
	for i, c := range source {
		if c == '.' {
			return source[:i], source[i+1:]
		}
	}
	return source, ""
}

// AllPathsComplete returns true if all paths have completed or failed.
func (s *EngineExecutionState) AllPathsComplete() bool {
	for _, pathState := range s.PathStates {
		if pathState.Status != domain.ExecutionStatusCompleted &&
			pathState.Status != domain.ExecutionStatusFailed {
			return false
		}
	}
	return len(s.PathStates) > 0
}

// HasActivePaths returns true if there are any pending or running paths.
func (s *EngineExecutionState) HasActivePaths() bool {
	for _, pathState := range s.PathStates {
		if pathState.Status == domain.ExecutionStatusPending ||
			pathState.Status == domain.ExecutionStatusRunning {
			return true
		}
	}
	return false
}

// HasFailedPaths returns true if any path has failed.
func (s *EngineExecutionState) HasFailedPaths() bool {
	for _, pathState := range s.PathStates {
		if pathState.Status == domain.ExecutionStatusFailed {
			return true
		}
	}
	return false
}

// GetCompletedOutputs returns the combined outputs from all completed paths.
// For single-step workflows, returns the last step's output directly.
// For multi-step workflows, returns variables and step outputs.
func (s *EngineExecutionState) GetCompletedOutputs() map[string]any {
	outputs := make(map[string]any)

	// First, collect outputs from the main path for backward compatibility
	if mainPath := s.PathStates["main"]; mainPath != nil && mainPath.Status == domain.ExecutionStatusCompleted {
		// For main path, merge the last step's output directly
		// This ensures backward compatibility where exec.Outputs = result.Data
		var lastStepOutput map[string]any
		for _, output := range mainPath.StepOutputs {
			if outputMap, ok := output.(map[string]any); ok {
				lastStepOutput = outputMap
			}
		}
		if lastStepOutput != nil {
			for k, v := range lastStepOutput {
				outputs[k] = v
			}
		}
	}

	// Add outputs from all completed paths with path prefix
	for pathID, pathState := range s.PathStates {
		if pathState.Status == domain.ExecutionStatusCompleted {
			for stepName, output := range pathState.StepOutputs {
				outputs[pathID+"."+stepName] = output
			}
		}
	}

	return outputs
}
