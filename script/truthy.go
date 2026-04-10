package script

import "strings"

// IsTruthyValue returns whether a plain Go value should be treated as truthy
// in workflow conditions. Scripting engines should use this helper when
// implementing Value.IsTruthy for values that are already Go-native.
//
// The rules are intentionally pragmatic for workflow authoring:
//   - bool: itself
//   - numeric: non-zero is truthy
//   - string: non-empty and not "false" (case-insensitive)
//   - slices/maps: non-empty is truthy
//   - nil: false
//   - anything else: non-nil is truthy
func IsTruthyValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
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
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != "" && !strings.EqualFold(v, "false")
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	default:
		return value != nil
	}
}
