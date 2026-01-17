package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	sprites "github.com/superfly/sprites-go"
)

// SpritesEnvironmentOptions contains configuration for SpritesEnvironment.
type SpritesEnvironmentOptions struct {
	// Token is the Sprites API token (required).
	Token string

	// StoreDSN is the database connection string for the worker to connect to
	// the ExecutionStore (required).
	StoreDSN string

	// WorkerCommand is the command to run for the worker process.
	// Defaults to "/app/worker" if not specified.
	WorkerCommand string

	// SpritePrefix is the prefix for sprite names.
	// Defaults to "workflow-worker-" if not specified.
	SpritePrefix string

	// Logger is the logger for the environment.
	Logger *slog.Logger

	// CleanupSprites determines whether sprites are deleted after dispatch.
	// Default is false (sprites are kept for debugging/reuse).
	CleanupSprites bool
}

// SpritesEnvironment implements DispatchEnvironment using Sprites for
// on-demand compute. Each dispatch creates a sprite, runs the worker command,
// and returns. The worker is responsible for claiming, running, and completing
// the execution.
type SpritesEnvironment struct {
	client        *sprites.Client
	storeDSN      string
	workerCommand string
	spritePrefix  string
	logger        *slog.Logger
	cleanup       bool
}

// NewSpritesEnvironment creates a new SpritesEnvironment.
func NewSpritesEnvironment(opts SpritesEnvironmentOptions) (*SpritesEnvironment, error) {
	if opts.Token == "" {
		return nil, fmt.Errorf("sprites token is required")
	}
	if opts.StoreDSN == "" {
		return nil, fmt.Errorf("store DSN is required")
	}

	workerCommand := opts.WorkerCommand
	if workerCommand == "" {
		workerCommand = "/app/worker"
	}

	spritePrefix := opts.SpritePrefix
	if spritePrefix == "" {
		spritePrefix = "workflow-worker-"
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	client := sprites.New(opts.Token)

	return &SpritesEnvironment{
		client:        client,
		storeDSN:      opts.StoreDSN,
		workerCommand: workerCommand,
		spritePrefix:  spritePrefix,
		logger:        logger,
		cleanup:       opts.CleanupSprites,
	}, nil
}

// Mode returns EnvironmentModeDispatch.
func (e *SpritesEnvironment) Mode() EnvironmentMode {
	return EnvironmentModeDispatch
}

// Dispatch triggers remote execution in a Sprite. Returns once handoff succeeds.
// The remote worker is responsible for claiming, running, and completing.
func (e *SpritesEnvironment) Dispatch(ctx context.Context, executionID string, attempt int) error {
	// Generate a unique sprite name for this execution
	spriteName := fmt.Sprintf("%s%s-%d", e.spritePrefix, executionID, attempt)

	e.logger.Info("dispatching execution to sprite",
		"execution_id", executionID,
		"attempt", attempt,
		"sprite", spriteName)

	// Create a new sprite for this execution
	sprite, err := e.client.CreateSprite(ctx, spriteName, nil)
	if err != nil {
		return fmt.Errorf("create sprite: %w", err)
	}

	e.logger.Debug("sprite created",
		"sprite", spriteName,
		"status", sprite.Status)

	// Build the worker command
	// The worker binary is responsible for:
	// 1. Connecting to the store using the DSN
	// 2. Claiming the execution (with fencing via attempt)
	// 3. Running the workflow with heartbeating
	// 4. Completing/failing in the store (with fencing)
	cmd := sprite.CommandContext(ctx, e.workerCommand,
		"--execution-id", executionID,
		"--attempt", strconv.Itoa(attempt),
	)

	// Pass store DSN via environment variable for security
	cmd.Env = append(cmd.Env, "WORKFLOW_STORE_DSN="+e.storeDSN)

	// Start the command without waiting for completion.
	// The worker will run asynchronously in the sprite.
	if err := cmd.Start(); err != nil {
		// Clean up sprite on failure
		if e.cleanup {
			if deleteErr := sprite.Delete(ctx); deleteErr != nil {
				e.logger.Warn("failed to delete sprite after exec error",
					"sprite", spriteName,
					"error", deleteErr)
			}
		}
		return fmt.Errorf("start worker: %w", err)
	}

	e.logger.Info("worker started in sprite",
		"execution_id", executionID,
		"attempt", attempt,
		"sprite", spriteName)

	// Return immediately - the worker runs asynchronously.
	// The reaper will detect if the worker fails to claim (stale dispatched_at)
	// or dies during execution (stale heartbeat).
	return nil
}

// DeleteSprite deletes a sprite by name. This can be called to clean up
// sprites after execution completes.
func (e *SpritesEnvironment) DeleteSprite(ctx context.Context, spriteName string) error {
	return e.client.DeleteSprite(ctx, spriteName)
}

// ListSprites lists all sprites with the configured prefix.
func (e *SpritesEnvironment) ListSprites(ctx context.Context) ([]*sprites.Sprite, error) {
	return e.client.ListAllSprites(ctx, e.spritePrefix)
}

// CleanupStaleSprites deletes sprites that are older than the specified age.
// This is useful for cleaning up sprites from failed or orphaned executions.
func (e *SpritesEnvironment) CleanupStaleSprites(ctx context.Context, maxAge time.Duration) (int, error) {
	allSprites, err := e.ListSprites(ctx)
	if err != nil {
		return 0, fmt.Errorf("list sprites: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	deleted := 0

	for _, s := range allSprites {
		if s.CreatedAt.Before(cutoff) {
			if err := e.DeleteSprite(ctx, s.Name()); err != nil {
				e.logger.Warn("failed to delete stale sprite",
					"sprite", s.Name(),
					"error", err)
				continue
			}
			e.logger.Debug("deleted stale sprite",
				"sprite", s.Name(),
				"created_at", s.CreatedAt)
			deleted++
		}
	}

	return deleted, nil
}

// Verify SpritesEnvironment implements DispatchEnvironment.
var _ DispatchEnvironment = (*SpritesEnvironment)(nil)
