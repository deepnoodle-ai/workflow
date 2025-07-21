package workflow

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/deepnoodle-ai/workflow/state"
)

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
	variables    map[string]any
	pathCounter  int
	pathStates   map[string]*PathState
	mutex        sync.RWMutex
}

// newExecutionState creates a new unified execution state
func newExecutionState(
	executionID, workflowName string,
	inputs map[string]any,
	initialState map[string]any,
) *ExecutionState {
	variables := make(map[string]any)
	for k, v := range initialState {
		variables[k] = v
	}
	return &ExecutionState{
		executionID:  executionID,
		workflowName: workflowName,
		status:       ExecutionStatusPending,
		inputs:       copyMap(inputs),
		outputs:      make(map[string]any),
		variables:    variables,
		pathStates:   make(map[string]*PathState),
		pathCounter:  0,
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

// SetVariable sets a workflow variable
func (s *ExecutionState) SetVariable(key string, value any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.variables[key] = value
}

// GetVariable retrieves a workflow variable
func (s *ExecutionState) GetVariable(key string) (any, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	value, exists := s.variables[key]
	return value, exists
}

// DeleteVariable removes a workflow variable
func (s *ExecutionState) DeleteVariable(key string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.variables, key)
}

// GetVariableNames returns all variable names
func (s *ExecutionState) GetVariableNames() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	names := make([]string, 0, len(s.variables))
	for name := range s.variables {
		names = append(names, name)
	}
	return names
}

// NextPathID generates a new unique path ID
func (s *ExecutionState) NextPathID(baseID string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.pathCounter++
	if baseID == "" {
		return "main"
	}
	return baseID + "-" + fmt.Sprintf("%d", s.pathCounter)
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

	return copyPathStatesMap(s.pathStates)
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

// GetVariables creates a shallow copy of the variables
func (s *ExecutionState) GetVariables() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.variables)
}

// ApplyPatches applies a list of patches to the variables
func (s *ExecutionState) ApplyPatches(patches []state.Patch) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, patch := range patches {
		if patch.Delete {
			delete(s.variables, patch.Variable)
		} else {
			s.variables[patch.Variable] = patch.Value
		}
	}
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
		if pathState.Status == PathStatusFailed {
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
		Variables:    copyMap(s.variables),
		PathStates:   copyPathStatesMap(s.pathStates),
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
	s.variables = copyMap(checkpoint.Variables)
	s.pathStates = copyPathStatesMap(checkpoint.PathStates)
	s.pathCounter = checkpoint.PathCounter
	s.startTime = checkpoint.StartTime
	s.endTime = checkpoint.EndTime
	s.err = checkpoint.Error
}

// Copy returns a copy of the execution state
func (s *ExecutionState) Copy() *ExecutionState {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return &ExecutionState{
		executionID:  s.executionID,
		workflowName: s.workflowName,
		status:       s.status,
		startTime:    s.startTime,
		endTime:      s.endTime,
		err:          s.err,
		inputs:       copyMap(s.inputs),
		outputs:      copyMap(s.outputs),
		variables:    copyMap(s.variables),
		pathCounter:  s.pathCounter,
		pathStates:   copyPathStatesMap(s.pathStates),
	}
}

// GetScriptGlobals returns a safe copy of state for script contexts
func (s *ExecutionState) GetScriptGlobals() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return map[string]any{
		"inputs": copyMap(s.inputs),
		"state":  copyMap(s.variables),
	}
}

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
