// Package main implements a worker binary for remote workflow execution.
// This worker is designed to run in a Sprite (or any remote environment) and:
// 1. Poll for available tasks
// 2. Claim and execute tasks with heartbeating
// 3. Complete tasks with results
//
// The worker supports two connection modes:
// - HTTP mode: Set ORCHESTRATOR_URL to connect via HTTP to an orchestrator
// - Direct mode: Set WORKFLOW_STORE_DSN to connect directly to PostgreSQL
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deepnoodle-ai/workflow"
	workflowhttp "github.com/deepnoodle-ai/workflow/internal/http"
	"github.com/deepnoodle-ai/workflow/internal/postgres"
	"github.com/deepnoodle-ai/wonton/cli"
	"github.com/deepnoodle-ai/wonton/env"
	"github.com/deepnoodle-ai/wonton/retry"
)

// Config holds all worker configuration, loaded from environment variables.
type Config struct {
	// OrchestratorURL is the HTTP URL for connecting to the orchestrator.
	// When set, the worker uses HTTP instead of direct database access.
	OrchestratorURL string `env:"ORCHESTRATOR_URL"`

	// WorkerToken is the Bearer token for HTTP authentication.
	// Used when OrchestratorURL is set.
	WorkerToken string `env:"WORKER_TOKEN"`

	// StoreDSN is the PostgreSQL connection string for direct database access.
	// Used when OrchestratorURL is not set.
	StoreDSN string `env:"WORKFLOW_STORE_DSN"`

	// HeartbeatInterval is how often to send heartbeats.
	HeartbeatInterval time.Duration `env:"WORKFLOW_HEARTBEAT_INTERVAL" envDefault:"30s"`

	// PollInterval is how often to poll for new tasks.
	PollInterval time.Duration `env:"WORKFLOW_POLL_INTERVAL" envDefault:"1s"`

	// DBConnectRetries is the number of times to retry connecting to the database.
	DBConnectRetries int `env:"WORKFLOW_DB_CONNECT_RETRIES" envDefault:"5"`

	// DBRetryBackoff is the initial backoff duration for database retries.
	DBRetryBackoff time.Duration `env:"WORKFLOW_DB_RETRY_BACKOFF" envDefault:"1s"`
}

// TaskStore is a minimal interface for task operations needed by the worker.
type TaskStore interface {
	ClaimTask(ctx context.Context, workerID string) (*workflow.ClaimedTask, error)
	CompleteTask(ctx context.Context, taskID, workerID string, result *workflow.TaskResult) error
	HeartbeatTask(ctx context.Context, taskID, workerID string) error
}

func main() {
	app := cli.New("worker").
		Description("Workflow engine worker for remote task execution").
		Version("0.1.0")

	app.Command("run").
		Description("Run the worker to poll and execute tasks").
		Flags(
			cli.String("worker-id", "w").
				Help("Worker ID (defaults to hostname)"),
		).
		Run(runWorker)

	app.Command("once").
		Description("Claim and execute a single task, then exit").
		Flags(
			cli.String("worker-id", "w").
				Help("Worker ID (defaults to hostname)"),
		).
		Run(runOnce)

	if err := app.Execute(); err != nil {
		if cli.IsHelpRequested(err) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(cli.GetExitCode(err))
	}
}

