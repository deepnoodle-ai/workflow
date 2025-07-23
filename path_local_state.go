package workflow

import (
	"sort"
)

// PathLocalState provides activities with access to workflow input variables
// and to the execution path's copy of state variables.
type PathLocalState struct {
	inputs    map[string]any
	variables map[string]any
}

func NewPathLocalState(inputs, variables map[string]any) *PathLocalState {
	return &PathLocalState{
		inputs:    copyMapAny(inputs),
		variables: copyMapAny(variables),
	}
}

func (s *PathLocalState) ListInputs() []string {
	var keys []string
	for key := range s.inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *PathLocalState) GetInput(key string) (any, bool) {
	value, exists := s.inputs[key]
	return value, exists
}

func (s *PathLocalState) SetVariable(key string, value any) {
	s.variables[key] = value
}

func (s *PathLocalState) DeleteVariable(key string) {
	delete(s.variables, key)
}

func (s *PathLocalState) ListVariables() []string {
	var keys []string
	for key := range s.variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *PathLocalState) GetVariable(key string) (any, bool) {
	value, exists := s.variables[key]
	return value, exists
}
