package workflow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalEnvironment_Mode(t *testing.T) {
	env := NewLocalEnvironment()
	require.Equal(t, EnvironmentModeBlocking, env.Mode())
}

func TestLocalEnvironment_ImplementsBlockingEnvironment(t *testing.T) {
	env := NewLocalEnvironment()

	// Verify it implements BlockingEnvironment
	var _ BlockingEnvironment = env
	require.NotNil(t, env)
}
