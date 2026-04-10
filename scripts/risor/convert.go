package risor

import (
	"sort"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
)

// risorObjectToGo recursively converts a Risor object into a Go value. Maps
// are iterated in sorted key order so conversions are deterministic.
func risorObjectToGo(obj object.Object) any {
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
		result := make([]any, 0, len(o.Value()))
		for _, item := range o.Value() {
			result = append(result, risorObjectToGo(item))
		}
		return result
	case *object.Map:
		m := o.Value()
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		result := make(map[string]any, len(keys))
		for _, k := range keys {
			result[k] = risorObjectToGo(m[k])
		}
		return result
	default:
		return obj.Inspect()
	}
}
