// Package main implements a worker binary for remote workflow execution.
// This worker is designed to run in a Sprite (or any remote environment) and:
// 1. Claim the execution (with fencing via attempt)
// 2. Run the workflow with heartbeating
// 3. Complete/fail the execution (with fencing)
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deepnoodle-ai/workflow"
	"github.com/deepnoodle-ai/wonton/cli"
	"github.com/deepnoodle-ai/wonton/env"
	"github.com/deepnoodle-ai/wonton/retry"
)

// Config holds all worker configuration, loaded from environment variables.
type Config struct {
	// StoreDSN is the PostgreSQL connection string for the ExecutionStore.
	StoreDSN string `env:"WORKFLOW_STORE_DSN,required"`

	// HeartbeatInterval is how often to send heartbeats.
	HeartbeatInterval time.Duration `env:"WORKFLOW_HEARTBEAT_INTERVAL" envDefault:"30s"`

	// DBConnectRetries is the number of times to retry connecting to the database.
	DBConnectRetries int `env:"WORKFLOW_DB_CONNECT_RETRIES" envDefault:"5"`

	// DBRetryBackoff is the initial backoff duration for database retries.
	DBRetryBackoff time.Duration `env:"WORKFLOW_DB_RETRY_BACKOFF" envDefault:"1s"`
}

func main() {
	app := cli.New("worker").
		Description("Workflow engine worker for remote execution").
		Version("0.1.0")

	app.Command("run").
		Description("Run a workflow execution").
		Args("execution-id", "attempt").
		Flags(
			cli.String("worker-id", "w").
				Help("Worker ID (defaults to hostname)"),
		).
		Run(runExecution)

	if err := app.Execute(); err != nil {
		if cli.IsHelpRequested(err) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(cli.GetExitCode(err))
	}
}

