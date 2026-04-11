package workflow

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// BranchState tracks the state of an execution branch. This struct is designed to
// be fully JSON serializable.
type BranchState struct {
	ID           string          `json:"id"`
	Status       ExecutionStatus `json:"status"`
	CurrentStep  string          `json:"current_step"`
	StartTime    time.Time       `json:"start_time,omitzero"`
	EndTime      time.Time       `json:"end_time,omitzero"`
	ErrorMessage string          `json:"error_message,omitempty"`
	StepOutputs  map[string]any  `json:"step_outputs"`
	Variables    map[string]any  `json:"variables"`
	// Wait is populated when the branch is hard-suspended on a durable
	// wait (signal-wait or durable sleep). nil otherwise.
	Wait *WaitState `json:"wait,omitempty"`
	// PauseRequested marks a branch as paused by an explicit pause
	// trigger — either an external PauseBranch call or a declarative
	// Pause step. A branch with PauseRequested=true will re-park at its
	// next step boundary after construction; UnpauseBranch must clear
	// the flag before the branch can advance.
	PauseRequested bool `json:"pause_requested,omitempty"`
	// PauseReason is an optional human-readable note describing why
	// the branch was paused. Set by the PauseBranch caller or by a
	// PauseConfig.Reason on a declarative Pause step.
	PauseReason string `json:"pause_reason,omitempty"`
	// ActivityHistory is the persisted cache for the currently
	// executing activity. It survives wait-unwind replays so
	// activities can cache expensive work across suspensions via
	// [workflow.ActivityHistory] + [History.RecordOrReplay]. Cleared
	// when the step advances past the activity so there is no
	// cross-step leakage.
	ActivityHistory map[string]any `json:"activity_history,omitempty"`
	// ActivityHistoryStep records which step's activity owns the
	// current ActivityHistory map. executeActivity uses it to scope
	// history access to a single step: if the branch has raced ahead to
	// a new step before the orchestrator cleared the prior step's
	// history, the mismatch discards the stale entries so they do not
	// leak into the next activity.
	ActivityHistoryStep string `json:"activity_history_step,omitempty"`
}

// JoinState tracks a branch waiting at a join step
type JoinState struct {
	StepName      string      `json:"step_name"`
	WaitingPathID string      `json:"waiting_path_id"` // The single branch that's waiting
	Config        *JoinConfig `json:"config"`
	CreatedAt     time.Time   `json:"created_at"`
}

// Copy returns a shallow copy of the branch state.
func (p *BranchState) Copy() *BranchState {
	var wait *WaitState
	if p.Wait != nil {
		waitCopy := *p.Wait
		wait = &waitCopy
	}
	return &BranchState{
		ID:                  p.ID,
		Status:              p.Status,
		CurrentStep:         p.CurrentStep,
		StartTime:           p.StartTime,
		EndTime:             p.EndTime,
		ErrorMessage:        p.ErrorMessage,
		StepOutputs:         copyMap(p.StepOutputs),
		Variables:           copyMap(p.Variables),
		Wait:                wait,
		PauseRequested:      p.PauseRequested,
		PauseReason:         p.PauseReason,
		ActivityHistory:     copyMap(p.ActivityHistory),
		ActivityHistoryStep: p.ActivityHistoryStep,
	}
}

// executionState consolidates all execution state into a single structure. All
// data here is serializable for checkpointing.
type executionState struct {
	executionID  string
	workflowName string
	status       ExecutionStatus
	startTime    time.Time
	endTime      time.Time
	err          string
	inputs       map[string]any
	outputs      map[string]any
	pathCounter  int
	branchStates   map[string]*BranchState
	joinStates   map[string]*JoinState // stepName -> JoinState
	mutex        sync.RWMutex
}

// newExecutionState creates a new unified execution state
func newExecutionState(executionID, workflowName string, inputs map[string]any) *executionState {
	return &executionState{
		executionID:  executionID,
		workflowName: workflowName,
		status:       ExecutionStatusPending,
		inputs:       copyMap(inputs),
		outputs:      map[string]any{},
		branchStates:   map[string]*BranchState{},
		joinStates:   map[string]*JoinState{},
	}
}

// ID returns the execution ID
func (s *executionState) ID() string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.executionID
}

// SetID sets the execution ID
func (s *executionState) SetID(id string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.executionID = id
}

// GetStatus returns the current execution status
func (s *executionState) GetStatus() ExecutionStatus {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.status
}

// SetStatus updates the execution status
func (s *executionState) SetStatus(status ExecutionStatus) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.status = status
	if status != ExecutionStatusFailed {
		s.err = ""
	}
}

