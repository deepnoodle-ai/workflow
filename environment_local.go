package workflow

import "context"

// LocalEnvironment runs workflows in-process. The Engine manages the full lifecycle.
type LocalEnvironment struct{}

// NewLocalEnvironment creates a new local environment.
func NewLocalEnvironment() *LocalEnvironment {
	return &LocalEnvironment{}
}

// Mode returns EnvironmentModeBlocking since this runs workflows in-process.
func (e *LocalEnvironment) Mode() EnvironmentMode {
	return EnvironmentModeBlocking
}

// Run executes the workflow. Blocks until completion.
func (e *LocalEnvironment) Run(ctx context.Context, exec *Execution) error {
	return exec.Run(ctx)
}

// Ensure LocalEnvironment implements BlockingEnvironment
var _ BlockingEnvironment = (*LocalEnvironment)(nil)
