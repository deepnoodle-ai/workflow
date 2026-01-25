// Package main implements the orchestrator binary for distributed workflow execution.
// The orchestrator provides HTTP endpoints for workers to claim and complete tasks,
// and manages execution state in PostgreSQL.
//
// Commands:
//   - serve: Start the HTTP server for task distribution
//   - reap: Run stale task detection once (for cron jobs)
//
// Environment variables:
//   - WORKFLOW_STORE_DSN (required): PostgreSQL connection string
//   - LISTEN_ADDR (default :8080): HTTP listen address
//   - AUTH_TOKEN (optional): Bearer token for authentication
//   - HEARTBEAT_TIMEOUT (default 2m): How long before a task is considered stale
//   - REAPER_INTERVAL (default 30s): How often to check for stale tasks
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

	"github.com/deepnoodle-ai/workflow/domain"
	workflowhttp "github.com/deepnoodle-ai/workflow/internal/http"
	"github.com/deepnoodle-ai/workflow/internal/postgres"
	"github.com/deepnoodle-ai/workflow/internal/services"
	"github.com/deepnoodle-ai/wonton/cli"
	"github.com/deepnoodle-ai/wonton/env"
	"github.com/deepnoodle-ai/wonton/retry"
)

// Config holds all orchestrator configuration, loaded from environment variables.
type Config struct {
	// StoreDSN is the PostgreSQL connection string (required).
	StoreDSN string `env:"WORKFLOW_STORE_DSN,required"`

	// ListenAddr is the HTTP listen address.
	ListenAddr string `env:"LISTEN_ADDR" envDefault:":8080"`

	// AuthToken is the Bearer token for authentication (optional).
	// When set, all requests must include "Authorization: Bearer <token>".
	AuthToken string `env:"AUTH_TOKEN"`

	// HeartbeatTimeout is how long before a task is considered stale.
	HeartbeatTimeout time.Duration `env:"HEARTBEAT_TIMEOUT" envDefault:"2m"`

	// ReaperInterval is how often to check for stale tasks.
	ReaperInterval time.Duration `env:"REAPER_INTERVAL" envDefault:"30s"`

	// DBConnectRetries is the number of times to retry connecting to the database.
	DBConnectRetries int `env:"WORKFLOW_DB_CONNECT_RETRIES" envDefault:"5"`

	// DBRetryBackoff is the initial backoff duration for database retries.
	DBRetryBackoff time.Duration `env:"WORKFLOW_DB_RETRY_BACKOFF" envDefault:"1s"`
}

func main() {
	app := cli.New("orchestrator").
		Description("Workflow engine orchestrator for distributed task execution").
		Version("0.1.0")

	app.Command("serve").
		Description("Start the HTTP server for task distribution").
		Run(serve)

	app.Command("reap").
		Description("Run stale task detection once (for cron jobs)").
		Run(reap)

	app.Command("migrate").
		Description("Create or update the database schema").
		Run(migrate)

	if err := app.Execute(); err != nil {
		if cli.IsHelpRequested(err) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(cli.GetExitCode(err))
	}
}

func serve(ctx *cli.Context) error {
	cfg, db, logger, err := setup(ctx.Context())
	if err != nil {
		return err
	}
	defer db.Close()

	// Create store
	store := postgres.NewStore(postgres.StoreOptions{
		DB: db,
		Config: domain.StoreConfig{
			HeartbeatInterval: cfg.HeartbeatTimeout / 4, // Workers should heartbeat 4x per timeout
			LeaseTimeout:      cfg.HeartbeatTimeout,
		},
	})

	// Create services
	taskService := services.NewTaskService(services.TaskServiceOptions{
		Tasks:  store,
		Events: store,
	})
	executionService := services.NewExecutionService(services.ExecutionServiceOptions{
		Executions: store,
		Events:     store,
	})
	reaperService := services.NewReaperService(services.ReaperServiceOptions{
		Tasks:            store,
		HeartbeatTimeout: cfg.HeartbeatTimeout,
		Logger:           logger,
	})

	// Setup authentication
	var auth workflowhttp.Authenticator
	if cfg.AuthToken != "" {
		auth = workflowhttp.NewTokenAuthenticator([]string{cfg.AuthToken})
		logger.Info("token authentication enabled")
	} else {
		auth = &workflowhttp.NoopAuthenticator{}
		logger.Warn("no authentication configured - running in open mode")
	}

	// Create HTTP server
	server := workflowhttp.NewServer(workflowhttp.ServerOptions{
		TaskService:      taskService,
		ExecutionService: executionService,
		Auth:             auth,
		Logger:           logger,
	})

	// Setup signal handling
	goCtx, cancel := signal.NotifyContext(ctx.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start reaper loop in background
	go reaperLoop(goCtx, reaperService, cfg.ReaperInterval, logger)

	// Start HTTP server
	logger.Info("starting orchestrator", "addr", cfg.ListenAddr)
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe(cfg.ListenAddr)
	}()

	// Wait for shutdown signal or error
	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-goCtx.Done():
		logger.Info("shutting down orchestrator")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	}
}

func reap(ctx *cli.Context) error {
	cfg, db, logger, err := setup(ctx.Context())
	if err != nil {
		return err
	}
	defer db.Close()

	// Create store
	store := postgres.NewStore(postgres.StoreOptions{DB: db})

	// Create reaper service
	reaperService := services.NewReaperService(services.ReaperServiceOptions{
		Tasks:            store,
		HeartbeatTimeout: cfg.HeartbeatTimeout,
		Logger:           logger,
	})

	// Run reaper once
	count, err := reaperService.ReapStaleTasks(ctx.Context())
	if err != nil {
		return fmt.Errorf("reap stale tasks: %w", err)
	}

	if count > 0 {
		logger.Info("reset stale tasks", "count", count)
	} else {
		logger.Info("no stale tasks found")
	}

	return nil
}

func migrate(ctx *cli.Context) error {
	_, db, logger, err := setup(ctx.Context())
	if err != nil {
		return err
	}
	defer db.Close()

	// Create store
	store := postgres.NewStore(postgres.StoreOptions{DB: db})

	// Run migrations
	logger.Info("creating database schema")
	if err := store.CreateSchema(ctx.Context()); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	logger.Info("database schema created successfully")
	return nil
}

func setup(ctx context.Context) (*Config, *sql.DB, *slog.Logger, error) {
	// Setup logging first
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// Load configuration from environment
	cfgVal, err := env.Parse[Config]()
	if err != nil {
		return nil, nil, logger, fmt.Errorf("load config: %w", err)
	}
	cfg := &cfgVal

	// Connect to database
	db, err := connectWithRetry(ctx, cfg, logger)
	if err != nil {
		return nil, nil, logger, err
	}

	return cfg, db, logger, nil
}

// reaperLoop runs the stale task reaper on an interval.
func reaperLoop(ctx context.Context, reaper *services.ReaperService, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := reaper.ReapStaleTasks(ctx)
			if err != nil {
				logger.Warn("reaper error", "error", err)
				continue
			}
			if count > 0 {
				logger.Info("reset stale tasks", "count", count)
			}
		}
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
