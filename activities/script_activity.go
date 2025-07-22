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

// ScriptParams defines the parameters for the script activity
type ScriptParams struct {
	Code string `json:"code"`
}

// ScriptResult defines the result of the script activity
type ScriptResult struct {
	Result any `json:"result"`
}

// ScriptActivity handles script execution (replaces "script" step type)
type ScriptActivity struct{}

func NewScriptActivity() workflow.Activity {
	return workflow.NewTypedActivity(&ScriptActivity{})
}

func (a *ScriptActivity) Name() string {
	return "script"
}

func (a *ScriptActivity) Execute(ctx context.Context, params ScriptParams) (ScriptResult, error) {
	code := params.Code
	if code == "" {
		return ScriptResult{}, fmt.Errorf("missing 'code' parameter")
	}

	stateReader, ok := workflow.GetStateFromContext(ctx)
	if !ok {
		return ScriptResult{}, fmt.Errorf("missing state reader in context")
	}

	// Get the original state before script execution
	originalState := stateReader.GetVariables()

	// Convert workflow state to Risor objects for script manipulation
	stateMap := convertMapToRisorMap(originalState)
	inputsMap := convertMapToRisorMap(stateReader.GetInputs())

	// Set up a new compiler with the globals it needs to know about plus default Risor built-ins
	engineGlobals := script.DefaultRisorGlobals()
	engineGlobals["inputs"] = inputsMap
	engineGlobals["state"] = stateMap

	risorEngine := script.NewRisorScriptingEngine(engineGlobals)

	globals := map[string]any{
		"inputs": inputsMap,
		"state":  stateMap,
	}

	// Compile the script using the properly configured engine
	compiledScript, err := risorEngine.Compile(ctx, code)
	if err != nil {
		return ScriptResult{}, fmt.Errorf("failed to compile script: %w", err)
	}

	// Execute the compiled script
	result, err := compiledScript.Evaluate(ctx, globals)
	if err != nil {
		return ScriptResult{}, fmt.Errorf("failed to execute script: %w", err)
	}

	// Extract the modified state from the script globals
	modifiedStateMap, ok := globals["state"].(*object.Map)
	if !ok {
		return ScriptResult{}, fmt.Errorf("state was not properly maintained as a Risor Map object")
	}

	// Convert back to Go map for comparison and handle nil values as deletions
	resultState := convertRisorMapWithDeletions(modifiedStateMap, originalState)

	// Generate patches based on the differences
	patches := generatePatches(originalState, resultState)

	// Apply the patches to the state
	if len(patches) > 0 {
		stateReader.ApplyPatches(patches)
	}

	return ScriptResult{Result: result.Value()}, nil
}

// convertMapToRisorMap converts a Go map to a Risor Map object
func convertMapToRisorMap(goMap map[string]any) *object.Map {
	risorMap := make(map[string]object.Object)
	for k, v := range goMap {
		risorMap[k] = convertGoValueToRisor(v)
	}
	return object.NewMap(risorMap)
}

// convertGoValueToRisor converts a Go value to a Risor object
func convertGoValueToRisor(value any) object.Object {
	if value == nil {
		// Return a string representation for nil - Risor will handle it
		return object.NewString("")
	}

	switch v := value.(type) {
	case bool:
		return object.NewBool(v)
	case int:
		return object.NewInt(int64(v))
	case int64:
		return object.NewInt(v)
	case int32:
		return object.NewInt(int64(v))
	case float32:
		return object.NewFloat(float64(v))
	case float64:
		return object.NewFloat(v)
	case string:
		return object.NewString(v)
	case []any:
		risorList := make([]object.Object, len(v))
		for i, item := range v {
			risorList[i] = convertGoValueToRisor(item)
		}
		return object.NewList(risorList)
	case []string:
		risorList := make([]object.Object, len(v))
		for i, item := range v {
			risorList[i] = object.NewString(item)
		}
		return object.NewList(risorList)
	case []int:
		risorList := make([]object.Object, len(v))
		for i, item := range v {
			risorList[i] = object.NewInt(int64(item))
		}
		return object.NewList(risorList)
	case map[string]any:
		return convertMapToRisorMap(v)
	default:
		// For other types, convert to string representation
		return object.NewString(fmt.Sprintf("%v", v))
	}
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

// convertRisorMapWithDeletions converts a Risor map to Go map, detecting nil values as deletions
func convertRisorMapWithDeletions(risorMap *object.Map, originalState map[string]any) map[string]any {
	result := make(map[string]any)

	// Convert all non-nil values
	for key, risorValue := range risorMap.Value() {
		goValue := script.ConvertRisorValueToGo(risorValue)

		// Check if this represents a deletion (nil value or "nil" string)
		if goValue == nil || goValue == "nil" || (goValue == "" && originalState[key] != "") {
			// This is a deletion - exclude from result
			continue
		}

		result[key] = goValue
	}

	return result
}
