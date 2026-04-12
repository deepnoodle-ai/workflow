package workflow

import (
	"errors"
	"fmt"
	"sort"
)

// ErrDuplicateActivity is returned when Register is called with an
// activity name that has already been registered.
var ErrDuplicateActivity = errors.New("workflow: duplicate activity registration")

// ActivityRegistry owns the set of activities an Execution can call
// by name. It is opaque — consumers construct one via
// NewActivityRegistry and add activities through Register or
// MustRegister. The registry is read-only once passed to NewExecution.
type ActivityRegistry struct {
	activities map[string]Activity
}

// NewActivityRegistry returns an empty registry.
func NewActivityRegistry() *ActivityRegistry {
	return &ActivityRegistry{activities: map[string]Activity{}}
}

// Register adds an activity to the registry. Returns ErrDuplicateActivity
// if an activity with the same name is already registered.
func (r *ActivityRegistry) Register(a Activity) error {
	if r == nil {
		return fmt.Errorf("workflow: nil activity registry")
	}
	if r.activities == nil {
		r.activities = make(map[string]Activity)
	}
	if a == nil {
		return fmt.Errorf("workflow: nil activity")
	}
	name := a.Name()
	if name == "" {
		return fmt.Errorf("workflow: activity has empty name")
	}
	if _, exists := r.activities[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateActivity, name)
	}
	r.activities[name] = a
	return nil
}

// MustRegister panics on registration failure. Returns the registry so
// calls can chain in init() or a builder expression.
func (r *ActivityRegistry) MustRegister(a Activity) *ActivityRegistry {
	if err := r.Register(a); err != nil {
		panic(err)
	}
	return r
}

// Get returns the activity registered under name, if any.
func (r *ActivityRegistry) Get(name string) (Activity, bool) {
	a, ok := r.activities[name]
	return a, ok
}

// Names returns the registered activity names in sorted order.
func (r *ActivityRegistry) Names() []string {
	names := make([]string, 0, len(r.activities))
	for name := range r.activities {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// internal accessor for the engine.
func (r *ActivityRegistry) asMap() map[string]Activity {
	if r == nil {
		return nil
	}
	return r.activities
}
