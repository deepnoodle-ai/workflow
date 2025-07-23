package workflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

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

// JoinState tracks a path waiting at a join step
type JoinState struct {
	StepName      string      `json:"step_name"`
	WaitingPathID string      `json:"waiting_path_id"` // The single path that's waiting
	Config        *JoinConfig `json:"config"`
	CreatedAt     time.Time   `json:"created_at"`
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
		StepOutputs:  copyMap(p.StepOutputs),
		Variables:    copyMap(p.Variables),
	}
}

// ExecutionState consolidates all execution state into a single structure. All
// data here is serializable for checkpointing.
type ExecutionState struct {
	executionID  string
	workflowName string
	status       ExecutionStatus
	startTime    time.Time
	endTime      time.Time
	err          string
	inputs       map[string]any
	outputs      map[string]any
	pathCounter  int
	pathStates   map[string]*PathState
	joinStates   map[string]*JoinState // stepName -> JoinState
	mutex        sync.RWMutex
}

// newExecutionState creates a new unified execution state
func newExecutionState(executionID, workflowName string, inputs map[string]any) *ExecutionState {
	return &ExecutionState{
		executionID:  executionID,
		workflowName: workflowName,
		status:       ExecutionStatusPending,
		inputs:       copyMap(inputs),
		outputs:      map[string]any{},
		pathStates:   map[string]*PathState{},
		joinStates:   map[string]*JoinState{},
	}
}

// ID returns the execution ID
func (s *ExecutionState) ID() string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.executionID
}

// SetID sets the execution ID
func (s *ExecutionState) SetID(id string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.executionID = id
}

// GetStatus returns the current execution status
func (s *ExecutionState) GetStatus() ExecutionStatus {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.status
}

// SetStatus updates the execution status
func (s *ExecutionState) SetStatus(status ExecutionStatus) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.status = status
	if status != ExecutionStatusFailed {
		s.err = ""
	}
}

// SetError sets the execution error
func (s *ExecutionState) SetError(err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if err != nil {
		s.err = err.Error()
		s.status = ExecutionStatusFailed
	} else {
		s.err = ""
	}
}

// GetError returns the current execution error
func (s *ExecutionState) GetError() error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if s.err == "" {
		return nil
	}
	return errors.New(s.err)
}

// SetTiming updates the execution timing
func (s *ExecutionState) SetTiming(startTime, endTime time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.startTime = startTime
	s.endTime = endTime
}

func (s *ExecutionState) SetFinished(status ExecutionStatus, endTime time.Time, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.status = status
	s.endTime = endTime
	if err != nil {
		s.err = err.Error()
	} else {
		s.err = ""
	}
}

// NextPathID generates a new unique path ID
func (s *ExecutionState) NextPathID(baseID string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.pathCounter++
	return baseID + "-" + fmt.Sprintf("%d", s.pathCounter)
}

// GeneratePathID creates a path ID, using pathName if provided, otherwise generating a sequential ID
func (s *ExecutionState) GeneratePathID(parentID, pathName string) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var pathID string
	if pathName != "" {
		// Use the provided path name as the ID
		pathID = pathName
		// Check for duplicate path names
		if _, exists := s.pathStates[pathID]; exists {
			return "", fmt.Errorf("duplicate path name: %q", pathName)
		}
	} else {
		// Default to generating sequential IDs
		s.pathCounter++
		pathID = parentID + "-" + fmt.Sprintf("%d", s.pathCounter)
	}
	return pathID, nil
}

// SetPathState sets or updates a path state
func (s *ExecutionState) SetPathState(pathID string, state *PathState) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.pathStates[pathID] = state.Copy()
}

// GetPathState retrieves a path state
func (s *ExecutionState) GetPathStates() map[string]*PathState {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyPathStates(s.pathStates)
}

// UpdatePathState applies an update function to a path state
func (s *ExecutionState) UpdatePathState(pathID string, updateFn func(*PathState)) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if state, exists := s.pathStates[pathID]; exists {
		updateFn(state)
	}
}

// GetInputs creates a shallow copy of the inputs
func (s *ExecutionState) GetInputs() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.inputs)
}

// SetOutput sets an output value
func (s *ExecutionState) SetOutput(key string, value any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.outputs[key] = value
}

