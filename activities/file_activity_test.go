package activities

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileActivity(t *testing.T) {
	activity := NewFileActivity()
	require.Equal(t, "file", activity.Name())

	t.Run("empty path", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "path cannot be empty")
	})

	t.Run("write and read", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "test.txt")
		ctx := newTestContext()

		result, err := activity.Execute(ctx, map[string]any{"operation": "write", "path": fp, "content": "hello world"})
		require.NoError(t, err)
		require.Equal(t, true, result)

		result, err = activity.Execute(ctx, map[string]any{"path": fp})
		require.NoError(t, err)
		require.Equal(t, "hello world", result)
	})

	t.Run("write with permissions", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "perm.txt")
		ctx := newTestContext()

		_, err := activity.Execute(ctx, map[string]any{"operation": "write", "path": fp, "content": "test", "permissions": "0644"})
		require.NoError(t, err)

		info, err := os.Stat(fp)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0644), info.Mode().Perm())
	})

	t.Run("write with create_dirs", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "sub", "dir", "test.txt")
		ctx := newTestContext()

		_, err := activity.Execute(ctx, map[string]any{"operation": "write", "path": fp, "content": "nested", "create_dirs": true})
		require.NoError(t, err)

		data, err := os.ReadFile(fp)
		require.NoError(t, err)
		require.Equal(t, "nested", string(data))
	})

	t.Run("write with decimal permissions", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "dec.txt")
		ctx := newTestContext()

		_, err := activity.Execute(ctx, map[string]any{"operation": "write", "path": fp, "content": "test", "permissions": "420"})
		require.NoError(t, err)
	})

	t.Run("append", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "append.txt")
		ctx := newTestContext()

		_, err := activity.Execute(ctx, map[string]any{"operation": "write", "path": fp, "content": "first"})
		require.NoError(t, err)

		result, err := activity.Execute(ctx, map[string]any{"operation": "append", "path": fp, "content": " second"})
		require.NoError(t, err)
		require.Equal(t, true, result)

		data, err := os.ReadFile(fp)
		require.NoError(t, err)
		require.Equal(t, "first second", string(data))
	})

	t.Run("delete", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "delete.txt")
		require.NoError(t, os.WriteFile(fp, []byte("delete me"), 0644))
		ctx := newTestContext()

		result, err := activity.Execute(ctx, map[string]any{"operation": "delete", "path": fp})
		require.NoError(t, err)
		require.Equal(t, true, result)
		_, err = os.Stat(fp)
		require.True(t, os.IsNotExist(err))
	})

	t.Run("exists", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "exists.txt")
		require.NoError(t, os.WriteFile(fp, []byte("here"), 0644))
		ctx := newTestContext()

		result, err := activity.Execute(ctx, map[string]any{"operation": "exists", "path": fp})
		require.NoError(t, err)
		require.Equal(t, true, result)

		result, err = activity.Execute(ctx, map[string]any{"operation": "exists", "path": filepath.Join(dir, "nope.txt")})
		require.NoError(t, err)
		require.Equal(t, false, result)
	})

	t.Run("mkdir", func(t *testing.T) {
		dir := t.TempDir()
		newDir := filepath.Join(dir, "newdir")
		ctx := newTestContext()

		result, err := activity.Execute(ctx, map[string]any{"operation": "mkdir", "path": newDir})
		require.NoError(t, err)
		require.Equal(t, true, result)
		info, err := os.Stat(newDir)
		require.NoError(t, err)
		require.True(t, info.IsDir())
	})

	t.Run("mkdir with create_dirs", func(t *testing.T) {
		dir := t.TempDir()
		newDir := filepath.Join(dir, "a", "b", "c")
		ctx := newTestContext()

		result, err := activity.Execute(ctx, map[string]any{"operation": "mkdir", "path": newDir, "create_dirs": true})
		require.NoError(t, err)
		require.Equal(t, true, result)
	})

	t.Run("list", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644))
		require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0755))
		ctx := newTestContext()

		result, err := activity.Execute(ctx, map[string]any{"operation": "list", "path": dir})
		require.NoError(t, err)
		files := result.([]string)
		require.Contains(t, files, "a.txt")
		require.Contains(t, files, "b.txt")
		require.Contains(t, files, "subdir/")
	})

	t.Run("unsupported operation", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{"operation": "unknown", "path": "/tmp/whatever"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported operation")
	})

	t.Run("read nonexistent", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{"path": "/tmp/definitely_does_not_exist_xyz.txt"})
		require.Error(t, err)
	})
}