func runExecution(ctx *cli.Context) error {
	// Parse positional arguments
	executionID := ctx.Arg(0)
	if executionID == "" {
		return cli.Error("execution-id is required")
	}

	attemptStr := ctx.Arg(1)
	if attemptStr == "" {
		return cli.Error("attempt is required")
	}
	var attempt int
	if _, err := fmt.Sscanf(attemptStr, "%d", &attempt); err != nil {
		return cli.Errorf("invalid attempt: %s", attemptStr)
	}
	if attempt <= 0 {
		return cli.Error("attempt must be positive")
	}

	// Load configuration from environment
	cfg, err := env.Parse[Config]()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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

	// Setup context with signal handling
	goCtx, cancel := signal.NotifyContext(ctx.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Run the worker
	return runWorker(goCtx, workerConfig{
		ExecutionID:       executionID,
		Attempt:           attempt,
		WorkerID:          workerID,
		StoreDSN:          cfg.StoreDSN,
		HeartbeatInterval: cfg.HeartbeatInterval,
		DBConnectRetries:  cfg.DBConnectRetries,
		DBRetryBackoff:    cfg.DBRetryBackoff,
		Logger:            logger,
	})
}

type workerConfig struct {
	ExecutionID       string
	Attempt           int
	WorkerID          string
	StoreDSN          string
	HeartbeatInterval time.Duration
	DBConnectRetries  int
	DBRetryBackoff    time.Duration
	Logger            *slog.Logger
}

func runWorker(ctx context.Context, cfg workerConfig) error {
	cfg.Logger.Info("starting worker",
		"execution_id", cfg.ExecutionID,
		"attempt", cfg.Attempt,
		"worker_id", cfg.WorkerID,
		"heartbeat_interval", cfg.HeartbeatInterval)

	// Connect to database with retry
	db, err := connectWithRetry(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	// Create store
	store := workflow.NewPostgresStore(workflow.PostgresStoreOptions{DB: db})

	// Load execution record
	record, err := store.Get(ctx, cfg.ExecutionID)
	if err != nil {
		return fmt.Errorf("load execution: %w", err)
	}
	if record == nil {
		return fmt.Errorf("execution not found: %s", cfg.ExecutionID)
	}

	cfg.Logger.Debug("loaded execution record",
		"status", record.Status,
		"workflow", record.WorkflowName,
		"record_attempt", record.Attempt)

	// Claim the execution with fencing
	// Only succeeds if status=pending AND attempt matches
	claimed, err := claimWithRetry(ctx, store, cfg)
	if err != nil {
		return err
	}
	if !claimed {
		// Fenced out - either not pending or newer attempt exists
		cfg.Logger.Info("execution not claimable, exiting",
			"execution_id", cfg.ExecutionID,
			"attempt", cfg.Attempt)
		return nil
	}

	cfg.Logger.Info("claimed execution",
		"execution_id", cfg.ExecutionID,
		"attempt", cfg.Attempt)

	// Start heartbeat goroutine
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()

	go heartbeatLoop(heartbeatCtx, store, cfg)

	// Note: In a real deployment, the worker would need access to:
	// 1. The workflow definition (e.g., via a registry or embedded)
	// 2. The checkpointer (e.g., file-based or S3)
	// 3. Any activities the workflow needs
	//
	// For now, this is a placeholder that demonstrates the claim/heartbeat/complete pattern.
	// A production implementation would:
	// - Load the workflow from a registry
	// - Create an Execution with the appropriate options
	// - Run the workflow
	// - Complete with the outputs

	cfg.Logger.Info("running execution (placeholder)",
		"execution_id", cfg.ExecutionID,
		"workflow", record.WorkflowName)

	// Simulate some work
	select {
	case <-time.After(1 * time.Second):
	case <-ctx.Done():
		return completeWithFencing(ctx, store, cfg.ExecutionID, cfg.Attempt,
			workflow.EngineStatusFailed, nil, "worker interrupted")
	}

	// Complete successfully with fencing
	// Only succeeds if attempt matches, preventing stale workers from overwriting
	return completeWithFencing(ctx, store, cfg.ExecutionID, cfg.Attempt,
		workflow.EngineStatusCompleted, map[string]any{"status": "ok"}, "")
}

// connectWithRetry connects to the database with exponential backoff retry.
func connectWithRetry(ctx context.Context, cfg workerConfig) (*sql.DB, error) {
	var db *sql.DB

	_, err := retry.Do(ctx, func() (*sql.DB, error) {
		cfg.Logger.Debug("connecting to database")

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
			cfg.Logger.Warn("database connection failed, retrying",
				"attempt", attempt,
				"error", err,
				"retry_delay", delay)
		}),
	)

	if err != nil {
		return nil, fmt.Errorf("connect to database after %d attempts: %w", cfg.DBConnectRetries, err)
	}

	cfg.Logger.Info("connected to database")
	return db, nil
}

// claimWithRetry claims the execution with retry for transient database errors.
func claimWithRetry(ctx context.Context, store workflow.ExecutionStore, cfg workerConfig) (bool, error) {
	var claimed bool

	_, err := retry.Do(ctx, func() (bool, error) {
		c, err := store.ClaimExecution(ctx, cfg.ExecutionID, cfg.WorkerID, cfg.Attempt)
		if err != nil {
			return false, err
		}
		claimed = c
		return c, nil
	},
		retry.WithMaxAttempts(3),
		retry.WithBackoff(100*time.Millisecond, 2*time.Second),
		retry.WithOnRetry(func(attempt int, err error, delay time.Duration) {
			cfg.Logger.Warn("claim execution failed, retrying",
				"attempt", attempt,
				"error", err,
				"retry_delay", delay)
		}),
	)

	if err != nil {
		return false, fmt.Errorf("claim execution: %w", err)
	}
	return claimed, nil
}

// heartbeatLoop sends periodic heartbeats to the store.
func heartbeatLoop(ctx context.Context, store workflow.ExecutionStore, cfg workerConfig) {
	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Heartbeat with retry for transient failures
			err := retry.DoSimple(ctx, func() error {
				return store.Heartbeat(ctx, cfg.ExecutionID, cfg.WorkerID)
			},
				retry.WithMaxAttempts(3),
				retry.WithBackoff(100*time.Millisecond, 1*time.Second),
			)
			if err != nil {
				cfg.Logger.Warn("heartbeat failed", "error", err)
			}
		}
	}
}

func completeWithFencing(
	ctx context.Context,
	store workflow.ExecutionStore,
	id string,
	attempt int,
	status workflow.EngineExecutionStatus,
	outputs map[string]any,
	lastError string,
) error {
	// Complete with retry for transient database errors
	var completed bool

	_, err := retry.Do(ctx, func() (bool, error) {
		c, err := store.CompleteExecution(ctx, id, attempt, status, outputs, lastError)
		if err != nil {
			return false, err
		}
		completed = c
		return c, nil
	},
		retry.WithMaxAttempts(5),
		retry.WithBackoff(100*time.Millisecond, 5*time.Second),
		retry.WithOnRetry(func(attempt int, err error, delay time.Duration) {
			slog.Warn("complete execution failed, retrying",
				"attempt", attempt,
				"error", err,
				"retry_delay", delay)
		}),
	)

	if err != nil {
		return fmt.Errorf("complete execution: %w", err)
	}
	if !completed {
		// Fenced out - a newer attempt took over
		slog.Warn("completion fenced out", "id", id, "attempt", attempt)
		return errors.New("completion fenced out")
	}
	slog.Info("execution completed", "id", id, "status", status)
	return nil
}