// SetError sets the execution error
func (s *executionState) SetError(err error) {
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
func (s *executionState) GetError() error {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	if s.err == "" {
		return nil
	}
	return errors.New(s.err)
}

// SetTiming updates the execution timing
func (s *executionState) SetTiming(startTime, endTime time.Time) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.startTime = startTime
	s.endTime = endTime
}

func (s *executionState) SetFinished(status ExecutionStatus, endTime time.Time, err error) {
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

// NextBranchID generates a new unique branch ID
func (s *executionState) NextBranchID(baseID string) string {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.pathCounter++
	return baseID + "-" + fmt.Sprintf("%d", s.pathCounter)
}

// GenerateBranchID creates a branch ID, using branchName if provided, otherwise generating a sequential ID
func (s *executionState) GenerateBranchID(parentID, branchName string) (string, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var branchID string
	if branchName != "" {
		// Use the provided branch name as the ID
		branchID = branchName
		// Check for duplicate branch names
		if _, exists := s.branchStates[branchID]; exists {
			return "", fmt.Errorf("duplicate branch name: %q", branchName)
		}
	} else {
		// Default to generating sequential IDs
		s.pathCounter++
		branchID = parentID + "-" + fmt.Sprintf("%d", s.pathCounter)
	}
	return branchID, nil
}

// SetBranchState sets or updates a branch state
func (s *executionState) SetBranchState(branchID string, state *BranchState) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.branchStates[branchID] = state.Copy()
}

// GetBranchState retrieves a branch state
func (s *executionState) GetBranchStates() map[string]*BranchState {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyBranchStates(s.branchStates)
}

// UpdateBranchState applies an update function to a branch state
func (s *executionState) UpdateBranchState(branchID string, updateFn func(*BranchState)) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if state, exists := s.branchStates[branchID]; exists {
		updateFn(state)
	}
}

// GetInputs creates a shallow copy of the inputs
func (s *executionState) GetInputs() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.inputs)
}

// SetOutput sets an output value
func (s *executionState) SetOutput(key string, value any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.outputs[key] = value
}

// GetOutput retrieves an output value
func (s *executionState) GetOutputs() map[string]any {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return copyMap(s.outputs)
}

// GetStartTime returns the execution start time
func (s *executionState) GetStartTime() time.Time {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.startTime
}

// GetEndTime returns the execution end time
func (s *executionState) GetEndTime() time.Time {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.endTime
}

// GetFailedBranchIDs returns a list of branch IDs that have failed
func (s *executionState) GetFailedBranchIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var failedIDs []string
	for branchID, branchState := range s.branchStates {
		if branchState.Status == ExecutionStatusFailed {
			failedIDs = append(failedIDs, branchID)
		}
	}
	return failedIDs
}

// GetWaitingBranchIDs returns a list of branch IDs that are waiting at joins.
// This reflects Status == Waiting, which the engine uses exclusively for
// join-in-progress. Hard-suspended branches have Status == Suspended and are
// reported by GetSuspendedBranchIDs.
func (s *executionState) GetWaitingBranchIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var waitingIDs []string
	for branchID, branchState := range s.branchStates {
		if branchState.Status == ExecutionStatusWaiting {
			waitingIDs = append(waitingIDs, branchID)
		}
	}
	return waitingIDs
}

// GetSuspendedBranchIDs returns a list of branch IDs that are hard-suspended
// on a durable wait (signal-wait or sleep). These branches have exited their
// goroutine and only live in the checkpoint.
func (s *executionState) GetSuspendedBranchIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var suspendedIDs []string
	for branchID, branchState := range s.branchStates {
		if branchState.Status == ExecutionStatusSuspended {
			suspendedIDs = append(suspendedIDs, branchID)
		}
	}
	return suspendedIDs
}

// GetPausedBranchIDs returns a list of branch IDs that are currently paused.
// Paused branches have exited their goroutine and only live in the checkpoint;
// an external UnpauseBranch call is required before the branch can advance.
func (s *executionState) GetPausedBranchIDs() []string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var pausedIDs []string
	for branchID, branchState := range s.branchStates {
		if branchState.Status == ExecutionStatusPaused {
			pausedIDs = append(pausedIDs, branchID)
		}
	}
	return pausedIDs
}