func runWorker(ctx *cli.Context) error {
	cfg, workerID, store, cleanup, logger, err := setupWorker(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	// Setup context with signal handling
	goCtx, cancel := signal.NotifyContext(ctx.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("starting worker loop", "worker_id", workerID)

	// Worker loop - continuously poll for and execute tasks
	for {
		select {
		case <-goCtx.Done():
			logger.Info("worker shutting down")
			return nil
		default:
		}

		// Try to claim a task
		task, err := store.ClaimTask(goCtx, workerID)
		if err != nil {
			if goCtx.Err() != nil {
				return nil
			}
			logger.Warn("claim task error", "error", err)
			time.Sleep(cfg.PollInterval)
			continue
		}

		if task == nil {
			// No work available, poll again
			time.Sleep(cfg.PollInterval)
			continue
		}

		// Execute the task
		logger.Info("executing task", "task_id", task.ID, "step", task.StepName)
		executeTask(goCtx, store, task, workerID, cfg.HeartbeatInterval, logger)
	}
}

func runOnce(ctx *cli.Context) error {
	cfg, workerID, store, cleanup, logger, err := setupWorker(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	_ = cfg // unused in once mode

	// Setup context with signal handling
	goCtx, cancel := signal.NotifyContext(ctx.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("looking for a task", "worker_id", workerID)

	// Try to claim a task
	task, err := store.ClaimTask(goCtx, workerID)
	if err != nil {
		return fmt.Errorf("claim task: %w", err)
	}

	if task == nil {
		logger.Info("no tasks available")
		return nil
	}

	// Execute the task
	logger.Info("executing task", "task_id", task.ID, "step", task.StepName)
	executeTask(goCtx, store, task, workerID, cfg.HeartbeatInterval, logger)
	return nil
}

func setupWorker(ctx *cli.Context) (*Config, string, TaskStore, func(), *slog.Logger, error) {
	// Load configuration from environment
	cfgVal, err := env.Parse[Config]()
	if err != nil {
		return nil, "", nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	cfg := &cfgVal

	// Validate that at least one connection method is configured
	if cfg.OrchestratorURL == "" && cfg.StoreDSN == "" {
		return nil, "", nil, nil, nil, fmt.Errorf("either ORCHESTRATOR_URL or WORKFLOW_STORE_DSN must be set")
	}

	// Determine worker ID
	workerID := ctx.String("worker-id")
	if workerID == "" {
		hostname, err := os.Hostname()
		if err != nil {
			workerID = fmt.Sprintf("worker-%d", os.Getpid())
		} else {
			workerID = hostname
		}
	}

	// Setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// Create store based on configuration
	var store TaskStore
	var cleanup func()

	if cfg.OrchestratorURL != "" {
		// HTTP mode - connect to orchestrator
		logger.Info("using HTTP mode", "orchestrator_url", cfg.OrchestratorURL)
		client := workflowhttp.NewTaskClient(workflowhttp.TaskClientOptions{
			BaseURL: cfg.OrchestratorURL,
			Token:   cfg.WorkerToken,
			Config: workflow.StoreConfig{
				HeartbeatInterval: cfg.HeartbeatInterval,
			},
		})
		store = &httpTaskStore{client: client}
		cleanup = func() {} // no cleanup needed for HTTP client
	} else {
		// Direct mode - connect to PostgreSQL
		logger.Info("using direct PostgreSQL mode")
		db, err := connectWithRetry(ctx.Context(), cfg, logger)
		if err != nil {
			return nil, "", nil, nil, nil, err
		}
		pgStore := postgres.NewStore(postgres.StoreOptions{DB: db})
		store = pgStore
		cleanup = func() { db.Close() }
	}

	return cfg, workerID, store, cleanup, logger, nil
}

// httpTaskStore wraps TaskClient to implement TaskStore.
type httpTaskStore struct {
	client *workflowhttp.TaskClient
}

func (s *httpTaskStore) ClaimTask(ctx context.Context, workerID string) (*workflow.ClaimedTask, error) {
	return s.client.ClaimTask(ctx, workerID)
}

func (s *httpTaskStore) CompleteTask(ctx context.Context, taskID, workerID string, result *workflow.TaskResult) error {
	return s.client.CompleteTask(ctx, taskID, workerID, result)
}

func (s *httpTaskStore) HeartbeatTask(ctx context.Context, taskID, workerID string) error {
	return s.client.HeartbeatTask(ctx, taskID, workerID)
}

func executeTask(
	ctx context.Context,
	store TaskStore,
	task *workflow.ClaimedTask,
	workerID string,
	heartbeatInterval time.Duration,
	logger *slog.Logger,
) {
	// Start heartbeat goroutine
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()

	go heartbeatLoop(heartbeatCtx, store, task.ID, workerID, heartbeatInterval, logger)

	// Execute based on task spec type
	var result *workflow.TaskResult

	switch task.Spec.Type {
	case "inline":
		// Inline tasks should not reach remote workers
		result = &workflow.TaskResult{
			Success: false,
			Error:   "inline tasks cannot be executed by remote workers",
		}

	case "container":
		// TODO: Implement container execution
		result = &workflow.TaskResult{
			Success: false,
			Error:   "container execution not yet implemented",
		}

	case "process":
		// TODO: Implement process execution
		result = &workflow.TaskResult{
			Success: false,
			Error:   "process execution not yet implemented",
		}

	case "http":
		// TODO: Implement HTTP execution
		result = &workflow.TaskResult{
			Success: false,
			Error:   "http execution not yet implemented",
		}

	default:
		result = &workflow.TaskResult{
			Success: false,
			Error:   fmt.Sprintf("unknown task type: %s", task.Spec.Type),
		}
	}

	// Stop heartbeating before completing
	cancelHeartbeat()

	// Complete the task
	err := completeWithRetry(ctx, store, task.ID, workerID, result, logger)
	if err != nil {
		logger.Error("failed to complete task", "task_id", task.ID, "error", err)
	} else {
		logger.Info("task completed", "task_id", task.ID, "success", result.Success)
	}
}

// connectWithRetry connects to the database with exponential backoff retry.
func connectWithRetry(ctx context.Context, cfg *Config, logger *slog.Logger) (*sql.DB, error) {
	var db *sql.DB

	_, err := retry.Do(ctx, func() (*sql.DB, error) {
		logger.Debug("connecting to database")

		d, err := sql.Open("postgres", cfg.StoreDSN)
		if err != nil {
			return nil, fmt.Errorf("open database: %w", err)
		}

		// Verify connection
		if err := d.PingContext(ctx); err != nil {
			d.Close()
			return nil, fmt.Errorf("ping database: %w", err)
		}

		db = d
		return d, nil
	},
		retry.WithMaxAttempts(cfg.DBConnectRetries),
		retry.WithBackoff(cfg.DBRetryBackoff, 30*time.Second),
		retry.WithOnRetry(func(attempt int, err error, delay time.Duration) {
			logger.Warn("database connection failed, retrying",
				"attempt", attempt,
				"error", err,
				"retry_delay", delay)
		}),
	)

	if err != nil {
		return nil, fmt.Errorf("connect to database after %d attempts: %w", cfg.DBConnectRetries, err)
	}

	logger.Info("connected to database")
	return db, nil
}

// heartbeatLoop sends periodic heartbeats to the store.
func heartbeatLoop(
	ctx context.Context,
	store TaskStore,
	taskID string,
	workerID string,
	interval time.Duration,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Heartbeat with retry for transient failures
			err := retry.DoSimple(ctx, func() error {
				return store.HeartbeatTask(ctx, taskID, workerID)
			},
				retry.WithMaxAttempts(3),
				retry.WithBackoff(100*time.Millisecond, 1*time.Second),
			)
			if err != nil {
				logger.Warn("heartbeat failed", "task_id", taskID, "error", err)
			}
		}
	}
}

func completeWithRetry(
	ctx context.Context,
	store TaskStore,
	taskID string,
	workerID string,
	result *workflow.TaskResult,
	logger *slog.Logger,
) error {
	return retry.DoSimple(ctx, func() error {
		return store.CompleteTask(ctx, taskID, workerID, result)
	},
		retry.WithMaxAttempts(5),
		retry.WithBackoff(100*time.Millisecond, 5*time.Second),
		retry.WithOnRetry(func(attempt int, err error, delay time.Duration) {
			logger.Warn("complete task failed, retrying",
				"attempt", attempt,
				"error", err,
				"retry_delay", delay)
		}),
	)
}
