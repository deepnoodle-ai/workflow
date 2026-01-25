package sprites

import (
	"os"
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
	"github.com/deepnoodle-ai/workflow/domain"
)

func TestNewEnvironment_RequiredOptions(t *testing.T) {
	// Missing token
	_, err := NewEnvironment(EnvironmentOptions{
		StoreDSN: "postgres://localhost/test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "token")

	// Missing store DSN
	_, err = NewEnvironment(EnvironmentOptions{
		Token: "test-token",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "store DSN")

	// Valid options
	env, err := NewEnvironment(EnvironmentOptions{
		Token:    "test-token",
		StoreDSN: "postgres://localhost/test",
	})
	assert.NoError(t, err)
	assert.NotNil(t, env)
}

func TestNewEnvironment_Defaults(t *testing.T) {
	env, err := NewEnvironment(EnvironmentOptions{
		Token:    "test-token",
		StoreDSN: "postgres://localhost/test",
	})
	assert.NoError(t, err)

	// Verify defaults
	assert.Equal(t, env.workerCommand, "/app/worker")
	assert.Equal(t, env.spritePrefix, "workflow-worker-")
	assert.False(t, env.cleanup)
}

func TestNewEnvironment_CustomOptions(t *testing.T) {
	env, err := NewEnvironment(EnvironmentOptions{
		Token:          "test-token",
		StoreDSN:       "postgres://localhost/test",
		WorkerCommand:  "/custom/worker",
		SpritePrefix:   "my-prefix-",
		CleanupSprites: true,
	})
	assert.NoError(t, err)

	assert.Equal(t, env.workerCommand, "/custom/worker")
	assert.Equal(t, env.spritePrefix, "my-prefix-")
	assert.True(t, env.cleanup)
}

func TestEnvironment_Mode(t *testing.T) {
	env, err := NewEnvironment(EnvironmentOptions{
		Token:    "test-token",
		StoreDSN: "postgres://localhost/test",
	})
	assert.NoError(t, err)

	assert.Equal(t, env.Mode(), domain.EnvironmentModeDispatch)
}

func TestEnvironment_ImplementsDispatchEnvironment(t *testing.T) {
	var _ domain.DispatchEnvironment = (*Environment)(nil)
}

// Integration tests that require SPRITE_API_TOKEN
func TestEnvironment_Integration(t *testing.T) {
	token := os.Getenv("SPRITE_API_TOKEN")
	if token == "" {
		t.Skip("SPRITE_API_TOKEN not set, skipping integration tests")
	}

	// Note: These tests require a real Sprites API token and will create/delete sprites.
	// They are primarily for manual testing during development.

	t.Run("CreateAndDeleteSprite", func(t *testing.T) {
		t.Skip("Enable for manual testing")

		// This test would:
		// 1. Create an Environment
		// 2. Call Dispatch()
		// 3. Verify the sprite was created
		// 4. Clean up
	})
}
