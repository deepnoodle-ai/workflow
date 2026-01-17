package workflow

import "context"

// EnvironmentMode indicates how an environment runs workflows.
type EnvironmentMode int

const (
	// EnvironmentModeBlocking runs workflows in-process, blocking until completion.
	EnvironmentModeBlocking EnvironmentMode = iota
	// EnvironmentModeDispatch hands off to remote workers and returns immediately.
	EnvironmentModeDispatch
)

// ExecutionEnvironment determines where and how workflows run.
type ExecutionEnvironment interface {
	// Mode returns whether this environment blocks or dispatches.
	Mode() EnvironmentMode
}

// BlockingEnvironment runs workflows in-process.
type BlockingEnvironment interface {
	ExecutionEnvironment

	// Run executes the workflow. Blocks until completion.
	// The engine is responsible for creating and configuring the Execution.
	Run(ctx context.Context, exec *Execution) error
}

// DispatchEnvironment hands off to remote workers.
type DispatchEnvironment interface {
	ExecutionEnvironment

	// Dispatch triggers remote execution. Returns once handoff succeeds.
	// The remote worker is responsible for claiming, running, and completing.
	Dispatch(ctx context.Context, executionID string, attempt int) error
}
