package goexpr

import (
	"context"
	"fmt"
	"math"
	"reflect"

	"github.com/deepnoodle-ai/workflow/script"
)

// ctxType is the reflect.Type of context.Context; cached at package init so
// buildCallArgs doesn't allocate a fresh one on every call.
var ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()

// callFunction invokes fn with args using reflection. Argument values are
// converted to the function's declared parameter types where possible.
// Return signatures supported: (), (T), (T, error). If the function's
// first parameter is context.Context, ctx is passed through automatically
// and is not counted against the caller-supplied argument list.
func callFunction(ctx context.Context, name string, fn any, args []any) (any, error) {
	fv := reflect.ValueOf(fn)
	if fv.Kind() != reflect.Func {
		return nil, fmt.Errorf("%w: %q is not a function (got %T)", ErrEvaluate, name, fn)
	}
	if fv.IsNil() {
		return nil, fmt.Errorf("%w: %q is a nil function value", ErrEvaluate, name)
	}
	ft := fv.Type()

	in, err := buildCallArgs(ctx, name, ft, args)
	if err != nil {
		return nil, err
	}

	out := fv.Call(in)
	switch ft.NumOut() {
	case 0:
		return nil, nil
	case 1:
		return out[0].Interface(), nil
	case 2:
		errType := reflect.TypeOf((*error)(nil)).Elem()
		if !ft.Out(1).Implements(errType) {
			return nil, fmt.Errorf("%w: %q: second return must be error, got %v", ErrEvaluate, name, ft.Out(1))
		}
		if !out[1].IsNil() {
			return nil, out[1].Interface().(error)
		}
		return out[0].Interface(), nil
	}
	return nil, fmt.Errorf("%w: %q returns %d values (expected 0, 1, or (T, error))", ErrEvaluate, name, ft.NumOut())
}

func buildCallArgs(ctx context.Context, name string, ft reflect.Type, args []any) ([]reflect.Value, error) {
	// Detect context.Context as the first declared parameter. If present,
	// ctx is injected automatically and the remaining parameters are
	// matched against the caller-supplied args.
	injectCtx := ft.NumIn() > 0 && ft.In(0) == ctxType
	paramOffset := 0
	if injectCtx {
		paramOffset = 1
	}
	declaredIn := ft.NumIn() - paramOffset

	if ft.IsVariadic() {
		fixed := declaredIn - 1
		if len(args) < fixed {
			return nil, fmt.Errorf("%w: %q expects at least %d args, got %d", ErrEvaluate, name, fixed, len(args))
		}
		in := make([]reflect.Value, paramOffset+len(args))
		if injectCtx {
			in[0] = reflect.ValueOf(ctx)
		}
		for i := 0; i < fixed; i++ {
			v, err := convertArg(name, i, args[i], ft.In(paramOffset+i))
			if err != nil {
				return nil, err
			}
			in[paramOffset+i] = v
		}
		variadicElem := ft.In(paramOffset + fixed).Elem()
		for i := fixed; i < len(args); i++ {
			v, err := convertArg(name, i, args[i], variadicElem)
			if err != nil {
				return nil, err
			}
			in[paramOffset+i] = v
		}
		return in, nil
	}
	if len(args) != declaredIn {
		return nil, fmt.Errorf("%w: %q expects %d args, got %d", ErrEvaluate, name, declaredIn, len(args))
	}
	in := make([]reflect.Value, paramOffset+len(args))
	if injectCtx {
		in[0] = reflect.ValueOf(ctx)
	}
	for i, a := range args {
		v, err := convertArg(name, i, a, ft.In(paramOffset+i))
		if err != nil {
			return nil, err
		}
		in[paramOffset+i] = v
	}
	return in, nil
}

func convertArg(name string, idx int, value any, want reflect.Type) (reflect.Value, error) {
	if value == nil {
		switch want.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			return reflect.Zero(want), nil
		}
		return reflect.Value{}, fmt.Errorf("%w: %q arg %d: cannot pass nil as %v", ErrEvaluate, name, idx, want)
	}
	rv := reflect.ValueOf(value)
	if rv.Type().AssignableTo(want) {
		return rv, nil
	}
	// Numeric coercion: goexpr stores ints as int64 and floats as float64,
	// but callers often declare fn(int) / fn(float32) / etc. We range-check
	// every integer narrowing so silent wraparound can't hide bugs.
	if isNumericKind(want.Kind()) && isNumericKind(rv.Kind()) {
		converted, err := safeNumericConvert(rv, want)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("%w: %q arg %d: %v", ErrEvaluate, name, idx, err)
		}
		return converted, nil
	}
	if want.Kind() == reflect.Interface && rv.Type().Implements(want) {
		return rv, nil
	}
	if rv.Type().ConvertibleTo(want) {
		return rv.Convert(want), nil
	}
	return reflect.Value{}, fmt.Errorf("%w: %q arg %d: cannot use %T as %v", ErrEvaluate, name, idx, value, want)
}

func isNumericKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// safeNumericConvert performs a numeric conversion that rejects any value
// whose magnitude cannot be represented in the target type. Float→int
// truncates toward zero (matching Go's built-in conversion), but only if
// the truncated integer still fits in the target kind. float64→float32
// converts even if precision is lost, since that is always expected.
func safeNumericConvert(rv reflect.Value, want reflect.Type) (reflect.Value, error) {
	k := want.Kind()
	// Float targets
	if k == reflect.Float32 || k == reflect.Float64 {
		if rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64 {
			return rv.Convert(want), nil
		}
		// int/uint → float
		return rv.Convert(want), nil
	}
	// Integer targets — compute the int64-equivalent of rv and bounds-check.
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return reflect.Value{}, fmt.Errorf("cannot convert %v to %v", f, want)
		}
		// Truncate toward zero, then bounds-check as if we had an int64.
		t := math.Trunc(f)
		if t > math.MaxInt64 || t < math.MinInt64 {
			return reflect.Value{}, fmt.Errorf("float %v out of range for %v", f, want)
		}
		return intToKind(int64(t), want)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > math.MaxInt64 && isSignedInt(k) {
			return reflect.Value{}, fmt.Errorf("uint %d overflows %v", u, want)
		}
		if isSignedInt(k) {
			return intToKind(int64(u), want)
		}
		return uintToKind(u, want)
	default:
		// Signed int sources.
		return intToKind(rv.Int(), want)
	}
}

func isSignedInt(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

func intToKind(v int64, want reflect.Type) (reflect.Value, error) {
	switch want.Kind() {
	case reflect.Int:
		// int is 64 bits on every supported platform; the int64 range covers it.
		return reflect.ValueOf(int(v)).Convert(want), nil
	case reflect.Int8:
		if v < math.MinInt8 || v > math.MaxInt8 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for int8", v)
		}
		return reflect.ValueOf(int8(v)).Convert(want), nil
	case reflect.Int16:
		if v < math.MinInt16 || v > math.MaxInt16 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for int16", v)
		}
		return reflect.ValueOf(int16(v)).Convert(want), nil
	case reflect.Int32:
		if v < math.MinInt32 || v > math.MaxInt32 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for int32", v)
		}
		return reflect.ValueOf(int32(v)).Convert(want), nil
	case reflect.Int64:
		return reflect.ValueOf(v).Convert(want), nil
	case reflect.Uint:
		if v < 0 {
			return reflect.Value{}, fmt.Errorf("negative int %d cannot become uint", v)
		}
		return reflect.ValueOf(uint(v)).Convert(want), nil
	case reflect.Uint8:
		if v < 0 || v > math.MaxUint8 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for uint8", v)
		}
		return reflect.ValueOf(uint8(v)).Convert(want), nil
	case reflect.Uint16:
		if v < 0 || v > math.MaxUint16 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for uint16", v)
		}
		return reflect.ValueOf(uint16(v)).Convert(want), nil
	case reflect.Uint32:
		if v < 0 || v > math.MaxUint32 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for uint32", v)
		}
		return reflect.ValueOf(uint32(v)).Convert(want), nil
	case reflect.Uint64:
		if v < 0 {
			return reflect.Value{}, fmt.Errorf("negative int %d cannot become uint64", v)
		}
		return reflect.ValueOf(uint64(v)).Convert(want), nil
	case reflect.Uintptr:
		if v < 0 {
			return reflect.Value{}, fmt.Errorf("negative int %d cannot become uintptr", v)
		}
		return reflect.ValueOf(uintptr(v)).Convert(want), nil
	default:
		// safeNumericConvert only calls this with numeric targets, and
		// every numeric integer kind is handled above.
		panic(fmt.Sprintf("goexpr: intToKind called with unsupported kind %v", want.Kind()))
	}
}

func uintToKind(v uint64, want reflect.Type) (reflect.Value, error) {
	switch want.Kind() {
	case reflect.Uint:
		return reflect.ValueOf(uint(v)).Convert(want), nil
	case reflect.Uint8:
		if v > math.MaxUint8 {
			return reflect.Value{}, fmt.Errorf("uint %d out of range for uint8", v)
		}
		return reflect.ValueOf(uint8(v)).Convert(want), nil
	case reflect.Uint16:
		if v > math.MaxUint16 {
			return reflect.Value{}, fmt.Errorf("uint %d out of range for uint16", v)
		}
		return reflect.ValueOf(uint16(v)).Convert(want), nil
	case reflect.Uint32:
		if v > math.MaxUint32 {
			return reflect.Value{}, fmt.Errorf("uint %d out of range for uint32", v)
		}
		return reflect.ValueOf(uint32(v)).Convert(want), nil
	case reflect.Uint64:
		return reflect.ValueOf(v).Convert(want), nil
	case reflect.Uintptr:
		return reflect.ValueOf(uintptr(v)).Convert(want), nil
	default:
		panic(fmt.Sprintf("goexpr: uintToKind called with unsupported kind %v", want.Kind()))
	}
}

// --- numeric / equality / truthiness helpers ---

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	}
	return 0, false
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	if i, ok := toInt64(v); ok {
		return float64(i), true
	}
	return 0, false
}

// looseEqual compares values across compatible numeric types and falls
// back to Go's native equality for everything else. Typed nils (e.g. a
// nil slice stored in an `any`) compare equal to the untyped `nil`
// literal, matching user expectation. The second return is false when
// the values are not comparable at all (e.g. slice == map).
func looseEqual(lhs, rhs any) (bool, bool) {
	if lhs == nil && rhs == nil {
		return true, true
	}
	if lhs == nil {
		return isNilValue(rhs), true
	}
	if rhs == nil {
		return isNilValue(lhs), true
	}
	if lf, lok := toFloat64(lhs); lok {
		if rf, rok := toFloat64(rhs); rok {
			return lf == rf, true
		}
	}
	lt := reflect.TypeOf(lhs)
	if !lt.Comparable() {
		return false, false
	}
	rt := reflect.TypeOf(rhs)
	if !rt.Comparable() {
		return false, false
	}
	if lt != rt {
		return false, true
	}
	return lhs == rhs, true
}

// isNilValue reports whether v is nil at the Go level, including typed
// nils for nilable reference kinds.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

func isTruthy(v any) bool { return script.IsTruthyValue(v) }
