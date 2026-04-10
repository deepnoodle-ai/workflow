package goexpr

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deepnoodle-ai/workflow/script"
)

// Builtins returns a fresh copy of the default builtin function set made
// available to every expression. The returned map is owned by the caller
// and safe to mutate.
//
// The defaults are chosen to be small, deterministic, side-effect free,
// and useful for typical condition/template work:
//
//	len(v)            rune count for strings; element count for
//	                  slice/array/map/chan
//	string(v)         stringified form of v
//	int(v)            numeric conversion to int64; strings parse strictly
//	                  as base-10 integers
//	float(v)          numeric conversion to float64; strings parse strictly
//	bool(v)           truthiness check (matches script.IsTruthyValue)
//	contains(h, n)    substring for strings, element membership for
//	                  slices/arrays (using loose numeric equality), or
//	                  key presence for string-keyed maps
//	has(m, k)         true if map m has key k; errors if m is not a map
//	keys(m)           sorted string keys of a map
//	lower(s), upper(s)  case conversion
//	sprintf(fmt, ...) fmt.Sprintf passthrough
func Builtins() map[string]any {
	return map[string]any{
		"len":      builtinLen,
		"string":   builtinString,
		"int":      builtinInt,
		"float":    builtinFloat,
		"bool":     script.IsTruthyValue,
		"contains": builtinContains,
		"has":      builtinHas,
		"keys":     builtinKeys,
		"lower":    strings.ToLower,
		"upper":    strings.ToUpper,
		"sprintf":  fmt.Sprintf,
	}
}

func builtinLen(v any) (int, error) {
	if v == nil {
		return 0, nil
	}
	if s, ok := v.(string); ok {
		return utf8.RuneCountInString(s), nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return utf8.RuneCountInString(rv.String()), nil
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Chan:
		return rv.Len(), nil
	}
	return 0, fmt.Errorf("%w: len: unsupported type %T", ErrEvaluate, v)
}

func builtinString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func builtinInt(v any) (int64, error) {
	if v == nil {
		return 0, nil
	}
	if i, ok := toInt64(v); ok {
		return i, nil
	}
	if f, ok := toFloat64(v); ok {
		return int64(f), nil
	}
	if s, ok := v.(string); ok {
		i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: int: cannot parse %q", ErrEvaluate, s)
		}
		return i, nil
	}
	return 0, fmt.Errorf("%w: int: unsupported type %T", ErrEvaluate, v)
}

func builtinFloat(v any) (float64, error) {
	if v == nil {
		return 0, nil
	}
	if f, ok := toFloat64(v); ok {
		return f, nil
	}
	if s, ok := v.(string); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: float: cannot parse %q", ErrEvaluate, s)
		}
		return f, nil
	}
	return 0, fmt.Errorf("%w: float: unsupported type %T", ErrEvaluate, v)
}

func builtinContains(haystack, needle any) (bool, error) {
	if haystack == nil {
		return false, nil
	}
	if s, ok := haystack.(string); ok {
		sub, ok := needle.(string)
		if !ok {
			return false, fmt.Errorf("%w: contains: needle must be string for string haystack, got %T", ErrEvaluate, needle)
		}
		return strings.Contains(s, sub), nil
	}
	rv := reflect.ValueOf(haystack)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			eq, _ := looseEqual(rv.Index(i).Interface(), needle)
			if eq {
				return true, nil
			}
		}
		return false, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return false, fmt.Errorf("%w: contains: map key must be string", ErrEvaluate)
		}
		key, ok := needle.(string)
		if !ok {
			return false, fmt.Errorf("%w: contains: map lookup needs string needle, got %T", ErrEvaluate, needle)
		}
		return rv.MapIndex(reflect.ValueOf(key)).IsValid(), nil
	}
	return false, fmt.Errorf("%w: contains: unsupported haystack type %T", ErrEvaluate, haystack)
}

func builtinHas(m, key any) (bool, error) {
	if m == nil {
		return false, nil
	}
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map {
		return false, fmt.Errorf("%w: has: expected map, got %T", ErrEvaluate, m)
	}
	if rv.Type().Key().Kind() != reflect.String {
		return false, fmt.Errorf("%w: has: map key must be string", ErrEvaluate)
	}
	k, ok := key.(string)
	if !ok {
		return false, fmt.Errorf("%w: has: key must be string, got %T", ErrEvaluate, key)
	}
	return rv.MapIndex(reflect.ValueOf(k)).IsValid(), nil
}

func builtinKeys(m any) ([]any, error) {
	if m == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("%w: keys: expected map with string keys, got %T", ErrEvaluate, m)
	}
	mapKeys := rv.MapKeys()
	strs := make([]string, len(mapKeys))
	for i, k := range mapKeys {
		strs[i] = k.String()
	}
	sort.Strings(strs)
	out := make([]any, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out, nil
}
