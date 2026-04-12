package workflow

import (
	"reflect"
)

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

func (p patch) Variable() string { return p.variable }
func (p patch) Value() any       { return p.value }
func (p patch) Delete() bool     { return p.delete }

// newPatch creates a new patch.
func newPatch(opts patchOptions) patch {
	return patch{
		variable: opts.Variable,
		value:    opts.Value,
		delete:   opts.Delete,
	}
}

// generatePatches compares original and modified state maps and
// returns patches for the differences.
func generatePatches(original, modified map[string]any) []patch {
	var patches []patch
	for key, currentValue := range modified {
		if originalValue, exists := original[key]; exists {
			if !reflect.DeepEqual(originalValue, currentValue) {
				patches = append(patches, patch{variable: key, value: currentValue})
			}
		} else {
			patches = append(patches, patch{variable: key, value: currentValue})
		}
	}
	for key := range original {
		if _, exists := modified[key]; !exists {
			patches = append(patches, patch{variable: key, delete: true})
		}
	}
	return patches
}

// applyPatches applies a list of patches to a branch-local state map.
func applyPatches(state *BranchLocalState, patches []patch) {
	for _, p := range patches {
		if p.delete {
			state.Delete(p.variable)
		} else {
			state.Set(p.variable, p.value)
		}
	}
}
