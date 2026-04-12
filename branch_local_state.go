package workflow

import (
	"sort"
	"sync"
)

// BranchLocalState provides activities with access to workflow input
// variables and to the execution branch's copy of state variables. It
// is safe for concurrent use.
//
// BranchLocalState is embedded in executionContext so its methods
// bubble up as the Context.Get/Set/Delete/Keys methods activity code
// uses directly.
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

// Set writes a branch-local variable.
func (s *BranchLocalState) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.variables[key] = value
}

// Delete removes a branch-local variable.
func (s *BranchLocalState) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.variables, key)
}

// Keys returns the names of all branch-local variables in sorted
// order.
func (s *BranchLocalState) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.variables))
	for key := range s.variables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Get returns a branch-local variable and whether it was present.
func (s *BranchLocalState) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.variables[key]
	return value, exists
}

// inputsSnapshot returns a copy of the input map for use by
// executionContext.Inputs. The snapshot is the stable view exposed
// to activity code — once taken, later writes do not affect it.
func (s *BranchLocalState) inputsSnapshot() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]any, len(s.inputs))
	for k, v := range s.inputs {
		out[k] = v
	}
	return out
}
