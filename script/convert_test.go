package script

import (
	"testing"
	"time"

	"github.com/risor-io/risor/object"
	"github.com/stretchr/testify/require"
)

func TestConvertRisorValueToGo(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		obj := object.NewString("hello")
		result := ConvertRisorValueToGo(obj)
		require.Equal(t, "hello", result)
	})

	t.Run("int", func(t *testing.T) {
		obj := object.NewInt(42)
		result := ConvertRisorValueToGo(obj)
		require.Equal(t, int64(42), result)
	})

	t.Run("float", func(t *testing.T) {
		obj := object.NewFloat(3.14)
		result := ConvertRisorValueToGo(obj)
		require.Equal(t, 3.14, result)
	})

	t.Run("bool true", func(t *testing.T) {
		obj := object.True
		result := ConvertRisorValueToGo(obj)
		require.Equal(t, true, result)
	})

	t.Run("bool false", func(t *testing.T) {
		obj := object.False
		result := ConvertRisorValueToGo(obj)
		require.Equal(t, false, result)
	})

	t.Run("nil", func(t *testing.T) {
		obj := object.Nil
		result := ConvertRisorValueToGo(obj)
		require.Nil(t, result)
	})

	t.Run("time", func(t *testing.T) {
		now := time.Now()
		obj := object.NewTime(now)
		result := ConvertRisorValueToGo(obj)
		require.Equal(t, now, result)
	})

	t.Run("list", func(t *testing.T) {
		obj := object.NewList([]object.Object{
			object.NewString("a"),
			object.NewInt(1),
			object.NewFloat(2.5),
		})
		result := ConvertRisorValueToGo(obj)
		arr, ok := result.([]interface{})
		require.True(t, ok)
		require.Equal(t, "a", arr[0])
		require.Equal(t, int64(1), arr[1])
		require.Equal(t, 2.5, arr[2])
	})

	t.Run("map", func(t *testing.T) {
		obj := object.NewMap(map[string]object.Object{
			"key": object.NewString("value"),
			"num": object.NewInt(42),
		})
		result := ConvertRisorValueToGo(obj)
		m, ok := result.(map[string]interface{})
		require.True(t, ok)
		require.Equal(t, "value", m["key"])
		require.Equal(t, int64(42), m["num"])
	})

	t.Run("set", func(t *testing.T) {
		obj := object.NewSet([]object.Object{
			object.NewString("a"),
			object.NewString("b"),
		})
		result := ConvertRisorValueToGo(obj)
		arr, ok := result.([]interface{})
		require.True(t, ok)
		require.Len(t, arr, 2)
	})
}

func TestConvertRisorValueToBool(t *testing.T) {
	t.Run("bool true", func(t *testing.T) {
		require.True(t, ConvertRisorValueToBool(object.True))
	})
	t.Run("bool false", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.False))
	})
	t.Run("int nonzero", func(t *testing.T) {
		require.True(t, ConvertRisorValueToBool(object.NewInt(1)))
	})
	t.Run("int zero", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewInt(0)))
	})
	t.Run("float nonzero", func(t *testing.T) {
		require.True(t, ConvertRisorValueToBool(object.NewFloat(0.1)))
	})
	t.Run("float zero", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewFloat(0.0)))
	})
	t.Run("string nonempty", func(t *testing.T) {
		require.True(t, ConvertRisorValueToBool(object.NewString("hello")))
	})
	t.Run("string empty", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewString("")))
	})
	t.Run("string false", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewString("false")))
	})
	t.Run("string FALSE", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewString("FALSE")))
	})
	t.Run("list nonempty", func(t *testing.T) {
		require.True(t, ConvertRisorValueToBool(object.NewList([]object.Object{object.NewInt(1)})))
	})
	t.Run("list empty", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewList([]object.Object{})))
	})
	t.Run("map nonempty", func(t *testing.T) {
		require.True(t, ConvertRisorValueToBool(object.NewMap(map[string]object.Object{"a": object.NewInt(1)})))
	})
	t.Run("map empty", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.NewMap(map[string]object.Object{})))
	})
	t.Run("nil", func(t *testing.T) {
		require.False(t, ConvertRisorValueToBool(object.Nil))
	})
}

