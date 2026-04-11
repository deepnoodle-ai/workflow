package workflow

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// RunnerOption is a functional option for NewRunner.
type RunnerOption func(*runnerConfig)

type runnerConfig struct {
	logger         *slog.Logger
	defaultTimeout time.Duration
}

// WithRunnerLogger sets the Runner's structured logger. Defaults to a
// discard logger.
func WithRunnerLogger(l *slog.Logger) RunnerOption {
	return func(c *runnerConfig) { c.logger = l }
}

// WithDefaultTimeout sets the default per-execution timeout applied
// unless Run overrides it via WithRunTimeout. Zero means no timeout.
func WithDefaultTimeout(d time.Duration) RunnerOption {
	return func(c *runnerConfig) { c.defaultTimeout = d }
}

// RunOption is a functional option for a single Runner.Run call.
type RunOption func(*runConfig)

type runConfig struct {
	priorExecutionID string
	heartbeat        *HeartbeatConfig
	completionHook   CompletionHook
	timeout          time.Duration
	timeoutSet       bool
}

// WithResumeFrom triggers resume-or-run behavior. When set, the Runner
// attempts to resume from this execution's checkpoint before falling
// back to a fresh run. When empty, always starts fresh.
func WithResumeFrom(priorExecutionID string) RunOption {
	return func(c *runConfig) { c.priorExecutionID = priorExecutionID }
}

// WithHeartbeat installs a heartbeat config for this run. Optional.
func WithHeartbeat(h *HeartbeatConfig) RunOption {
	return func(c *runConfig) { c.heartbeat = h }
}

// WithCompletionHook installs a hook called after successful execution
// to produce follow-up workflow specs. Optional.
func WithCompletionHook(h CompletionHook) RunOption {
	return func(c *runConfig) { c.completionHook = h }
}

// WithRunTimeout overrides the Runner's default timeout for this run.
// A negative duration means no timeout.
func WithRunTimeout(d time.Duration) RunOption {
	return func(c *runConfig) {
		c.timeout = d
		c.timeoutSet = true
	}
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

// Runner manages the full lifecycle of workflow executions. It
// composes heartbeating, crash recovery, structured results, and
// completion hooks.
//
// Create a Runner once at startup and call Run for each workflow
// execution.
type Runner struct {
	logger         *slog.Logger
	defaultTimeout time.Duration
}

// NewRunner creates a Runner with the given options.
func NewRunner(opts ...RunnerOption) *Runner {
	cfg := &runnerConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Runner{
		logger:         cfg.logger,
		defaultTimeout: cfg.defaultTimeout,
	}
}

// Run executes a workflow with full lifecycle management. It:
//
//  1. Starts a heartbeat goroutine (if configured)
//  2. Applies a timeout (if configured)
//  3. Calls exec.Execute (with ResumeFrom if WithResumeFrom is set)
//  4. Stops the heartbeat and collects the result
//  5. Calls the completion hook (if configured and execution succeeded)
//  6. Returns the structured ExecutionResult
//
// The caller creates the Execution with its own options. The Runner
// manages lifecycle concerns on top of that.
//
// The error return indicates infrastructure failures (execution
// couldn't run). Workflow-level failures are in result.Error.
func (r *Runner) Run(ctx context.Context, exec *Execution, opts ...RunOption) (*ExecutionResult, error) {
	if exec == nil {
		return nil, ErrNilExecution
	}

	cfg := &runConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// Apply timeout.
	timeout := r.resolveTimeout(cfg)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Create a cancellable context for the execution. The heartbeat
	// cancels this context on lease loss.
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	// Start heartbeat.
	if cfg.heartbeat != nil {
		if cfg.heartbeat.Interval <= 0 {
			return nil, ErrInvalidHeartbeatInterval
		}
		if cfg.heartbeat.Func == nil {
			return nil, ErrNilHeartbeatFunc
		}
		stopHeartbeat := r.startHeartbeat(execCtx, execCancel, cfg.heartbeat)
		defer stopHeartbeat()
	}

	// Run (optionally resuming from a prior checkpoint).
	var execOpts []ExecuteOption
	if cfg.priorExecutionID != "" {
		execOpts = append(execOpts, ResumeFrom(cfg.priorExecutionID))
	}
	result, err := exec.Execute(execCtx, execOpts...)
	if err != nil {
		return nil, err
	}

	// Completion hook.
	if result.Completed() && cfg.completionHook != nil {
		followUps, hookErr := cfg.completionHook(ctx, result)
		// Attach follow-ups even when the hook returns an error: a
		// partial list is still useful for diagnosis and the error is
		// logged separately.
		result.FollowUps = followUps
		if hookErr != nil {
			r.logger.Error("completion hook failed",
				"execution_id", exec.ID(),
				"error", hookErr,
			)
		}
	}

	return result, nil
}

// startHeartbeat launches a goroutine that calls the heartbeat
// function on the configured interval. Returns a stop function that
// blocks until the goroutine exits.
//
// execCancel is called on heartbeat failure to cancel the execution context.
func (r *Runner) startHeartbeat(ctx context.Context, execCancel context.CancelFunc, cfg *HeartbeatConfig) func() {
	// A separate cancel to stop the heartbeat goroutine itself on normal completion.
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

func (r *Runner) resolveTimeout(cfg *runConfig) time.Duration {
	if !cfg.timeoutSet {
		return r.defaultTimeout
	}
	if cfg.timeout < 0 {
		return 0 // explicit no-timeout
	}
	return cfg.timeout
}
