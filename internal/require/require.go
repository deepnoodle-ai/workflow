// Package require provides a minimal subset of the testify/require API so
// that tests in this module can run without a third-party dependency. It
// intentionally covers only the functions used by the workflow test suite.
package require

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

// TestingT is the interface common to *testing.T and *testing.B used by the
// helpers in this package.
type TestingT interface {
	Helper()
	Errorf(format string, args ...any)
	FailNow()
}

func NoError(t TestingT, err error, msgAndArgs ...any) {
	t.Helper()
	if err != nil {
		fail(t, fmt.Sprintf("unexpected error: %v", err), msgAndArgs)
	}
}

func Error(t TestingT, err error, msgAndArgs ...any) {
	t.Helper()
	if err == nil {
		fail(t, "expected an error, got nil", msgAndArgs)
	}
}

func ErrorIs(t TestingT, err, target error, msgAndArgs ...any) {
	t.Helper()
	if !errors.Is(err, target) {
		fail(t, fmt.Sprintf("expected errors.Is to match target %v, got %v", target, err), msgAndArgs)
	}
}

func Equal(t TestingT, expected, actual any, msgAndArgs ...any) {
	t.Helper()
	if !objectsAreEqual(expected, actual) {
		fail(t, fmt.Sprintf("not equal:\n  expected: %#v\n  actual:   %#v", expected, actual), msgAndArgs)
	}
}

func NotEqual(t TestingT, expected, actual any, msgAndArgs ...any) {
	t.Helper()
	if objectsAreEqual(expected, actual) {
		fail(t, fmt.Sprintf("expected values to differ, both were:\n  %#v", expected), msgAndArgs)
	}
}

// EqualValues compares two values after converting numeric types so that
// e.g. int(42) equals int64(42). For non-numeric values it falls back to
// Equal semantics.
func EqualValues(t TestingT, expected, actual any, msgAndArgs ...any) {
	t.Helper()
	if objectsAreEqualValues(expected, actual) {
		return
	}
	fail(t, fmt.Sprintf("not equal values:\n  expected: %#v\n  actual:   %#v", expected, actual), msgAndArgs)
}

func True(t TestingT, value bool, msgAndArgs ...any) {
	t.Helper()
	if !value {
		fail(t, "expected true, got false", msgAndArgs)
	}
}

func False(t TestingT, value bool, msgAndArgs ...any) {
	t.Helper()
	if value {
		fail(t, "expected false, got true", msgAndArgs)
	}
}

func Nil(t TestingT, value any, msgAndArgs ...any) {
	t.Helper()
	if !isNil(value) {
		fail(t, fmt.Sprintf("expected nil, got %#v", value), msgAndArgs)
	}
}

func NotNil(t TestingT, value any, msgAndArgs ...any) {
	t.Helper()
	if isNil(value) {
		fail(t, "expected non-nil value", msgAndArgs)
	}
}

func Contains(t TestingT, container, element any, msgAndArgs ...any) {
	t.Helper()
	ok, err := containsElement(container, element)
	if err != nil {
		fail(t, err.Error(), msgAndArgs)
		return
	}
	if !ok {
		fail(t, fmt.Sprintf("%#v does not contain %#v", container, element), msgAndArgs)
	}
}

func NotContains(t TestingT, container, element any, msgAndArgs ...any) {
	t.Helper()
	ok, err := containsElement(container, element)
	if err != nil {
		fail(t, err.Error(), msgAndArgs)
		return
	}
	if ok {
		fail(t, fmt.Sprintf("%#v should not contain %#v", container, element), msgAndArgs)
	}
}

func Len(t TestingT, object any, length int, msgAndArgs ...any) {
	t.Helper()
	n, ok := getLen(object)
	if !ok {
		fail(t, fmt.Sprintf("cannot get length of %T", object), msgAndArgs)
		return
	}
	if n != length {
		fail(t, fmt.Sprintf("expected length %d, got %d", length, n), msgAndArgs)
	}
}

// Empty asserts that an object is empty: nil, a zero-length collection,
// a zero-length string, or the zero value of its type.
func Empty(t TestingT, object any, msgAndArgs ...any) {
	t.Helper()
	if !isEmpty(object) {
		fail(t, fmt.Sprintf("expected empty, got %#v", object), msgAndArgs)
	}
}

func NotEmpty(t TestingT, object any, msgAndArgs ...any) {
	t.Helper()
	if isEmpty(object) {
		fail(t, fmt.Sprintf("expected non-empty, got %#v", object), msgAndArgs)
	}
}

// NotZero asserts that the value is not the zero value for its type.
func NotZero(t TestingT, value any, msgAndArgs ...any) {
	t.Helper()
	if value == nil || reflect.DeepEqual(value, reflect.Zero(reflect.TypeOf(value)).Interface()) {
		fail(t, fmt.Sprintf("expected non-zero value, got %#v", value), msgAndArgs)
	}
}

func Greater(t TestingT, a, b any, msgAndArgs ...any) {
	t.Helper()
	cmp, ok := compareOrdered(a, b)
	if !ok {
		fail(t, fmt.Sprintf("cannot compare %T and %T", a, b), msgAndArgs)
		return
	}
	if cmp <= 0 {
		fail(t, fmt.Sprintf("expected %v > %v", a, b), msgAndArgs)
	}
}

func GreaterOrEqual(t TestingT, a, b any, msgAndArgs ...any) {
	t.Helper()
	cmp, ok := compareOrdered(a, b)
	if !ok {
		fail(t, fmt.Sprintf("cannot compare %T and %T", a, b), msgAndArgs)
		return
	}
	if cmp < 0 {
		fail(t, fmt.Sprintf("expected %v >= %v", a, b), msgAndArgs)
	}
}

