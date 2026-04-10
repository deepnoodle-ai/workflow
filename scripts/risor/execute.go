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

// passthrough records a state key whose Go value has no native Risor
// representation. The script sees `placeholder`; if that same placeholder
// object is still in the Risor state map at the end of execution, we restore
// the original Go value in its place so non-primitive types round-trip
// untouched.
type passthrough struct {
	placeholder object.Object
	original    any
}

// ExecuteScript compiles and runs a Risor script with a mutable "state" map
// and read-only "inputs" map exposed as globals. Any mutations the script
// makes to the state map are captured and returned in the result.
//
// State values whose types Risor does not natively understand are passed to
// the script as an opaque string placeholder but preserved on the way back
// out, so scripts cannot silently corrupt non-primitive state entries.
//
// The script activity (see NewScriptActivity) is the primary caller. It is
// exported here so consumers can build their own state-mutating activities.
func ExecuteScript(ctx context.Context, compiler script.Compiler, code string, state, inputs map[string]any) (*ScriptResult, error) {
	if compiler == nil {
		return nil, script.ErrNoScriptCompiler
	}
	stateMap, statePassthrough := goMapToRisorMap(state)
	inputsMap, _ := goMapToRisorMap(inputs)

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
		if pt, ok := statePassthrough[k]; ok && v == pt.placeholder {
			updatedState[k] = pt.original
			continue
		}
		updatedState[k] = v.Interface()
	}
	return &ScriptResult{Value: result.Value(), State: updatedState}, nil
}

// goMapToRisorMap converts a Go map into a Risor Map. The returned map is
// mutable so scripts can add or modify keys, which ExecuteScript then reads
// back to produce the updated state. Values whose types Risor does not
// natively understand are represented in the Risor map as opaque string
// placeholders; the passthrough map returned alongside lets the caller
// restore the original Go values after execution.
func goMapToRisorMap(goMap map[string]any) (*object.Map, map[string]passthrough) {
	risorMap := make(map[string]object.Object, len(goMap))
	passthroughs := map[string]passthrough{}
	for k, v := range goMap {
		obj, preserved := goValueToRisor(v)
		risorMap[k] = obj
		if preserved {
			passthroughs[k] = passthrough{placeholder: obj, original: v}
		}
	}
	return object.NewMap(risorMap), passthroughs
}

// goValueToRisor converts a Go value into a Risor object. Unknown types fall
// back to an opaque string placeholder and set preserved=true so callers can
// restore the original Go value after script execution.
func goValueToRisor(value any) (obj object.Object, preserved bool) {
	if value == nil {
		return object.Nil, false
	}
	switch v := value.(type) {
	case bool:
		return object.NewBool(v), false
	case int:
		return object.NewInt(int64(v)), false
	case int64:
		return object.NewInt(v), false
	case int32:
		return object.NewInt(int64(v)), false
	case float32:
		return object.NewFloat(float64(v)), false
	case float64:
		return object.NewFloat(v), false
	case string:
		return object.NewString(v), false
	case []any:
		list := make([]object.Object, len(v))
		for i, item := range v {
			list[i], _ = goValueToRisor(item)
		}
		return object.NewList(list), false
	case []string:
		list := make([]object.Object, len(v))
		for i, item := range v {
			list[i] = object.NewString(item)
		}
		return object.NewList(list), false
	case []int:
		list := make([]object.Object, len(v))
		for i, item := range v {
			list[i] = object.NewInt(int64(item))
		}
		return object.NewList(list), false
	case map[string]any:
		m, _ := goMapToRisorMap(v)
		return m, false
	case time.Time:
		return object.NewTime(v), false
	default:
		return object.NewString(fmt.Sprintf("%v", v)), true
	}
}
