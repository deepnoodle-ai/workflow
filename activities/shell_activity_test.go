package activities

import (
	"path/filepath"
	"testing"

	"github.com/deepnoodle-ai/workflow/internal/require"
)

func TestShellActivity(t *testing.T) {
	activity := NewShellActivity()
	require.Equal(t, "shell", activity.Name())

	t.Run("empty command", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "command cannot be empty")
	})

	t.Run("echo", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"command": "echo", "args": []string{"hello"}})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, "hello", m["stdout"])
		require.Equal(t, 0, m["exit_code"])
		require.Equal(t, true, m["success"])
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"command": "sh", "args": []string{"-c", "exit 1"}})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, 1, m["exit_code"])
		require.Equal(t, false, m["success"])
	})

	t.Run("working directory", func(t *testing.T) {
		dir := t.TempDir()
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"command": "pwd", "working_dir": dir})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Contains(t, m["stdout"], filepath.Base(dir))
	})

	t.Run("environment variables", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{
			"command": "sh", "args": []string{"-c", "echo $MY_VAR"},
			"environment": map[string]string{"MY_VAR": "test_value"},
		})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, "test_value", m["stdout"])
	})

	t.Run("with timeout", func(t *testing.T) {
		ctx := newTestContext()
		result, err := activity.Execute(ctx, map[string]any{"command": "echo", "args": []string{"fast"}, "timeout": 5.0})
		require.NoError(t, err)
		m := result.(map[string]any)
		require.Equal(t, "fast", m["stdout"])
	})

	t.Run("command not found", func(t *testing.T) {
		ctx := newTestContext()
		_, err := activity.Execute(ctx, map[string]any{"command": "nonexistent_command_xyz"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to execute command")
	})
}
