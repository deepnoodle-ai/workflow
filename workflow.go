package workflow

import (
	"fmt"
	"sort"
)

// Input defines an expected input parameter
type Input struct {
	Name        string      `json:"name"`
	Type        string      `json:"type,omitempty"`
	Description string      `json:"description,omitempty"`
	Required    bool        `json:"required,omitempty"`
	Default     interface{} `json:"default,omitempty"`
}

// Output defines an expected output parameter
type Output struct {
	Name        string      `json:"name"`
	Type        string      `json:"type,omitempty"`
	Description string      `json:"description,omitempty"`
	Format      string      `json:"format,omitempty"`
	Default     interface{} `json:"default,omitempty"`
	Document    string      `json:"document,omitempty"`
}

type Trigger struct {
	Name   string
	Type   string
	Config map[string]interface{}
}

// Workflow defines a repeatable process as a graph of tasks to be executed.
type Workflow struct {
	name        string
	description string
	path        string
	inputs      []*Input
	output      *Output
	steps       []*Step
	stepsByName map[string]*Step
	start       *Step
	triggers    []*Trigger
}

// Options are used to configure a Workflow.
type Options struct {
	Name        string
	Description string
	Path        string
	Inputs      []*Input
	Output      *Output
	Steps       []*Step
	Triggers    []*Trigger
}

// New returns a new Workflow configured with the given options.
func New(opts Options) (*Workflow, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("workflow name required")
	}
	if len(opts.Steps) == 0 {
		return nil, fmt.Errorf("steps required")
	}

	// Build stepsByName map
	stepsByName := make(map[string]*Step, len(opts.Steps))
	for _, step := range opts.Steps {
		if step.Name == "" {
			return nil, fmt.Errorf("step name required")
		}
		stepsByName[step.Name] = step
	}

	// Validate the workflow structure
	if err := validateWorkflowSteps(stepsByName); err != nil {
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	w := &Workflow{
		name:        opts.Name,
		description: opts.Description,
		path:        opts.Path,
		inputs:      opts.Inputs,
		output:      opts.Output,
		steps:       opts.Steps,
		stepsByName: stepsByName,
		start:       opts.Steps[0],
		triggers:    opts.Triggers,
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Workflow) Path() string {
	return w.path
}

func (w *Workflow) Name() string {
	return w.name
}

func (w *Workflow) Description() string {
	return w.description
}

func (w *Workflow) Inputs() []*Input {
	return w.inputs
}

func (w *Workflow) Output() *Output {
	return w.output
}

func (w *Workflow) Steps() []*Step {
	return w.steps
}

func (w *Workflow) Start() *Step {
	return w.start
}

// Get returns a step by name
func (w *Workflow) Get(name string) (*Step, bool) {
	step, ok := w.stepsByName[name]
	return step, ok
}

// Names returns the names of all steps in the workflow
func (w *Workflow) Names() []string {
	names := make([]string, 0, len(w.stepsByName))
	for name := range w.stepsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (w *Workflow) Triggers() []*Trigger {
	return w.triggers
}

// Validate checks if the workflow is properly configured
func (w *Workflow) Validate() error {
	if w.name == "" {
		return fmt.Errorf("workflow name required")
	}
	if w.start == nil {
		return fmt.Errorf("graph start task required")
	}
	return nil
}

// validateWorkflowSteps validates the workflow step structure
func validateWorkflowSteps(stepsByName map[string]*Step) error {
	if len(stepsByName) == 0 {
		return fmt.Errorf("workflow must have at least one step")
	}
	for _, step := range stepsByName {
		if step.Name == "" {
			return fmt.Errorf("step name cannot be empty")
		}
		for _, edge := range step.Next {
			if _, ok := stepsByName[edge.Step]; !ok {
				return fmt.Errorf("edge to step %q not found", edge.Step)
			}
		}
	}
	return nil
}
