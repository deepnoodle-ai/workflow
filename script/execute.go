package script

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
)

// ScriptResult holds the result of a script execution along with the
// updated state after the script has run.
type ScriptResult struct {
	// Value is the return value of the script.
	Value any
	// State is the updated state map after script execution.
	State map[string]any
}

// ExecuteScript compiles and runs a script using the given compiler,
// injecting state and inputs as globals. It returns the script's return
// value and the updated state map. All Risor-specific type conversion
// is handled internally.
func ExecuteScript(ctx context.Context, compiler Compiler, code string, state, inputs map[string]any) (*ScriptResult, error) {
	stateMap := goMapToRisorMap(state)
	inputsMap := goMapToRisorMap(inputs)

	compiled, err := compiler.Compile(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to compile script: %w", err)
	}

	evalGlobals := map[string]any{
		"state":  stateMap,
		"inputs": inputsMap,
	}
	result, err := compiled.Evaluate(ctx, evalGlobals)
	if err != nil {
		return nil, fmt.Errorf("failed to execute script: %w", err)
	}

	// Convert the mutated Risor map back to a Go map
	updatedState := make(map[string]any, len(stateMap.Value()))
	for k, v := range stateMap.Value() {
		updatedState[k] = v.Interface()
	}

	return &ScriptResult{Value: result.Value(), State: updatedState}, nil
}

// goMapToRisorMap converts a Go map to a Risor Map object.
func goMapToRisorMap(goMap map[string]any) *object.Map {
	risorMap := make(map[string]object.Object, len(goMap))
	for k, v := range goMap {
		risorMap[k] = goValueToRisor(v)
	}
	return object.NewMap(risorMap)
}

// goValueToRisor converts a Go value to a Risor object.
func goValueToRisor(value any) object.Object {
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
			risorList[i] = goValueToRisor(item)
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
		return goMapToRisorMap(v)
	case time.Time:
		return object.NewTime(v)
	default:
		return object.NewString(fmt.Sprintf("%v", v))
	}
}
