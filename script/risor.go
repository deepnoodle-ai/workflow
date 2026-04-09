package script

import (
	"context"
	"fmt"
	"strings"
	"time"

	risor "github.com/deepnoodle-ai/risor/v2"
	"github.com/deepnoodle-ai/risor/v2/pkg/bytecode"
	"github.com/deepnoodle-ai/risor/v2/pkg/object"
)

type RisorScript struct {
	engine *RisorScriptingEngine
	code   *bytecode.Code
}

func (s *RisorScript) Evaluate(ctx context.Context, globals map[string]any) (Value, error) {
	combinedGlobals := make(map[string]any)
	for name, value := range s.engine.globals {
		combinedGlobals[name] = value
	}
	for name, value := range globals {
		combinedGlobals[name] = value
	}
	value, err := risor.Run(ctx, s.code,
		risor.WithEnv(combinedGlobals),
		risor.WithRawResult(),
		risor.WithSyntax(risor.BasicScripting),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate risor script: %w", err)
	}
	return &RisorValue{obj: value.(object.Object)}, nil
}

type RisorScriptingEngine struct {
	globals map[string]any
}

func NewRisorScriptingEngine(globals map[string]any) *RisorScriptingEngine {
	return &RisorScriptingEngine{globals: globals}
}

func (e *RisorScriptingEngine) Compile(ctx context.Context, code string) (Script, error) {
	compiledCode, err := risor.Compile(ctx, code, risor.WithEnv(e.globals), risor.WithSyntax(risor.BasicScripting))
	if err != nil {
		return nil, err
	}
	return &RisorScript{engine: e, code: compiledCode}, nil
}

type RisorValue struct {
	obj object.Object
}

func (value *RisorValue) Value() any {
	switch o := value.obj.(type) {
	case *object.String:
		return o.Value()
	case *object.Int:
		return o.Value()
	case *object.Float:
		return o.Value()
	case *object.Bool:
		return o.Value()
	case *object.Time:
		return o.Value()
	case *object.NilType:
		return nil
	case *object.List:
		var result []interface{}
		for _, item := range o.Value() {
			result = append(result, ConvertRisorValueToGo(item))
		}
		return result
	case *object.Map:
		result := make(map[string]interface{})
		for key, value := range o.Value() {
			result[key] = ConvertRisorValueToGo(value)
		}
		return result
	default:
		// Fallback to string representation
		return o.Inspect()
	}
}

func (value *RisorValue) IsTruthy() bool {
	switch obj := value.obj.(type) {
	case *object.Bool:
		return obj.Value()
	case *object.Int:
		return obj.Value() != 0
	case *object.Float:
		return obj.Value() != 0.0
	case *object.List:
		return len(obj.Value()) > 0
	case *object.Map:
		return len(obj.Value()) > 0
	case *object.String:
		val := obj.Value()
		return val != "" && strings.ToLower(val) != "false"
	default:
		// Use Risor's built-in truthiness evaluation
		return obj.IsTruthy()
	}
}

func (value *RisorValue) Items() ([]any, error) {
	switch o := value.obj.(type) {
	case *object.String:
		return []any{o.Value()}, nil
	case *object.Int:
		return []any{o.Value()}, nil
	case *object.Float:
		return []any{o.Value()}, nil
	case *object.Bool:
		return []any{o.Value()}, nil
	case *object.Time:
		return []any{o.Value()}, nil
	case *object.List:
		var values []any
		for _, item := range o.Value() {
			subValues, err := ConvertEachValue(item)
			if err != nil {
				return nil, err
			}
			values = append(values, subValues...)
		}
		return values, nil
	case *object.Map:
		var values []any
		for _, item := range o.Value() {
			subValues, err := ConvertEachValue(item)
			if err != nil {
				return nil, err
			}
			values = append(values, subValues...)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported risor result type for 'each': %T", value.obj)
	}
}

func (value *RisorValue) String() string {
	// Convert the result to a string based on its type
	var strValue string
	switch v := value.obj.(type) {
	case *object.String:
		strValue = v.Value()
	case *object.Int:
		strValue = fmt.Sprintf("%d", v.Value())
	case *object.Float:
		strValue = fmt.Sprintf("%g", v.Value())
	case *object.Bool:
		strValue = fmt.Sprintf("%t", v.Value())
	case *object.Time:
		strValue = v.Value().Format(time.RFC3339)
	case *object.NilType:
		strValue = ""
	case *object.List:
		// Double newline between each item
		var items []string
		for _, item := range v.Value() {
			items = append(items, fmt.Sprintf("%v", item))
		}
		strValue = strings.Join(items, "\n\n")
	case *object.Map:
		// Double newline between each key-value pair
		var items []string
		for k, v := range v.Value() {
			items = append(items, fmt.Sprintf("%s: %v", k, v))
		}
		strValue = strings.Join(items, "\n\n")
	case fmt.Stringer:
		strValue = v.String()
	default:
		return fmt.Sprintf("%v", value.obj)
	}
	return strValue
}

func DefaultRisorGlobals() map[string]any {
	allowed := GetAllowedGlobals()
	globals := make(map[string]any)
	for name, value := range risor.Builtins() {
		if allowed[name] {
			globals[name] = value
		}
	}
	globals["inputs"] = map[string]any{}
	globals["state"] = map[string]any{}
	return globals
}
