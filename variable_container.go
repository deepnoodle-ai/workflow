package workflow

import (
	"reflect"
)

// VariableContainer is a container for state variables.
type VariableContainer interface {

	// SetVariable sets the value of a state variable.
	SetVariable(key string, value any)

	// DeleteVariable deletes a state variable.
	DeleteVariable(key string)

	// ListVariables returns a slice containing all variable names.
	ListVariables() []string

	// GetVariable returns the value of a state variable.
	GetVariable(key string) (value any, exists bool)
}

// PatchOptions is used to create a Patch.
type PatchOptions struct {
	Variable string
	Value    any
	Delete   bool
}

// Patch represents a change to a state variable.
type Patch struct {
	variable string
	value    any
	delete   bool
}

func (p Patch) Variable() string {
	return p.variable
}

func (p Patch) Value() any {
	return p.value
}

func (p Patch) Delete() bool {
	return p.delete
}

// NewPatch creates a new Patch.
func NewPatch(opts PatchOptions) Patch {
	return Patch{
		variable: opts.Variable,
		value:    opts.Value,
		delete:   opts.Delete,
	}
}

// GeneratePatches compares original and modified state maps and returns patches
// for the differences.
func GeneratePatches(original, modified map[string]any) []Patch {
	var patches []Patch
	// Check for modified or added variables
	for key, currentValue := range modified {
		if originalValue, exists := original[key]; exists {
			// Variable existed before - check if it was modified
			if !reflect.DeepEqual(originalValue, currentValue) {
				patches = append(patches, Patch{
					variable: key,
					value:    currentValue,
				})
			}
		} else {
			// New variable added
			patches = append(patches, Patch{
				variable: key,
				value:    currentValue,
			})
		}
	}
	// Check for deleted variables
	for key := range original {
		if _, exists := modified[key]; !exists {
			// Variable was deleted
			patches = append(patches, Patch{
				variable: key,
				delete:   true,
			})
		}
	}
	return patches
}

// ApplyPatches applies a list of patches to a variable container.
func ApplyPatches(container VariableContainer, patches []Patch) {
	for _, patch := range patches {
		if patch.delete {
			container.DeleteVariable(patch.variable)
		} else {
			container.SetVariable(patch.variable, patch.value)
		}
	}
}