func TestConvertValueToBool(t *testing.T) {
	// Go native types
	require.True(t, ConvertValueToBool(true))
	require.False(t, ConvertValueToBool(false))
	require.True(t, ConvertValueToBool(1))
	require.False(t, ConvertValueToBool(0))
	require.True(t, ConvertValueToBool(int8(1)))
	require.False(t, ConvertValueToBool(int8(0)))
	require.True(t, ConvertValueToBool(int16(1)))
	require.False(t, ConvertValueToBool(int16(0)))
	require.True(t, ConvertValueToBool(int32(1)))
	require.False(t, ConvertValueToBool(int32(0)))
	require.True(t, ConvertValueToBool(int64(1)))
	require.False(t, ConvertValueToBool(int64(0)))
	require.True(t, ConvertValueToBool(uint(1)))
	require.False(t, ConvertValueToBool(uint(0)))
	require.True(t, ConvertValueToBool(uint8(1)))
	require.False(t, ConvertValueToBool(uint8(0)))
	require.True(t, ConvertValueToBool(uint16(1)))
	require.False(t, ConvertValueToBool(uint16(0)))
	require.True(t, ConvertValueToBool(uint32(1)))
	require.False(t, ConvertValueToBool(uint32(0)))
	require.True(t, ConvertValueToBool(uint64(1)))
	require.False(t, ConvertValueToBool(uint64(0)))
	require.True(t, ConvertValueToBool(float32(0.1)))
	require.False(t, ConvertValueToBool(float32(0.0)))
	require.True(t, ConvertValueToBool(3.14))
	require.False(t, ConvertValueToBool(0.0))
	require.True(t, ConvertValueToBool("hello"))
	require.False(t, ConvertValueToBool(""))
	require.False(t, ConvertValueToBool("false"))
	require.True(t, ConvertValueToBool([]any{1}))
	require.False(t, ConvertValueToBool([]any{}))
	require.True(t, ConvertValueToBool(map[string]any{"a": 1}))
	require.False(t, ConvertValueToBool(map[string]any{}))
	require.False(t, ConvertValueToBool(nil))

	// Risor objects via the interface
	require.True(t, ConvertValueToBool(object.NewInt(1)))
	require.False(t, ConvertValueToBool(object.NewInt(0)))

	// Unknown type
	type custom struct{}
	require.True(t, ConvertValueToBool(custom{}))
}

func TestConvertEachValue(t *testing.T) {
	t.Run("go slice of any", func(t *testing.T) {
		result, err := ConvertEachValue([]any{1, "two", 3.0})
		require.NoError(t, err)
		require.Equal(t, []any{1, "two", 3.0}, result)
	})

	t.Run("go slice of strings", func(t *testing.T) {
		result, err := ConvertEachValue([]string{"a", "b", "c"})
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b", "c"}, result)
	})

	t.Run("go slice of ints", func(t *testing.T) {
		result, err := ConvertEachValue([]int{1, 2, 3})
		require.NoError(t, err)
		require.Equal(t, []any{1, 2, 3}, result)
	})

	t.Run("go slice of floats", func(t *testing.T) {
		result, err := ConvertEachValue([]float64{1.1, 2.2})
		require.NoError(t, err)
		require.Equal(t, []any{1.1, 2.2}, result)
	})

	t.Run("go map", func(t *testing.T) {
		result, err := ConvertEachValue(map[string]any{"a": 1})
		require.NoError(t, err)
		require.Len(t, result, 1)
		item, ok := result[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "a", item["key"])
		require.Equal(t, 1, item["value"])
	})

	t.Run("single scalar values", func(t *testing.T) {
		for _, v := range []any{"hello", 42, int64(7), true, 3.14, float32(1.0)} {
			result, err := ConvertEachValue(v)
			require.NoError(t, err)
			require.Len(t, result, 1)
			require.Equal(t, v, result[0])
		}
	})

	t.Run("unsupported type", func(t *testing.T) {
		type custom struct{}
		_, err := ConvertEachValue(custom{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported value type for 'each'")
	})

	// Risor objects
	t.Run("risor string", func(t *testing.T) {
		result, err := ConvertEachValue(object.NewString("test"))
		require.NoError(t, err)
		require.Equal(t, []any{"test"}, result)
	})

	t.Run("risor int", func(t *testing.T) {
		result, err := ConvertEachValue(object.NewInt(42))
		require.NoError(t, err)
		require.Equal(t, []any{int64(42)}, result)
	})

	t.Run("risor float", func(t *testing.T) {
		result, err := ConvertEachValue(object.NewFloat(3.14))
		require.NoError(t, err)
		require.Equal(t, []any{3.14}, result)
	})

	t.Run("risor bool", func(t *testing.T) {
		result, err := ConvertEachValue(object.True)
		require.NoError(t, err)
		require.Equal(t, []any{true}, result)
	})

	t.Run("risor time", func(t *testing.T) {
		now := time.Now()
		result, err := ConvertEachValue(object.NewTime(now))
		require.NoError(t, err)
		require.Equal(t, []any{now}, result)
	})

	t.Run("risor list", func(t *testing.T) {
		obj := object.NewList([]object.Object{
			object.NewString("a"),
			object.NewString("b"),
		})
		result, err := ConvertEachValue(obj)
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b"}, result)
	})

	t.Run("risor set", func(t *testing.T) {
		obj := object.NewSet([]object.Object{
			object.NewInt(1),
			object.NewInt(2),
		})
		result, err := ConvertEachValue(obj)
		require.NoError(t, err)
		require.Len(t, result, 2)
	})

	t.Run("risor map", func(t *testing.T) {
		obj := object.NewMap(map[string]object.Object{
			"key": object.NewString("value"),
		})
		result, err := ConvertEachValue(obj)
		require.NoError(t, err)
		require.Len(t, result, 1)
	})

	t.Run("unsupported risor type", func(t *testing.T) {
		_, err := ConvertEachValue(object.Nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported risor result type for 'each'")
	})
}

func TestGetSafeGlobals(t *testing.T) {
	globals := GetSafeGlobals()
	require.True(t, globals["json"])
	require.True(t, globals["strings"])
	require.True(t, globals["math"])
	require.True(t, globals["len"])
	require.True(t, globals["fmt"])
	require.False(t, globals["os"])
	require.False(t, globals["exec"])
	require.Greater(t, len(globals), 20)
}
