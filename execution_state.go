package workflow

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ExecutionState consolidates all execution state into a single structure. All
// data here is serializable for checkpointing.
type ExecutionState struct {
	// Metadata
	ExecutionID  string          `json:"execution_id"`
	WorkflowName string          `json:"workflow_name"`
	Status       ExecutionStatus `json:"status"`
	StartTime    time.Time       `json:"start_time,omitzero"`
	EndTime      time.Time       `json:"end_time,omitzero"`
	Error        string          `json:"error,omitempty"`

	// Variables
	Inputs    map[string]any `json:"inputs,omitempty"`
	Outputs   map[string]any `json:"outputs,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`

	// Path execution state
	PathCounter int                   `json:"path_counter"`
	PathStates  map[string]*PathState `json:"path_states"`

	// Thread-safe access
	mutex sync.RWMutex
}

// NewExecutionState creates a new unified execution state
func NewExecutionState(executionID, workflowName string, inputs map[string]any) *ExecutionState {
	return &ExecutionState{
		ExecutionID:  executionID,
		WorkflowName: workflowName,
		Status:       ExecutionStatusPending,
		Inputs:       copyMap(inputs),
		Outputs:      make(map[string]any),
		Variables:    make(map[string]any),
		PathStates:   make(map[string]*PathState),
		PathCounter:  0,
	}
}

// === Core Execution Methods ===

// ID returns the execution ID
func (s *ExecutionState) ID() string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.ExecutionID
}

// SetID sets the execution ID
func (s *ExecutionState) SetID(id string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.ExecutionID = id
}

// GetStatus returns the current execution status
func (s *ExecutionState) GetStatus() ExecutionStatus {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.Status
}

// SetStatus updates the execution status
func (s *ExecutionState) SetStatus(status ExecutionStatus) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Status = status
	if status != ExecutionStatusFailed {
		s.Error = ""
	}
}

// SetError sets the execution error
func (s *ExecutionState) SetError(err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if err != nil {
		s.Error = err.Error()
		s.Status = ExecutionStatusFailed
	} else {
		s.Error = ""
	}
}

// GetError returns the current execution error
func (s *ExecutionState) GetError() error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if s.Error == "" {
		return nil
	}
	return errors.New(s.Error)
}

// SetTiming updates the execution timing
func (s *ExecutionState) SetTiming(startTime, endTime time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.StartTime = startTime
	s.EndTime = endTime
}

func (s *ExecutionState) SetFinished(status ExecutionStatus, endTime time.Time, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Status = status
	s.EndTime = endTime
	if err != nil {
		s.Error = err.Error()
	} else {
		s.Error = ""
	}
}

// === Variables ===

// SetVariable sets a workflow variable
func (s *ExecutionState) SetVariable(key string, value any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Variables[key] = value
}

// GetVariable retrieves a workflow variable
func (s *ExecutionState) GetVariable(key string) (any, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	value, exists := s.Variables[key]
	return value, exists
}

// DeleteVariable removes a workflow variable
func (s *ExecutionState) DeleteVariable(key string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.Variables, key)
}

// GetVariableNames returns all variable names
func (s *ExecutionState) GetVariableNames() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	names := make([]string, 0, len(s.Variables))
	for name := range s.Variables {
		names = append(names, name)
	}
	return names
}

// === Path Management ===

// NextPathID generates a new unique path ID
func (s *ExecutionState) NextPathID(baseID string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.PathCounter++
	if baseID == "" {
		return "main"
	}
	return baseID + "-" + fmt.Sprintf("%d", s.PathCounter)
}

// SetPathState sets or updates a path state
func (s *ExecutionState) SetPathState(pathID string, state *PathState) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.PathStates[pathID] = state.Copy()
}

// GetPathState retrieves a path state
func (s *ExecutionState) GetPathState(pathID string) (*PathState, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	state, exists := s.PathStates[pathID]
	if !exists {
		return nil, false
	}
	return state.Copy(), true
}

// UpdatePathState applies an update function to a path state
func (s *ExecutionState) UpdatePathState(pathID string, updateFn func(*PathState)) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if state, exists := s.PathStates[pathID]; exists {
		updateFn(state)
	}
}