// GetOutput retrieves an output value
func (s *ExecutionState) GetOutputs() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.outputs)
}

// GetStartTime returns the execution start time
func (s *ExecutionState) GetStartTime() time.Time {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.startTime
}

// GetFailedPathIDs returns a list of path IDs that have failed
func (s *ExecutionState) GetFailedPathIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var failedIDs []string
	for pathID, pathState := range s.pathStates {
		if pathState.Status == ExecutionStatusFailed {
			failedIDs = append(failedIDs, pathID)
		}
	}
	return failedIDs
}

// GetWaitingPathIDs returns a list of path IDs that are waiting at joins
func (s *ExecutionState) GetWaitingPathIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var waitingIDs []string
	for pathID, pathState := range s.pathStates {
		if pathState.Status == ExecutionStatusWaiting {
			waitingIDs = append(waitingIDs, pathID)
		}
	}
	return waitingIDs
}

// AddPathToJoin adds a path to a join step
func (s *ExecutionState) AddPathToJoin(stepName, pathID string, config *JoinConfig, variables, stepOutputs map[string]any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Create join state if it doesn't exist
	if s.joinStates[stepName] == nil {
		s.joinStates[stepName] = &JoinState{
			StepName:      stepName,
			WaitingPathID: pathID,
			Config:        config,
			CreatedAt:     time.Now(),
		}
	} else {
		// Update the existing join state with the new path ID
		s.joinStates[stepName].WaitingPathID = pathID
	}
}

// IsJoinReady checks if a join step is ready to proceed
func (s *ExecutionState) IsJoinReady(stepName string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	joinState := s.joinStates[stepName]
	if joinState == nil {
		return false
	}

	config := joinState.Config

	// If specific paths are specified, check if all are completed (excluding the waiting path)
	if len(config.Paths) > 0 {
		for _, requiredPath := range config.Paths {
			// Skip the path that's currently waiting at the join
			if requiredPath == joinState.WaitingPathID {
				continue
			}
			pathState, exists := s.pathStates[requiredPath]
			if !exists || pathState.Status != ExecutionStatusCompleted {
				return false
			}
		}
		return true
	}

	// If count is specified, count completed paths (excluding the waiting path)
	if config.Count > 0 {
		completedCount := 0
		for pathID, pathState := range s.pathStates {
			if pathID != joinState.WaitingPathID && pathState.Status == ExecutionStatusCompleted {
				completedCount++
			}
		}
		return completedCount >= config.Count
	}

	// Default: wait for at least 2 paths to complete (minimum for a join)
	completedCount := 0
	for pathID, pathState := range s.pathStates {
		if pathID != joinState.WaitingPathID && pathState.Status == ExecutionStatusCompleted {
			completedCount++
		}
	}
	return completedCount >= 2
}

// GetJoinState returns a copy of the join state for a step
func (s *ExecutionState) GetJoinState(stepName string) *JoinState {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if joinState := s.joinStates[stepName]; joinState != nil {
		return &JoinState{
			StepName:      joinState.StepName,
			WaitingPathID: joinState.WaitingPathID,
			Config:        joinState.Config,
			CreatedAt:     joinState.CreatedAt,
		}
	}
	return nil
}

// RemoveJoinState removes a join state after it has been processed
func (s *ExecutionState) RemoveJoinState(stepName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.joinStates, stepName)
}

// GetAllJoinStates returns all join states
func (s *ExecutionState) GetAllJoinStates() map[string]*JoinState {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	result := make(map[string]*JoinState)
	for stepName, joinState := range s.joinStates {
		result[stepName] = &JoinState{
			StepName:      joinState.StepName,
			WaitingPathID: joinState.WaitingPathID,
			Config:        joinState.Config,
			CreatedAt:     joinState.CreatedAt,
		}
	}
	return result
}

