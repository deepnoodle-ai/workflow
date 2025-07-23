package activities

import (
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/workflow/script"
	"github.com/risor-io/risor/object"
)

// ScriptParams defines the parameters for the script activity
type ScriptParams struct {
	Code string `json:"code"`
}

// ScriptActivity executes a script.
type ScriptActivity struct{}

func NewScriptActivity() workflow.Activity {
	return workflow.NewTypedActivity(&ScriptActivity{})
}

func (a *ScriptActivity) Name() string {
	return "script"
}

func (a *ScriptActivity) Execute(ctx workflow.Context, params ScriptParams) (any, error) {
	code := params.Code
	if code == "" {
		return nil, fmt.Errorf("missing 'code' parameter")
	}

	// Get the original state before script execution
	originalState := workflow.VariablesFromContext(ctx)

	// Create Risor maps for the state and inputs
	stateMap := convertMapToRisorMap(originalState)
	inputsMap := convertMapToRisorMap(workflow.InputsFromContext(ctx))

	// Create a map with global variables that will be provided to the script.
	scriptGlobals := script.DefaultRisorGlobals()
	scriptGlobals["state"] = stateMap
	scriptGlobals["inputs"] = inputsMap

	compiler := script.NewRisorScriptingEngine(scriptGlobals)

	// Compile the script
	compiledScript, err := compiler.Compile(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to compile script: %w", err)
	}

	// Run the script
	result, err := compiledScript.Evaluate(ctx, scriptGlobals)
	if err != nil {
		return nil, fmt.Errorf("failed to execute script: %w", err)
	}

	// Convert back to Go map for comparison
	resultState := map[string]any{}
	for k, v := range stateMap.Value() {
		resultState[k] = v.Interface()
	}

	// Generate patches based on the differences
	patches := workflow.GeneratePatches(originalState, resultState)

	// Apply the patches to the state
	if len(patches) > 0 {
		workflow.ApplyPatches(ctx, patches)
	}

	return result.Value(), nil
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
		return object.Nil
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
	case time.Time:
		return object.NewTime(v)
	default:
		// For other types, convert to string representation
		return object.NewString(fmt.Sprintf("%v", v))
	}
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
