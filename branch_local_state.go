package workflow

import (
	"sort"
	"sync"
)

// BranchLocalState provides activities with access to workflow input variables
// and to the execution branch's copy of state variables. It is safe for
// concurrent use.
type BranchLocalState struct {
	mu        sync.RWMutex
	inputs    map[string]any
	variables map[string]any
}

func NewBranchLocalState(inputs, variables map[string]any) *BranchLocalState {
	return &BranchLocalState{
		inputs:    copyMap(inputs),
		variables: copyMap(variables),
	}
}

func (s *BranchLocalState) ListInputs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for key := range s.inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *BranchLocalState) GetInput(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.inputs[key]
	return value, exists
}

func (s *BranchLocalState) SetVariable(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.variables[key] = value
}

func (s *BranchLocalState) DeleteVariable(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.variables, key)
}

func (s *BranchLocalState) ListVariables() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for key := range s.variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *BranchLocalState) GetVariable(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.variables[key]
	return value, exists
}
