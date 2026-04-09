package workflow

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// RunnerConfig holds reusable settings for a Runner. These are typically set
// once at application startup and shared across all executions.
type RunnerConfig struct {
	// Logger is the structured logger. Defaults to a discard logger.
	Logger *slog.Logger

	// DefaultTimeout is applied to every execution unless overridden in
	// RunOptions. Zero means no timeout.
	DefaultTimeout time.Duration
}

// RunOptions holds per-execution lifecycle settings that the Runner manages
// on top of a caller-created Execution.
type RunOptions struct {
	// PriorExecutionID triggers resume-or-run behavior. When set, the Runner
	// attempts to resume from this execution's checkpoint before falling back
	// to a fresh run. When empty, always starts fresh.
	PriorExecutionID string

	// Heartbeat configures periodic liveness checks. Optional.
	Heartbeat *HeartbeatConfig

	// CompletionHook is called after successful execution to produce follow-up
	// workflow specs. Optional.
	CompletionHook CompletionHook

	// Timeout overrides RunnerConfig.DefaultTimeout for this execution.
	// Zero means use the default; negative means no timeout.
	Timeout time.Duration
}

// HeartbeatConfig configures periodic liveness reporting.
type HeartbeatConfig struct {
	// Interval is how often the heartbeat function is called.
	Interval time.Duration

	// Func is called on each heartbeat tick. Return nil to indicate liveness.
	// Return an error to signal lease loss — the Runner cancels the execution
	// context, causing the workflow to abort.
	Func HeartbeatFunc
}

// HeartbeatFunc is called periodically to prove worker liveness.
// Return nil to continue. Return an error to abort the execution.
type HeartbeatFunc func(ctx context.Context) error

// Runner manages the full lifecycle of workflow executions. It composes
// heartbeating, crash recovery (RunOrResume), structured results, and
// completion hooks.
//
// Create a Runner once at startup and call Run for each workflow execution.
type Runner struct {
	logger         *slog.Logger
	defaultTimeout time.Duration
}

// NewRunner creates a Runner with the given configuration.
func NewRunner(cfg RunnerConfig) *Runner {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Runner{
		logger:         logger,
		defaultTimeout: cfg.DefaultTimeout,
	}
}

// Run executes a workflow with full lifecycle management. It:
//
//  1. Starts a heartbeat goroutine (if configured)
//  2. Applies a timeout (if configured)
//  3. Calls ExecuteOrResume or Execute (depending on PriorExecutionID)
//  4. Stops the heartbeat and collects the result
//  5. Calls the CompletionHook (if configured and execution succeeded)
//  6. Returns the structured ExecutionResult
//
// The caller creates the Execution with their own ExecutionOptions.
// The Runner manages lifecycle concerns on top of that.
//
// The error return indicates infrastructure failures (execution couldn't run).
// Workflow-level failures are in result.Error.
func (r *Runner) Run(
	ctx context.Context,
	exec *Execution,
	opts RunOptions,
) (*ExecutionResult, error) {
	if exec == nil {
		return nil, ErrNilExecution
	}

	// Apply timeout
	timeout := r.resolveTimeout(opts.Timeout)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Create a cancellable context for the execution. The heartbeat
	// cancels this context on lease loss.
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	// Start heartbeat
	if opts.Heartbeat != nil {
		if opts.Heartbeat.Interval <= 0 {
			return nil, ErrInvalidHeartbeatInterval
		}
		if opts.Heartbeat.Func == nil {
			return nil, ErrNilHeartbeatFunc
		}
		stopHeartbeat := r.startHeartbeat(execCtx, execCancel, opts.Heartbeat)
		defer stopHeartbeat()
	}

	// Run or resume
	var result *ExecutionResult
	var err error
	if opts.PriorExecutionID != "" {
		result, err = exec.ExecuteOrResume(execCtx, opts.PriorExecutionID)
	} else {
		result, err = exec.Execute(execCtx)
	}
	if err != nil {
		return nil, err
	}

	// Completion hook
	if result.Completed() && opts.CompletionHook != nil {
		followUps, hookErr := opts.CompletionHook(ctx, result)
		if hookErr != nil {
			r.logger.Error("completion hook failed",
				"execution_id", exec.ID(),
				"error", hookErr,
			)
		} else {
			result.FollowUps = followUps
		}
	}

	return result, nil
}

// startHeartbeat launches a goroutine that calls the heartbeat function on
// the configured interval. Returns a stop function that blocks until the
// goroutine exits.
//
// execCancel is called on heartbeat failure to cancel the execution context.
func (r *Runner) startHeartbeat(ctx context.Context, execCancel context.CancelFunc, cfg *HeartbeatConfig) func() {
	// A separate cancel to stop the heartbeat goroutine itself on normal completion
	hbCtx, hbCancel := context.WithCancel(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				if err := cfg.Func(hbCtx); err != nil {
					r.logger.Error("heartbeat failed, canceling execution", "error", err)
					execCancel() // cancel the execution, not just the heartbeat
					return
				}
			}
		}
	}()

	return func() {
		hbCancel() // stop the heartbeat goroutine
		<-done     // wait for it to exit
	}
}

func (r *Runner) resolveTimeout(override time.Duration) time.Duration {
	if override < 0 {
		return 0 // explicit no-timeout
	}
	if override > 0 {
		return override
	}
	return r.defaultTimeout
}