// ToCheckpoint converts the execution state to a checkpoint
func (s *ExecutionState) ToCheckpoint() *Checkpoint {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return &Checkpoint{
		ID:           s.executionID + "-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		ExecutionID:  s.executionID,
		WorkflowName: s.workflowName,
		Status:       string(s.status),
		Inputs:       copyMap(s.inputs),
		Outputs:      copyMap(s.outputs),
		Variables:    map[string]any{}, // Variables are now per-path, so global variables are empty
		PathStates:   copyPathStates(s.pathStates),
		JoinStates:   copyJoinStates(s.joinStates),
		PathCounter:  s.pathCounter,
		StartTime:    s.startTime,
		EndTime:      s.endTime,
		CheckpointAt: time.Now(),
		Error:        s.err,
	}
}

// FromCheckpoint restores execution state from a checkpoint
func (s *ExecutionState) FromCheckpoint(checkpoint *Checkpoint) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.executionID = checkpoint.ExecutionID
	s.workflowName = checkpoint.WorkflowName
	s.status = ExecutionStatus(checkpoint.Status)
	s.inputs = copyMap(checkpoint.Inputs)
	s.outputs = copyMap(checkpoint.Outputs)
	s.pathStates = copyPathStates(checkpoint.PathStates)

	// Handle backward compatibility for checkpoints without JoinStates
	if checkpoint.JoinStates != nil {
		s.joinStates = copyJoinStates(checkpoint.JoinStates)
	} else {
		s.joinStates = make(map[string]*JoinState)
	}

	s.pathCounter = checkpoint.PathCounter
	s.startTime = checkpoint.StartTime
	s.endTime = checkpoint.EndTime
	s.err = checkpoint.Error
}

// copyMap creates a deep copy of a map
func copyMap(m map[string]any) map[string]any {
	copy := make(map[string]any, len(m))
	for k, v := range m {
		copy[k] = v
	}
	return copy
}

// copyPathStates creates a deep copy of a path states map
func copyPathStates(m map[string]*PathState) map[string]*PathState {
	copy := make(map[string]*PathState, len(m))
	for k, v := range m {
		copy[k] = v.Copy()
	}
	return copy
}

// copyJoinStates creates a deep copy of a join states map
func copyJoinStates(m map[string]*JoinState) map[string]*JoinState {
	copy := make(map[string]*JoinState, len(m))
	for k, v := range m {
		copy[k] = &JoinState{
			StepName:      v.StepName,
			WaitingPathID: v.WaitingPathID,
			Config:        v.Config,
			CreatedAt:     v.CreatedAt,
		}
	}
	return copy
}

// getNestedField retrieves a nested field from a map using dot notation
// e.g., "user.profile.name" -> map["user"]["profile"]["name"]
func getNestedField(data map[string]any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}

	// Handle simple case with no dots
	if !strings.Contains(path, ".") {
		value, exists := data[path]
		return value, exists
	}

	// Split path by dots and traverse
	parts := strings.Split(path, ".")
	current := data

	for i, part := range parts {
		if part == "" {
			return nil, false // Empty part in path
		}

		value, exists := current[part]
		if !exists {
			return nil, false
		}

		// If this is the last part, return the value
		if i == len(parts)-1 {
			return value, true
		}

		// Otherwise, expect the value to be a map and continue traversing
		if nextMap, ok := value.(map[string]any); ok {
			current = nextMap
		} else {
			return nil, false // Path leads to non-map value before the end
		}
	}

	return nil, false
}

// setNestedField sets a nested field in a map using dot notation
// e.g., "user.profile.name" -> map["user"]["profile"]["name"] = value
// Creates intermediate maps as needed
func setNestedField(data map[string]any, path string, value any) {
	if path == "" {
		return
	}

	// Handle simple case with no dots
	if !strings.Contains(path, ".") {
		data[path] = value
		return
	}

	// Split path by dots and traverse, creating maps as needed
	parts := strings.Split(path, ".")
	current := data

	for i, part := range parts {
		if part == "" {
			return // Empty part in path
		}

		// If this is the last part, set the value
		if i == len(parts)-1 {
			current[part] = value
			return
		}

		// Otherwise, ensure the next level exists as a map
		if existing, exists := current[part]; exists {
			if nextMap, ok := existing.(map[string]any); ok {
				current = nextMap
			} else {
				// Existing value is not a map, replace it with a map
				newMap := make(map[string]any)
				current[part] = newMap
				current = newMap
			}
		} else {
			// Create new map for this level
			newMap := make(map[string]any)
			current[part] = newMap
			current = newMap
		}
	}
}
