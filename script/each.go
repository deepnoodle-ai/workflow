package script

import "fmt"

// EachValue converts a plain Go value into a slice of items for iteration
// by the workflow engine's `each` blocks. Scripting engines should use this
// helper when implementing Value.Items for values that are already Go-native.
//
// Slices become themselves. Maps become a list of {"key", "value"} pairs.
// Scalars become single-element slices. Unknown types return an error.
func EachValue(value any) ([]any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
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
	case []int64:
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
		result := make([]any, 0, len(v))
		for key, val := range v {
			result = append(result, map[string]any{"key": key, "value": val})
		}
		return result, nil
	case string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return []any{v}, nil
	default:
		return nil, fmt.Errorf("unsupported value type for 'each': %T", value)
	}
}
