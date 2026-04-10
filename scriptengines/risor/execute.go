package risor

import (
	"context"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
	"github.com/deepnoodle-ai/workflow/script"
)

// ScriptResult holds the result of executing a Risor script with mutable
// state. It is the return type of ExecuteScript.
type ScriptResult struct {
	// Value is the return value of the script.
	Value any
	// State is the updated state map after script execution.
	State map[string]any
}

// ExecuteScript compiles and runs a Risor script with a mutable "state" map
// and read-only "inputs" map exposed as globals. Any mutations the script
// makes to the state map are captured and returned in the result.
//
// The script activity (see NewScriptActivity) is the primary caller. It is
// exported here so consumers can build their own state-mutating activities.
func ExecuteScript(ctx context.Context, compiler script.Compiler, code string, state, inputs map[string]any) (*ScriptResult, error) {
	stateMap := goMapToRisorMap(state)
	inputsMap := goMapToRisorMap(inputs)

	compiled, err := compiler.Compile(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to compile script: %w", err)
	}

	result, err := compiled.Evaluate(ctx, map[string]any{
		"state":  stateMap,
		"inputs": inputsMap,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to execute script: %w", err)
	}

	updatedState := make(map[string]any, len(stateMap.Value()))
	for k, v := range stateMap.Value() {
		updatedState[k] = v.Interface()
	}
	return &ScriptResult{Value: result.Value(), State: updatedState}, nil
}

// goMapToRisorMap converts a Go map into a Risor Map. The returned map is
// mutable so scripts can add or modify keys, which ExecuteScript then reads
// back to produce the updated state.
func goMapToRisorMap(goMap map[string]any) *object.Map {
	risorMap := make(map[string]object.Object, len(goMap))
	for k, v := range goMap {
		risorMap[k] = goValueToRisor(v)
	}
	return object.NewMap(risorMap)
}

// goValueToRisor converts a Go value into a Risor object. Unknown types fall
// back to their string representation so scripts can at least inspect them.
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
		list := make([]object.Object, len(v))
		for i, item := range v {
			list[i] = goValueToRisor(item)
		}
		return object.NewList(list)
	case []string:
		list := make([]object.Object, len(v))
		for i, item := range v {
			list[i] = object.NewString(item)
		}
		return object.NewList(list)
	case []int:
		list := make([]object.Object, len(v))
		for i, item := range v {
			list[i] = object.NewInt(int64(item))
		}
		return object.NewList(list)
	case map[string]any:
		return goMapToRisorMap(v)
	case time.Time:
		return object.NewTime(v)
	default:
		return object.NewString(fmt.Sprintf("%v", v))
	}
}
