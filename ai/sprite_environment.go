package ai

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	sprites "github.com/superfly/sprites-go"
)

// SpriteConfig contains configuration for running agents in Sprites.
type SpriteConfig struct {
	// Image is the container image for the Sprite (optional, uses default if empty).
	Image string

	// Timeout is the maximum execution time for the agent.
	Timeout time.Duration

	// CleanupOnComplete determines if the Sprite is deleted after execution.
	CleanupOnComplete bool
}

// SpriteAgentEnvironmentOptions configures a SpriteAgentEnvironment.
type SpriteAgentEnvironmentOptions struct {
	// Token is the Sprites API token (required).
	Token string

	// Config contains Sprite configuration.
	Config SpriteConfig

	// Logger for the environment.
	Logger *slog.Logger

	// SpritePrefix is the prefix for sprite names.
	SpritePrefix string
}

// SpriteAgentEnvironment runs agents in isolated Sprite VMs.
// This provides security isolation and enables VM-level checkpointing.
type SpriteAgentEnvironment struct {
	client       *sprites.Client
	config       SpriteConfig
	logger       *slog.Logger
	spritePrefix string
}

// NewSpriteAgentEnvironment creates a new SpriteAgentEnvironment.
func NewSpriteAgentEnvironment(opts SpriteAgentEnvironmentOptions) (*SpriteAgentEnvironment, error) {
	if opts.Token == "" {
		return nil, fmt.Errorf("sprites token is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	spritePrefix := opts.SpritePrefix
	if spritePrefix == "" {
		spritePrefix = "agent-"
	}

	return &SpriteAgentEnvironment{
		client:       sprites.New(opts.Token),
		config:       opts.Config,
		logger:       logger,
		spritePrefix: spritePrefix,
	}, nil
}

// SpriteAgentHandle represents a running agent in a Sprite.
type SpriteAgentHandle struct {
	SpriteName   string
	CheckpointID string
	sprite       *sprites.Sprite
	env          *SpriteAgentEnvironment
}

// CreateSprite creates a new Sprite for running an agent.
func (e *SpriteAgentEnvironment) CreateSprite(ctx context.Context, name string) (*SpriteAgentHandle, error) {
	spriteName := e.spritePrefix + name

	e.logger.Info("creating sprite for agent",
		"name", name,
		"sprite_name", spriteName)

	sprite, err := e.client.CreateSprite(ctx, spriteName, nil)
	if err != nil {
		return nil, fmt.Errorf("create sprite: %w", err)
	}

	e.logger.Debug("sprite created",
		"sprite_name", spriteName,
		"status", sprite.Status)

	return &SpriteAgentHandle{
		SpriteName: spriteName,
		sprite:     sprite,
		env:        e,
	}, nil
}

// GetSprite gets an existing Sprite by name.
func (e *SpriteAgentEnvironment) GetSprite(ctx context.Context, name string) (*SpriteAgentHandle, error) {
	spriteName := e.spritePrefix + name

	sprite, err := e.client.GetSprite(ctx, spriteName)
	if err != nil {
		return nil, fmt.Errorf("get sprite: %w", err)
	}

	return &SpriteAgentHandle{
		SpriteName: spriteName,
		sprite:     sprite,
		env:        e,
	}, nil
}

// ListSprites lists all agent Sprites.
func (e *SpriteAgentEnvironment) ListSprites(ctx context.Context) ([]*sprites.Sprite, error) {
	return e.client.ListAllSprites(ctx, e.spritePrefix)
}

// DeleteSprite deletes a Sprite by name.
func (e *SpriteAgentEnvironment) DeleteSprite(ctx context.Context, name string) error {
	spriteName := e.spritePrefix + name
	return e.client.DeleteSprite(ctx, spriteName)
}

// CleanupStaleSprites deletes Sprites older than maxAge.
func (e *SpriteAgentEnvironment) CleanupStaleSprites(ctx context.Context, maxAge time.Duration) (int, error) {
	allSprites, err := e.ListSprites(ctx)
	if err != nil {
		return 0, fmt.Errorf("list sprites: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	deleted := 0

	for _, s := range allSprites {
		if s.CreatedAt.Before(cutoff) {
			if err := e.client.DeleteSprite(ctx, s.Name()); err != nil {
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

// RunCommand runs a command in the Sprite.
func (h *SpriteAgentHandle) RunCommand(ctx context.Context, command string, args ...string) error {
	cmd := h.sprite.CommandContext(ctx, command, args...)

	if h.env.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.env.config.Timeout)
		defer cancel()
	}

	return cmd.Run()
}

// Destroy deletes the Sprite.
func (h *SpriteAgentHandle) Destroy(ctx context.Context) error {
	return h.sprite.Delete(ctx)
}

// Name returns the full Sprite name.
func (h *SpriteAgentHandle) Name() string {
	return h.SpriteName
}

// SpriteCheckpoint represents a checkpoint of a Sprite's state.
type SpriteCheckpoint struct {
	// ID is the checkpoint identifier.
	ID string `json:"id"`

	// SpriteName is the name of the Sprite this checkpoint belongs to.
	SpriteName string `json:"sprite_name"`

	// CreatedAt is when the checkpoint was created.
	CreatedAt time.Time `json:"created_at"`

	// ConversationState is the agent's conversation state at checkpoint time.
	ConversationState *ConversationState `json:"conversation_state,omitempty"`
}

// SpriteAgentRunner provides a higher-level interface for running agents in Sprites.
// It handles checkpoint/restore automatically.
type SpriteAgentRunner struct {
	env    *SpriteAgentEnvironment
	llm    LLMProvider
	logger *slog.Logger
}

// NewSpriteAgentRunner creates a new SpriteAgentRunner.
func NewSpriteAgentRunner(env *SpriteAgentEnvironment, llm LLMProvider) *SpriteAgentRunner {
	return &SpriteAgentRunner{
		env:    env,
		llm:    llm,
		logger: env.logger,
	}
}

// RunAgentParams configures an agent run.
type RunAgentParams struct {
	// Name is a unique name for this agent run.
	Name string

	// Input is the initial user message.
	Input string

	// SystemPrompt for the agent.
	SystemPrompt string

	// Tools available to the agent.
	Tools map[string]Tool

	// MaxTurns limits conversation turns.
	MaxTurns int

	// RestoreFrom is an optional checkpoint to restore from.
	RestoreFrom *SpriteCheckpoint
}

// RunAgentResult contains the result of an agent run.
type RunAgentResult struct {
	// Response is the agent's final response.
	Response string

	// Conversation is the final conversation state.
	Conversation *ConversationState

	// TurnsUsed is the number of turns used.
	TurnsUsed int

	// Checkpoint is the final checkpoint (can be used to resume).
	Checkpoint *SpriteCheckpoint

	// SpriteName is the name of the Sprite used.
	SpriteName string
}