// AddBranchToJoin adds a branch to a join step
func (s *executionState) AddBranchToJoin(stepName, branchID string, config *JoinConfig, variables, stepOutputs map[string]any) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Create join state if it doesn't exist
	if s.joinStates[stepName] == nil {
		s.joinStates[stepName] = &JoinState{
			StepName:      stepName,
			WaitingPathID: branchID,
			Config:        config,
			CreatedAt:     time.Now(),
		}
	} else {
		// Update the existing join state with the new branch ID
		s.joinStates[stepName].WaitingPathID = branchID
	}
}

// IsJoinReady checks if a join step is ready to proceed
func (s *executionState) IsJoinReady(stepName string) bool {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	joinState := s.joinStates[stepName]
	if joinState == nil {
		return false
	}

	config := joinState.Config

	// If specific branches are specified, check if all are completed (excluding the waiting branch)
	if len(config.Branches) > 0 {
		for _, requiredBranch := range config.Branches {
			// Skip the branch that's currently waiting at the join
			if requiredBranch == joinState.WaitingPathID {
				continue
			}
			branchState, exists := s.branchStates[requiredBranch]
			if !exists || branchState.Status != ExecutionStatusCompleted {
				return false
			}
		}
		return true
	}

	// If count is specified, count completed branches (excluding the waiting branch)
	if config.Count > 0 {
		completedCount := 0
		for branchID, branchState := range s.branchStates {
			if branchID != joinState.WaitingPathID && branchState.Status == ExecutionStatusCompleted {
				completedCount++
			}
		}
		return completedCount >= config.Count
	}

	// Default: wait for at least 2 branches to complete (minimum for a join)
	completedCount := 0
	for branchID, branchState := range s.branchStates {
		if branchID != joinState.WaitingPathID && branchState.Status == ExecutionStatusCompleted {
			completedCount++
		}
	}
	return completedCount >= 2
}

// GetJoinState returns a copy of the join state for a step
func (s *executionState) GetJoinState(stepName string) *JoinState {
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
func (s *executionState) RemoveJoinState(stepName string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	delete(s.joinStates, stepName)
}

// GetAllJoinStates returns all join states
func (s *executionState) GetAllJoinStates() map[string]*JoinState {
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
func (s *executionState) ToCheckpoint() *Checkpoint {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return &Checkpoint{
		ID:           s.executionID + "-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		ExecutionID:  s.executionID,
		WorkflowName: s.workflowName,
		Status:       string(s.status),
		Inputs:       copyMap(s.inputs),
		Outputs:      copyMap(s.outputs),
		Variables:    map[string]any{}, // Variables are now per-branch, so global variables are empty
		BranchStates:   copyBranchStates(s.branchStates),
		JoinStates:   copyJoinStates(s.joinStates),
		BranchCounter:  s.pathCounter,
		StartTime:    s.startTime,
		EndTime:      s.endTime,
		CheckpointAt: time.Now(),
		Error:        s.err,
	}
}

// FromCheckpoint restores execution state from a checkpoint
func (s *executionState) FromCheckpoint(checkpoint *Checkpoint) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.executionID = checkpoint.ExecutionID
	s.workflowName = checkpoint.WorkflowName
	s.status = ExecutionStatus(checkpoint.Status)
	s.inputs = copyMap(checkpoint.Inputs)
	s.outputs = copyMap(checkpoint.Outputs)
	s.branchStates = copyBranchStates(checkpoint.BranchStates)

	// Handle backward compatibility for checkpoints without JoinStates
	if checkpoint.JoinStates != nil {
		s.joinStates = copyJoinStates(checkpoint.JoinStates)
	} else {
		s.joinStates = make(map[string]*JoinState)
	}

	s.pathCounter = checkpoint.BranchCounter
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

// copyBranchStates creates a deep copy of a branch states map
func copyBranchStates(m map[string]*BranchState) map[string]*BranchState {
	copy := make(map[string]*BranchState, len(m))
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
func getNestedField(data map[string]any, branch string) (any, bool) {
	if branch == "" {
		return nil, false
	}

	// Handle simple case with no dots
	if !strings.Contains(branch, ".") {
		value, exists := data[branch]
		return value, exists
	}

	// Split branch by dots and traverse
	parts := strings.Split(branch, ".")
	current := data

	for i, part := range parts {
		if part == "" {
			return nil, false // Empty part in branch
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
func setNestedField(data map[string]any, branch string, value any) {
	if branch == "" {
		return
	}

	// Handle simple case with no dots
	if !strings.Contains(branch, ".") {
		data[branch] = value
		return
	}

	// Split branch by dots and traverse, creating maps as needed
	parts := strings.Split(branch, ".")
	current := data

	for i, part := range parts {
		if part == "" {
			return // Empty part in branch
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
