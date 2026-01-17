package workflow

import (
	"testing"

	"github.com/deepnoodle-ai/wonton/assert"
)

func TestLocalEnvironment_Mode(t *testing.T) {
	env := NewLocalEnvironment()
	assert.Equal(t, env.Mode(), EnvironmentModeBlocking)
}

func TestLocalEnvironment_ImplementsBlockingEnvironment(t *testing.T) {
	env := NewLocalEnvironment()

	// Verify it implements BlockingEnvironment
	var _ BlockingEnvironment = env
	assert.NotNil(t, env)
}
