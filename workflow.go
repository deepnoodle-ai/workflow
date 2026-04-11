package workflow

import (
	"fmt"
	"sort"
)

// Input defines a workflow input parameter
type Input struct {
	Name        string      `json:"name" yaml:"name"`
	Type        string      `json:"type" yaml:"type"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Default     interface{} `json:"default,omitempty" yaml:"default,omitempty"`
}

func (i *Input) IsRequired() bool {
	return i.Default == nil
}

// Output defines a workflow output parameter
type Output struct {
	Name        string `json:"name" yaml:"name"`
	Variable    string `json:"variable" yaml:"variable"`
	// Branch names the execution branch to extract the output value from.
	// Defaults to "main" when empty.
	Branch      string `json:"branch,omitempty" yaml:"branch,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// Options are used to configure a workflow.
type Options struct {
	Name        string         `json:"name" yaml:"name"`
	Steps       []*Step        `json:"steps" yaml:"steps"`
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Inputs      []*Input       `json:"inputs,omitempty" yaml:"inputs,omitempty"`
	Outputs     []*Output      `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	State       map[string]any `json:"state,omitempty" yaml:"state,omitempty"`
}

// Workflow defines a repeatable process as a graph of steps to be executed.
type Workflow struct {
	name         string
	description  string
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

	return &Workflow{
		name:         opts.Name,
		description:  opts.Description,
		inputs:       opts.Inputs,
		outputs:      opts.Outputs,
		steps:        opts.Steps,
		stepsByName:  stepsByName,
		start:        opts.Steps[0],
		initialState: opts.State,
	}, nil
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

// validateWorkflowSteps validates the workflow step structure
func validateWorkflowSteps(stepsByName map[string]*Step) error {
	usedBranchNames := map[string]bool{}
	for _, step := range stepsByName {
		if step.Name == "" {
			return fmt.Errorf("empty step name detected")
		}
		for _, edge := range step.Next {
			if _, ok := stepsByName[edge.Step]; !ok {
				return fmt.Errorf("invalid edge detected on step %q: destination step %q not found",
					step.Name, edge.Step)
			}
			// Confirm reserved branch names are not used
			if edge.BranchName != "" {
				if edge.BranchName == "main" {
					return fmt.Errorf("branch name 'main' is reserved and cannot be used in step %q", step.Name)
				}
				if usedBranchNames[edge.BranchName] {
					return fmt.Errorf("branch name %q is already used", edge.BranchName)
				}
				usedBranchNames[edge.BranchName] = true
			}
		}
	}
	return nil
}
