package workflow

import (
	"sort"
	"sync"
)

// PathLocalState provides activities with access to workflow input variables
// and to the execution path's copy of state variables. It is safe for
// concurrent use.
type PathLocalState struct {
	mu        sync.RWMutex
	inputs    map[string]any
	variables map[string]any
}

func NewPathLocalState(inputs, variables map[string]any) *PathLocalState {
	return &PathLocalState{
		inputs:    copyMap(inputs),
		variables: copyMap(variables),
	}
}

func (s *PathLocalState) ListInputs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for key := range s.inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *PathLocalState) GetInput(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.inputs[key]
	return value, exists
}

func (s *PathLocalState) SetVariable(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.variables[key] = value
}

func (s *PathLocalState) DeleteVariable(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.variables, key)
}

func (s *PathLocalState) ListVariables() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for key := range s.variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *PathLocalState) GetVariable(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.variables[key]
	return value, exists
}