func LessOrEqual(t TestingT, a, b any, msgAndArgs ...any) {
	t.Helper()
	cmp, ok := compareOrdered(a, b)
	if !ok {
		fail(t, fmt.Sprintf("cannot compare %T and %T", a, b), msgAndArgs)
		return
	}
	if cmp > 0 {
		fail(t, fmt.Sprintf("expected %v <= %v", a, b), msgAndArgs)
	}
}

// Eventually polls condition every tick until it returns true or waitFor
// elapses. Fails the test if the deadline is hit first.
func Eventually(t TestingT, condition func() bool, waitFor, tick time.Duration, msgAndArgs ...any) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			fail(t, "condition never satisfied within deadline", msgAndArgs)
			return
		}
		time.Sleep(tick)
	}
}

func InDelta(t TestingT, expected, actual any, delta float64, msgAndArgs ...any) {
	t.Helper()
	ef, ok := toFloat64(expected)
	if !ok {
		fail(t, fmt.Sprintf("expected value %#v is not numeric", expected), msgAndArgs)
		return
	}
	af, ok := toFloat64(actual)
	if !ok {
		fail(t, fmt.Sprintf("actual value %#v is not numeric", actual), msgAndArgs)
		return
	}
	if math.IsNaN(ef) && math.IsNaN(af) {
		return
	}
	if math.IsNaN(ef) || math.IsNaN(af) {
		fail(t, fmt.Sprintf("NaN comparison: expected %v, actual %v", ef, af), msgAndArgs)
		return
	}
	if math.Abs(ef-af) > delta {
		fail(t, fmt.Sprintf("expected %v, got %v (delta %v)", ef, af, delta), msgAndArgs)
	}
}

func Panics(t TestingT, f func(), msgAndArgs ...any) {
	t.Helper()
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		f()
	}()
	if !panicked {
		fail(t, "expected function to panic", msgAndArgs)
	}
}

func fail(t TestingT, msg string, msgAndArgs []any) {
	t.Helper()
	if extra := formatMsgAndArgs(msgAndArgs); extra != "" {
		t.Errorf("%s\n%s", msg, extra)
	} else {
		t.Errorf("%s", msg)
	}
	t.FailNow()
}

func formatMsgAndArgs(msgAndArgs []any) string {
	if len(msgAndArgs) == 0 {
		return ""
	}
	if len(msgAndArgs) == 1 {
		return fmt.Sprint(msgAndArgs[0])
	}
	if format, ok := msgAndArgs[0].(string); ok {
		return fmt.Sprintf(format, msgAndArgs[1:]...)
	}
	return fmt.Sprint(msgAndArgs...)
}

func objectsAreEqual(expected, actual any) bool {
	if expected == nil || actual == nil {
		return expected == actual
	}
	if eb, ok := expected.([]byte); ok {
		ab, ok := actual.([]byte)
		if !ok {
			return false
		}
		if eb == nil || ab == nil {
			return eb == nil && ab == nil
		}
		return string(eb) == string(ab)
	}
	return reflect.DeepEqual(expected, actual)
}

// objectsAreEqualValues mirrors testify's EqualValues: numeric types are
// converted before comparison so int(42) equals int64(42).
func objectsAreEqualValues(expected, actual any) bool {
	if objectsAreEqual(expected, actual) {
		return true
	}
	ef, eok := toFloat64(expected)
	af, aok := toFloat64(actual)
	if eok && aok {
		return ef == af
	}
	// Fall back to reflect.Value.Convert if the kinds are assignable.
	ev := reflect.ValueOf(expected)
	av := reflect.ValueOf(actual)
	if ev.IsValid() && av.IsValid() && ev.Type().ConvertibleTo(av.Type()) {
		return reflect.DeepEqual(ev.Convert(av.Type()).Interface(), actual)
	}
	return false
}

func isNil(v any) bool {
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

func isEmpty(object any) bool {
	if object == nil {
		return true
	}
	rv := reflect.ValueOf(object)
	switch rv.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len() == 0
	case reflect.Pointer:
		if rv.IsNil() {
			return true
		}
		return isEmpty(rv.Elem().Interface())
	}
	// Zero value of its type counts as empty.
	return reflect.DeepEqual(object, reflect.Zero(rv.Type()).Interface())
}

func containsElement(container, element any) (bool, error) {
	if container == nil {
		return false, nil
	}
	rc := reflect.ValueOf(container)
	switch rc.Kind() {
	case reflect.String:
		sub := fmt.Sprint(element)
		return strings.Contains(rc.String(), sub), nil
	case reflect.Slice, reflect.Array:
		for i := 0; i < rc.Len(); i++ {
			if objectsAreEqual(rc.Index(i).Interface(), element) {
				return true, nil
			}
		}
		return false, nil
	case reflect.Map:
		for _, k := range rc.MapKeys() {
			if objectsAreEqual(k.Interface(), element) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("cannot apply Contains to %T", container)
}

func getLen(object any) (int, bool) {
	if object == nil {
		return 0, false
	}
	rv := reflect.ValueOf(object)
	switch rv.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len(), true
	}
	return 0, false
}

func toFloat64(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	}
	return 0, false
}

// compareOrdered returns -1/0/1 for a<b, a==b, a>b when both values are
// orderable numerics or strings of the same kind. Returns ok=false if the
// pair can't be compared.
func compareOrdered(a, b any) (int, bool) {
	if af, aok := toFloat64(a); aok {
		if bf, bok := toFloat64(b); bok {
			switch {
			case af < bf:
				return -1, true
			case af > bf:
				return 1, true
			default:
				return 0, true
			}
		}
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		switch {
		case as < bs:
			return -1, true
		case as > bs:
			return 1, true
		default:
			return 0, true
		}
	}
	return 0, false
}
