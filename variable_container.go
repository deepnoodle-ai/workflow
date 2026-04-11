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

// patchOptions is used to create a patch.
type patchOptions struct {
	Variable string
	Value    any
	Delete   bool
}

// patch represents a change to a state variable.
type patch struct {
	variable string
	value    any
	delete   bool
}

func (p patch) Variable() string {
	return p.variable
}

func (p patch) Value() any {
	return p.value
}

func (p patch) Delete() bool {
	return p.delete
}

// newPatch creates a new patch.
func newPatch(opts patchOptions) patch {
	return patch{
		variable: opts.Variable,
		value:    opts.Value,
		delete:   opts.Delete,
	}
}

// generatePatches compares original and modified state maps and returns patches
// for the differences.
func generatePatches(original, modified map[string]any) []patch {
	var patches []patch
	// Check for modified or added variables
	for key, currentValue := range modified {
		if originalValue, exists := original[key]; exists {
			// Variable existed before - check if it was modified
			if !reflect.DeepEqual(originalValue, currentValue) {
				patches = append(patches, patch{
					variable: key,
					value:    currentValue,
				})
			}
		} else {
			// New variable added
			patches = append(patches, patch{
				variable: key,
				value:    currentValue,
			})
		}
	}
	// Check for deleted variables
	for key := range original {
		if _, exists := modified[key]; !exists {
			// Variable was deleted
			patches = append(patches, patch{
				variable: key,
				delete:   true,
			})
		}
	}
	return patches
}

// applyPatches applies a list of patches to a variable container.
func applyPatches(container VariableContainer, patches []patch) {
	for _, patch := range patches {
		if patch.delete {
			container.DeleteVariable(patch.variable)
		} else {
			container.SetVariable(patch.variable, patch.value)
		}
	}
}
