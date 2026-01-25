package domain

// PathLocalState provides activities with access to workflow state.
type PathLocalState struct {
	inputs    map[string]any
	variables map[string]any
}

// NewPathLocalState creates a new path local state.
func NewPathLocalState(inputs, variables map[string]any) *PathLocalState {
	return &PathLocalState{
		inputs:    copyMapAny(inputs),
		variables: copyMapAny(variables),
	}
}

// ListInputs returns all input keys.
func (s *PathLocalState) ListInputs() []string {
	keys := make([]string, 0, len(s.inputs))
	for key := range s.inputs {
		keys = append(keys, key)
	}
	return keys
}

// GetInput returns an input value.
func (s *PathLocalState) GetInput(key string) (any, bool) {
	value, exists := s.inputs[key]
	return value, exists
}

// SetVariable sets a variable value.
func (s *PathLocalState) SetVariable(key string, value any) {
	s.variables[key] = value
}

// DeleteVariable deletes a variable.
func (s *PathLocalState) DeleteVariable(key string) {
	delete(s.variables, key)
}

// ListVariables returns all variable keys.
func (s *PathLocalState) ListVariables() []string {
	keys := make([]string, 0, len(s.variables))
	for key := range s.variables {
		keys = append(keys, key)
	}
	return keys
}

// GetVariable returns a variable value.
func (s *PathLocalState) GetVariable(key string) (any, bool) {
	value, exists := s.variables[key]
	return value, exists
}

// Variables returns a copy of all variables.
func (s *PathLocalState) Variables() map[string]any {
	return copyMapAny(s.variables)
}

// Inputs returns a copy of all inputs.
func (s *PathLocalState) Inputs() map[string]any {
	return copyMapAny(s.inputs)
}
