package risor

import (
	"fmt"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
)

// risorObjectToGo recursively converts a Risor object into a Go value.
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
		result := make(map[string]any, len(o.Value()))
		for key, value := range o.Value() {
			result[key] = risorObjectToGo(value)
		}
		return result
	default:
		return obj.Inspect()
	}
}

// eachRisorValue converts a Risor object into a slice of items for use in
// `each` blocks. Lists and maps expand; scalars become single-element slices.
func eachRisorValue(obj object.Object) ([]any, error) {
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
			sub, err := eachRisorValue(item)
			if err != nil {
				return nil, err
			}
			values = append(values, sub...)
		}
		return values, nil
	case *object.Map:
		var values []any
		for _, item := range o.Value() {
			sub, err := eachRisorValue(item)
			if err != nil {
				return nil, err
			}
			values = append(values, sub...)
		}
		return values, nil
	default:
		return nil, fmt.Errorf("unsupported risor result type for 'each': %T", obj)
	}
}
