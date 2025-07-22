package workflow

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Input defines a workflow input parameter
type Input struct {
	Name        string      `json:"name" yaml:"name"`
	Type        string      `json:"type" yaml:"type"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Required    bool        `json:"required,omitempty" yaml:"required,omitempty"`
	Default     interface{} `json:"default,omitempty" yaml:"default,omitempty"`
}

// Output defines a workflow output parameter
type Output struct {
	Name        string `json:"name" yaml:"name"`
	Variable    string `json:"variable" yaml:"variable"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Options are used to configure a workflow.
type Options struct {
	Name        string         `json:"name" yaml:"name"`
	Steps       []*Step        `json:"steps" yaml:"steps"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Path        string         `json:"path,omitempty" yaml:"path,omitempty"`
	Inputs      []*Input       `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Outputs     []*Output      `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	State       map[string]any `json:"state,omitempty" yaml:"state,omitempty"`
}

// Workflow defines a repeatable process as a graph of steps to be executed.
type Workflow struct {
	name         string
	description  string
	path         string
	inputs       []*Input
	outputs      []*Output
	steps        []*Step
	stepsByName  map[string]*Step
	start        *Step
	initialState map[string]any
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
		name:         opts.Name,
		description:  opts.Description,
		path:         opts.Path,
		inputs:       opts.Inputs,
		outputs:      opts.Outputs,
		steps:        opts.Steps,
		stepsByName:  stepsByName,
		start:        opts.Steps[0],
		initialState: opts.State,
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Path returns the workflow path
func (w *Workflow) Path() string {
	return w.path
}

// Name returns the workflow name
func (w *Workflow) Name() string {
	return w.name
}

// Description returns the workflow description
func (w *Workflow) Description() string {
	return w.description
}

// Inputs returns the workflow inputs
func (w *Workflow) Inputs() []*Input {
	return w.inputs
}

// Outputs returns the workflow outputs
func (w *Workflow) Outputs() []*Output {
	return w.outputs
}

// Steps returns the workflow steps
func (w *Workflow) Steps() []*Step {
	return w.steps
}

// Start returns the workflow start step
func (w *Workflow) Start() *Step {
	return w.start
}

// InitialState returns the workflow initial state
func (w *Workflow) InitialState() map[string]any {
	return w.initialState
}

// GetStep returns a step by name
func (w *Workflow) GetStep(name string) (*Step, bool) {
	step, ok := w.stepsByName[name]
	return step, ok
}

// StepNames returns the names of all steps in the workflow
func (w *Workflow) StepNames() []string {
	names := make([]string, 0, len(w.stepsByName))
	for name := range w.stepsByName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// LoadFile loads a workflow from a YAML file
func LoadFile(path string) (*Workflow, error) {
	yamlData, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow file: %w", err)
	}
	var opts Options
	if err := yaml.Unmarshal(yamlData, &opts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workflow file: %w", err)
	}
	return New(opts)
}

// LoadString loads a workflow from a YAML string
func LoadString(data string) (*Workflow, error) {
	var opts Options
	if err := yaml.Unmarshal([]byte(data), &opts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workflow file: %w", err)
	}
	return New(opts)
}
