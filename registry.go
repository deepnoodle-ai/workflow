package workflow

import (
	"fmt"
	"sync"
)

// Registry is a central registry for workflows and activities.
// It provides a single place to register all workflow definitions and activities,
// which can then be used with either local or remote execution.
type Registry struct {
	mu         sync.RWMutex
	workflows  map[string]*Workflow
	activities map[string]Activity
}

// NewRegistry creates a new empty registry.
func NewRegistry() *Registry {
	return &Registry{
		workflows:  make(map[string]*Workflow),
		activities: make(map[string]Activity),
	}
}

// RegisterWorkflow adds a workflow to the registry.
// Returns an error if a workflow with the same name already exists.
func (r *Registry) RegisterWorkflow(wf *Workflow) error {
	if wf == nil {
		return fmt.Errorf("workflow cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := wf.Name()
	if _, exists := r.workflows[name]; exists {
		return fmt.Errorf("workflow %q already registered", name)
	}

	r.workflows[name] = wf
	return nil
}

// RegisterActivity adds an activity to the registry.
// Returns an error if an activity with the same name already exists.
func (r *Registry) RegisterActivity(act Activity) error {
	if act == nil {
		return fmt.Errorf("activity cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	name := act.Name()
	if _, exists := r.activities[name]; exists {
		return fmt.Errorf("activity %q already registered", name)
	}

	r.activities[name] = act
	return nil
}

// MustRegisterWorkflow adds a workflow to the registry.
// Panics if the workflow is nil or already registered.
func (r *Registry) MustRegisterWorkflow(wf *Workflow) {
	if err := r.RegisterWorkflow(wf); err != nil {
		panic(err)
	}
}

// MustRegisterActivity adds an activity to the registry.
// Panics if the activity is nil or already registered.
func (r *Registry) MustRegisterActivity(act Activity) {
	if err := r.RegisterActivity(act); err != nil {
		panic(err)
	}
}

// GetWorkflow returns a workflow by name.
func (r *Registry) GetWorkflow(name string) (*Workflow, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	wf, ok := r.workflows[name]
	return wf, ok
}

// GetActivity returns an activity by name.
func (r *Registry) GetActivity(name string) (Activity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	act, ok := r.activities[name]
	return act, ok
}

// Workflows returns all registered workflows.
func (r *Registry) Workflows() []*Workflow {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Workflow, 0, len(r.workflows))
	for _, wf := range r.workflows {
		result = append(result, wf)
	}
	return result
}

// Activities returns all registered activities.
func (r *Registry) Activities() []Activity {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Activity, 0, len(r.activities))
	for _, act := range r.activities {
		result = append(result, act)
	}
	return result
}

// WorkflowNames returns the names of all registered workflows.
func (r *Registry) WorkflowNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.workflows))
	for name := range r.workflows {
		result = append(result, name)
	}
	return result
}

// ActivityNames returns the names of all registered activities.
func (r *Registry) ActivityNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, 0, len(r.activities))
	for name := range r.activities {
		result = append(result, name)
	}
	return result
}

// Clear removes all registered workflows and activities.
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workflows = make(map[string]*Workflow)
	r.activities = make(map[string]Activity)
}

// Register implements WorkflowRegistry by delegating to RegisterWorkflow.
func (r *Registry) Register(wf *Workflow) error {
	return r.RegisterWorkflow(wf)
}

// Get implements WorkflowRegistry by delegating to GetWorkflow.
func (r *Registry) Get(name string) (*Workflow, bool) {
	return r.GetWorkflow(name)
}

// List implements WorkflowRegistry by delegating to WorkflowNames.
func (r *Registry) List() []string {
	return r.WorkflowNames()
}

// Verify that Registry implements WorkflowRegistry
var _ WorkflowRegistry = (*Registry)(nil)