// GetActivePathIDs returns IDs of paths that are pending or actively running
func (s *ExecutionState) GetActivePathIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var activeIDs []string
	for id, state := range s.PathStates {
		if state.Status == PathStatusPending || state.Status == PathStatusRunning {
			activeIDs = append(activeIDs, id)
		}
	}
	return activeIDs
}

// GetFailedPathIDs returns IDs of paths that have failed
func (s *ExecutionState) GetFailedPathIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var failedIDs []string
	for id, state := range s.PathStates {
		if state.Status == PathStatusFailed {
			failedIDs = append(failedIDs, id)
		}
	}
	return failedIDs
}

// === Inputs and Outputs ===

// GetInput retrieves an input value
func (s *ExecutionState) GetInput(key string) (any, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	value, exists := s.Inputs[key]
	return value, exists
}

// GetInputs creates a deep copy of the inputs
func (s *ExecutionState) GetInputs() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.Inputs)
}

// GetVariables creates a deep copy of the variables
func (s *ExecutionState) GetVariables() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.Variables)
}

// SetOutput sets an output value
func (s *ExecutionState) SetOutput(key string, value any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.Outputs[key] = value
}

// GetOutput retrieves an output value
func (s *ExecutionState) GetOutput(key string) (any, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	value, exists := s.Outputs[key]
	return value, exists
}

// === Checkpointing ===

// ToCheckpoint converts the execution state to a checkpoint
func (s *ExecutionState) ToCheckpoint() *Checkpoint {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return &Checkpoint{
		ID:           s.ExecutionID + "-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		ExecutionID:  s.ExecutionID,
		WorkflowName: s.WorkflowName,
		Status:       string(s.Status),
		Inputs:       copyMap(s.Inputs),
		Outputs:      copyMap(s.Outputs),
		Variables:    copyMap(s.Variables),
		PathStates:   copyPathStatesMap(s.PathStates),
		PathCounter:  s.PathCounter,
		StartTime:    s.StartTime,
		EndTime:      s.EndTime,
		CheckpointAt: time.Now(),
		Error:        s.Error,
	}
}

// FromCheckpoint restores execution state from a checkpoint
func (s *ExecutionState) FromCheckpoint(checkpoint *Checkpoint) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.ExecutionID = checkpoint.ExecutionID
	s.WorkflowName = checkpoint.WorkflowName
	s.Status = ExecutionStatus(checkpoint.Status)
	s.Inputs = copyMap(checkpoint.Inputs)
	s.Outputs = copyMap(checkpoint.Outputs)
	s.Variables = copyMap(checkpoint.Variables)
	s.PathStates = copyPathStatesMap(checkpoint.PathStates)
	s.PathCounter = checkpoint.PathCounter
	s.StartTime = checkpoint.StartTime
	s.EndTime = checkpoint.EndTime
	s.Error = checkpoint.Error
}

// Copy returns a deep copy of the execution state
func (s *ExecutionState) Copy() *ExecutionState {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return &ExecutionState{
		ExecutionID:  s.ExecutionID,
		WorkflowName: s.WorkflowName,
		Status:       s.Status,
		StartTime:    s.StartTime,
		EndTime:      s.EndTime,
		Error:        s.Error,
		Inputs:       copyMap(s.Inputs),
		Outputs:      copyMap(s.Outputs),
		Variables:    copyMap(s.Variables),
		PathCounter:  s.PathCounter,
		PathStates:   copyPathStatesMap(s.PathStates),
	}
}

// === Risor Integration ===

// GetScriptGlobals returns a safe copy of state for script contexts
func (s *ExecutionState) GetScriptGlobals() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return map[string]any{
		"inputs": copyMap(s.Inputs),
		"state":  copyMap(s.Variables),
	}
}

// === Helper Types and Functions ===

// copyMap creates a deep copy of a map
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	copy := make(map[string]any, len(m))
	for k, v := range m {
		copy[k] = v
	}
	return copy
}

// copyPathStatesMap creates a deep copy of a path states map
func copyPathStatesMap(m map[string]*PathState) map[string]*PathState {
	if m == nil {
		return nil
	}
	copy := make(map[string]*PathState, len(m))
	for k, v := range m {
		copy[k] = v.Copy()
	}
	return copy
}
