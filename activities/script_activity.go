package activities

import (
	"context"
	"fmt"
	"reflect"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/script"
	"github.com/deepnoodle-ai/workflow/state"
	"github.com/risor-io/risor/object"
)

// ScriptActivity handles script execution (replaces "script" step type)
type ScriptActivity struct{}

func (a *ScriptActivity) Name() string {
	return "script"
}

func (a *ScriptActivity) Execute(ctx context.Context, params map[string]any) (any, error) {
	code, ok := params["code"].(string)
	if !ok || code == "" {
		return nil, fmt.Errorf("missing 'code' parameter")
	}

	stateReader, ok := workflow.GetStateFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing state reader in context")
	}

	compiler, ok := workflow.GetCompilerFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("missing compiler in context")
	}

	// Get the original state before script execution
	originalState := stateReader.GetVariables()

	// Create a mutable copy for the script to modify
	scriptState := make(map[string]any)
	for k, v := range originalState {
		scriptState[k] = v
	}

	globals := map[string]any{
		"inputs": stateReader.GetInputs(),
		"state": object.NewMap(map[string]object.Object{
			"counter": object.NewInt(0),
		}), // Use the mutable copy
	}

	// Compile the script using the engine
	compiledScript, err := compiler.Compile(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to compile script: %w", err)
	}

	// Execute the compiled script
	result, err := compiledScript.Evaluate(ctx, globals)
	if err != nil {
		return nil, fmt.Errorf("failed to execute script: %w", err)
	}

	for k, v := range originalState {
		fmt.Println("original state", k, v)
	}
	stateG := globals["state"].(*object.Map)
	resultState := script.ConvertRisorValueToGo(stateG).(map[string]any)
	for k, v := range resultState {
		fmt.Println("stateG", k, v)
	}
	for k, v := range scriptState {
		fmt.Println("script state", k, v)
	}

	// Generate patches based on the differences
	patches := generatePatches(originalState, resultState)

	// Apply the patches to the state
	if len(patches) > 0 {
		fmt.Println("applying patches", len(patches), patches)
		stateReader.ApplyPatches(patches)
	} else {
		fmt.Println("no patches to apply")
	}

	return result.Value(), nil
}

// generatePatches compares original and modified state maps and returns patches for the differences
func generatePatches(original, modified map[string]any) []state.Patch {
	var patches []state.Patch

	// Check for modified or added variables
	for key, newValue := range modified {
		if originalValue, exists := original[key]; exists {
			// Variable existed before - check if it was modified
			if !reflect.DeepEqual(originalValue, newValue) {
				patches = append(patches, state.Patch{
					Variable: key,
					Value:    newValue,
					Delete:   false,
				})
			}
		} else {
			// New variable added
			patches = append(patches, state.Patch{
				Variable: key,
				Value:    newValue,
				Delete:   false,
			})
		}
	}

	// Check for deleted variables
	for key := range original {
		if _, exists := modified[key]; !exists {
			// Variable was deleted
			patches = append(patches, state.Patch{
				Variable: key,
				Value:    nil,
				Delete:   true,
			})
		}
	}

	return patches
}
