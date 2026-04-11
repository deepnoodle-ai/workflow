package script

import (
	"fmt"
	"reflect"
	"sort"
)

// EachValue converts a plain Go value into a slice of items for iteration
// by the workflow engine's `each` blocks. Scripting engines should use this
// helper when implementing Value.Items for values that are already Go-native.
//
// Slices and arrays become a []any of their elements. Maps with string keys
// become a list of {"key", "value"} pairs ordered by sorted key so iteration
// is deterministic. Scalars become single-element slices. Unknown types
// return an error.
func EachValue(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	switch value.(type) {
	case string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return []any{value}, nil
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = rv.Index(i).Interface()
		}
		return result, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("unsupported value type for 'each': %T", value)
		}
		mapKeys := rv.MapKeys()
		keys := make([]string, len(mapKeys))
		for i, k := range mapKeys {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		result := make([]any, 0, len(keys))
		for _, k := range keys {
			v := rv.MapIndex(reflect.ValueOf(k)).Interface()
			result = append(result, map[string]any{"key": k, "value": v})
		}
		return result, nil
	}
	return nil, fmt.Errorf("unsupported value type for 'each': %T", value)
}
