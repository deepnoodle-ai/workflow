package workflow

import (
	"errors"
	"fmt"
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
