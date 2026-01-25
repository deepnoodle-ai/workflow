package workflow

import (
	"context"

	"github.com/deepnoodle-ai/workflow/domain"
)

// EnvironmentMode indicates how an environment runs workflows.
type EnvironmentMode = domain.EnvironmentMode

const (
	// EnvironmentModeBlocking runs workflows in-process, blocking until completion.
	EnvironmentModeBlocking = domain.EnvironmentModeBlocking
	// EnvironmentModeDispatch hands off to remote workers and returns immediately.
	EnvironmentModeDispatch = domain.EnvironmentModeDispatch
)

// ExecutionEnvironment determines where and how workflows run.
type ExecutionEnvironment = domain.ExecutionEnvironment

// DispatchEnvironment hands off to remote workers.
type DispatchEnvironment = domain.DispatchEnvironment

// BlockingEnvironment runs workflows in-process.
type BlockingEnvironment interface {
	ExecutionEnvironment

	// Run executes the workflow. Blocks until completion.
	// The engine is responsible for creating and configuring the Execution.
	Run(ctx context.Context, exec *Execution) error
}
