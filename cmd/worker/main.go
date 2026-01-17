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
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	"github.com/deepnoodle-ai/workflow"
)

func main() {
	// Parse command line flags
	var (
		executionID = flag.String("execution-id", "", "Execution ID to process")
		attempt     = flag.Int("attempt", 0, "Execution attempt number")
		workerID    = flag.String("worker-id", "", "Worker ID (defaults to hostname)")
	)
	flag.Parse()

	// Validate required flags
	if *executionID == "" {
		fmt.Fprintln(os.Stderr, "error: --execution-id is required")
		os.Exit(1)
	}
	if *attempt <= 0 {
		fmt.Fprintln(os.Stderr, "error: --attempt must be positive")
		os.Exit(1)
	}

	// Get store DSN from environment
	storeDSN := os.Getenv("WORKFLOW_STORE_DSN")
	if storeDSN == "" {
		fmt.Fprintln(os.Stderr, "error: WORKFLOW_STORE_DSN environment variable is required")
		os.Exit(1)
	}

	// Set worker ID
	wid := *workerID
	if wid == "" {
		hostname, err := os.Hostname()
		if err != nil {
			wid = fmt.Sprintf("worker-%d", os.Getpid())
		} else {
			wid = hostname
		}
	}

	// Setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// Setup context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Run the worker
	if err := runWorker(ctx, workerConfig{
		ExecutionID: *executionID,
		Attempt:     *attempt,
		WorkerID:    wid,
		StoreDSN:    storeDSN,
		Logger:      logger,
	}); err != nil {
		logger.Error("worker failed", "error", err)
		os.Exit(1)
	}
}

type workerConfig struct {
	ExecutionID string
	Attempt     int
	WorkerID    string
	StoreDSN    string
	Logger      *slog.Logger
}

func runWorker(ctx context.Context, cfg workerConfig) error {
	cfg.Logger.Info("starting worker",
		"execution_id", cfg.ExecutionID,
		"attempt", cfg.Attempt,
		"worker_id", cfg.WorkerID)

	// Connect to database
	db, err := sql.Open("postgres", cfg.StoreDSN)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()

	// Verify connection
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

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
	claimed, err := store.ClaimExecution(ctx, cfg.ExecutionID, cfg.WorkerID, cfg.Attempt)
	if err != nil {
		return fmt.Errorf("claim execution: %w", err)
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

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if err := store.Heartbeat(heartbeatCtx, cfg.ExecutionID, cfg.WorkerID); err != nil {
					cfg.Logger.Warn("heartbeat failed", "error", err)
				}
			}
		}
	}()

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

func completeWithFencing(
	ctx context.Context,
	store workflow.ExecutionStore,
	id string,
	attempt int,
	status workflow.EngineExecutionStatus,
	outputs map[string]any,
	lastError string,
) error {
	completed, err := store.CompleteExecution(ctx, id, attempt, status, outputs, lastError)
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
