package script

import (
	"fmt"
	"strings"

	"github.com/risor-io/risor/object"
)

// ConvertRisorValueToGo converts a Risor object to a Go value
func ConvertRisorValueToGo(obj object.Object) any {
	switch o := obj.(type) {
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

	case *object.Set:
		var result []interface{}
		for _, item := range o.Value() {
			result = append(result, ConvertRisorValueToGo(item))
		}
		return result

	default:
		// Fallback to string representation
		return obj.Inspect()
	}
}

// ConvertRisorValueToBool converts a Risor object to a boolean indicating truthiness
func ConvertRisorValueToBool(obj object.Object) bool {
	switch obj := obj.(type) {
	case *object.Bool:
		return obj.Value()

	case *object.Int:
		return obj.Value() != 0

	case *object.Float:
		return obj.Value() != 0.0

	case *object.String:
		val := obj.Value()
		return val != "" && strings.ToLower(val) != "false"

	case *object.List:
		return len(obj.Value()) > 0

	case *object.Map:
		return len(obj.Value()) > 0

	default:
		// Use Risor's built-in truthiness evaluation
		return obj.IsTruthy()
	}
}

// ConvertValueToBool converts any value to a boolean indicating truthiness
// This works with both Risor objects and plain Go values for extensibility
func ConvertValueToBool(value any) bool {
	// First try to treat it as a Risor object
	if obj, ok := value.(object.Object); ok {
		return ConvertRisorValueToBool(obj)
	}

	// Handle plain Go values
	switch v := value.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case int8:
		return v != 0
	case int16:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case uint:
		return v != 0
	case uint8:
		return v != 0
	case uint16:
		return v != 0
	case uint32:
		return v != 0
	case uint64:
		return v != 0
	case float32:
		return v != 0.0
	case float64:
		return v != 0.0
	case string:
		return v != "" && strings.ToLower(v) != "false"
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	case nil:
		return false
	default:
		// For unknown types, check if they're non-nil
		return value != nil
	}
}

// ConvertEachValue converts any value to an array of values for use in each blocks
// This works with both Risor objects and plain Go values for extensibility
func ConvertEachValue(value any) ([]any, error) {
	// Handle Risor objects - implement Risor-specific conversion inline
	if obj, ok := value.(object.Object); ok {
		switch o := obj.(type) {
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
		case *object.Set:
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
			return nil, fmt.Errorf("unsupported risor result type for 'each': %T", obj)
		}
	}

	// Handle plain Go values
	switch v := value.(type) {
	case []any:
		return v, nil
	case []string:
		result := make([]any, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result, nil
	case []int:
		result := make([]any, len(v))
		for i, n := range v {
			result[i] = n
		}
		return result, nil
	case []float64:
		result := make([]any, len(v))
		for i, f := range v {
			result[i] = f
		}
		return result, nil
	case map[string]any:
		var result []any
		for key, value := range v {
			result = append(result, map[string]any{"key": key, "value": value})
		}
		return result, nil
	case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool:
		// Single values become single-item arrays
		return []any{v}, nil
	default:
		return nil, fmt.Errorf("unsupported value type for 'each': %T", value)
	}
}

// GetSafeGlobals returns a map of Risor built-in function names that are safe
// to use in workflows due to being deterministic with no side effects.
func GetSafeGlobals() map[string]bool {
	return map[string]bool{
		"all":         true,
		"any":         true,
		"base64":      true,
		"bool":        true,
		"buffer":      true,
		"byte_slice":  true,
		"byte":        true,
		"bytes":       true,
		"call":        true,
		"chunk":       true,
		"coalesce":    true,
		"decode":      true,
		"encode":      true,
		"error":       true,
		"errorf":      true,
		"errors":      true,
		"filepath":    true,
		"float_slice": true,
		"float":       true,
		"fmt":         true,
		"getattr":     true,
		"int":         true,
		"is_hashable": true,
		"iter":        true,
		"json":        true,
		"keys":        true,
		"len":         true,
		"list":        true,
		"map":         true,
		"math":        true,
		"regexp":      true,
		"reversed":    true,
		"set":         true,
		"sorted":      true,
		"sprintf":     true,
		"string":      true,
		"strings":     true,
		"try":         true,
		"type":        true,
	}
}
