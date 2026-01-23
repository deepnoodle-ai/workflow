# Sprites Integration

Sprites provide isolated VM-level execution environments for AI agents. This enables secure agent execution, VM-level checkpointing, and parallel exploration through forking.

## Overview

[Sprites](https://github.com/superfly/sprites) are lightweight VMs that provide:

- **Isolation** - Each agent runs in its own VM, preventing escape
- **Checkpointing** - Sub-second VM snapshots capture full agent state
- **Forking** - Clone VMs to explore multiple reasoning paths
- **Resource limits** - Prevent runaway agents from consuming resources

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Workflow Engine                          │
│                         │                                   │
│                         ▼                                   │
│            ┌─────────────────────────┐                      │
│            │  SpriteAgentEnvironment │                      │
│            └───────────┬─────────────┘                      │
│                        │                                    │
└────────────────────────┼────────────────────────────────────┘
                         │
                         ▼
        ┌────────────────────────────────┐
        │         Sprites API            │
        └────────────┬───────────────────┘
                     │
     ┌───────────────┼───────────────┐
     ▼               ▼               ▼
┌─────────┐    ┌─────────┐    ┌─────────┐
│ Sprite  │    │ Sprite  │    │ Sprite  │
│ (VM 1)  │    │ (VM 2)  │    │ (VM 3)  │
│ Agent A │    │ Agent B │    │ Agent A │
│         │    │         │    │ (fork)  │
└─────────┘    └─────────┘    └─────────┘
```

## Configuration

### SpriteAgentEnvironmentOptions

```go
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
```

### SpriteConfig

```go
type SpriteConfig struct {
    // Image is the container image for the Sprite.
    Image string

    // Timeout is the maximum execution time for the agent.
    Timeout time.Duration

    // CleanupOnComplete determines if the Sprite is deleted after execution.
    CleanupOnComplete bool
}
```

## Creating an Environment

```go
env, err := ai.NewSpriteAgentEnvironment(ai.SpriteAgentEnvironmentOptions{
    Token: os.Getenv("SPRITES_TOKEN"),
    Config: ai.SpriteConfig{
        Image:             "agent-runtime:latest",
        Timeout:           5 * time.Minute,
        CleanupOnComplete: true,
    },
    SpritePrefix: "workflow-agent-",
})
```

## Managing Sprites

### Creating a Sprite

```go
handle, err := env.CreateSprite(ctx, "my-agent")
if err != nil {
    return err
}
defer handle.Destroy(ctx) // Clean up when done
```

### Running Commands

```go
// Run a command in the Sprite
err := handle.RunCommand(ctx, "python", "agent.py", "--input", inputFile)
```

### Getting Existing Sprites

```go
handle, err := env.GetSprite(ctx, "my-agent")
```

### Listing Sprites

```go
sprites, err := env.ListSprites(ctx)
for _, s := range sprites {
    fmt.Printf("Sprite: %s, Status: %s\n", s.Name(), s.Status)
}
```

### Cleanup

```go
// Delete a specific Sprite
err := env.DeleteSprite(ctx, "my-agent")

// Clean up stale Sprites (older than 1 hour)
deleted, err := env.CleanupStaleSprites(ctx, 1*time.Hour)
fmt.Printf("Deleted %d stale sprites\n", deleted)
```

## SpriteAgentRunner

Higher-level interface for running agents:

```go
runner := ai.NewSpriteAgentRunner(env, llmProvider)

result, err := runner.RunAgent(ctx, ai.RunAgentParams{
    Name:         "research-agent",
    Input:        "Research the latest AI papers",
    SystemPrompt: "You are a research assistant.",
    Tools:        tools,
    MaxTurns:     10,
})
```

### Resuming from Checkpoint

```go
// Save checkpoint from previous run
checkpoint := previousResult.Checkpoint

// Resume from checkpoint
result, err := runner.RunAgent(ctx, ai.RunAgentParams{
    Name:         "research-agent",
    Input:        "Continue the research",
    SystemPrompt: "You are a research assistant.",
    Tools:        tools,
    MaxTurns:     10,
    RestoreFrom:  checkpoint, // Resume from here
})
```

## Checkpointing

### SpriteCheckpoint

```go
type SpriteCheckpoint struct {
    // ID is the checkpoint identifier.
    ID string

    // SpriteName is the name of the Sprite.
    SpriteName string

    // CreatedAt is when the checkpoint was created.
    CreatedAt time.Time

    // ConversationState is the agent's conversation state.
    ConversationState *ConversationState
}
```

### Checkpoint Flow

```
                    ┌─────────────────┐
                    │  Agent Running  │
                    │   in Sprite     │
                    └────────┬────────┘
                             │
                             ▼
                    ┌─────────────────┐
                    │  Tool Call      │
                    │  Boundary       │
                    └────────┬────────┘
                             │
              ┌──────────────┴──────────────┐
              │                             │
              ▼                             ▼
    ┌──────────────────┐         ┌──────────────────┐
    │ Save Conversation│         │ Save Sprite      │
    │     State        │         │   Checkpoint     │
    └──────────────────┘         └──────────────────┘
              │                             │
              └──────────────┬──────────────┘
                             │
                             ▼
                    ┌─────────────────┐
                    │  SpriteCheckpoint│
                    │    (Complete)   │
                    └─────────────────┘
```

## Recovery

On workflow recovery:

1. Load `SpriteCheckpoint` from execution params
2. Restore Sprite from checkpoint or create new
3. Restore `ConversationState`
4. Resume agent execution from checkpoint

```go
// In AgentActivity.Execute
if params.Checkpoint != nil {
    // Restore from Sprite checkpoint
    handle, err := env.GetSprite(ctx, params.Checkpoint.SpriteName)
    if err != nil {
        // Sprite gone, create new and restore conversation
        handle, err = env.CreateSprite(ctx, agentName)
        // Restore conversation state...
    }
}
```

## Forking for Parallel Exploration

Create multiple Sprites to explore different reasoning paths:

```go
// Create base Sprite and run to decision point
baseHandle, _ := env.CreateSprite(ctx, "decision-agent")
// ... run until decision point ...

// Fork to explore option A
optionA, _ := env.CreateSprite(ctx, "decision-agent-option-a")
// Copy state and explore option A...

// Fork to explore option B
optionB, _ := env.CreateSprite(ctx, "decision-agent-option-b")
// Copy state and explore option B...

// Compare results and choose best path
```

## Security Benefits

### Isolation

- Agents cannot access host filesystem
- Network access can be restricted
- Resource limits prevent DoS

### Sandboxing

```go
env, _ := ai.NewSpriteAgentEnvironment(ai.SpriteAgentEnvironmentOptions{
    Token: token,
    Config: ai.SpriteConfig{
        Image:   "sandboxed-runtime:latest",
        Timeout: 5 * time.Minute,
    },
})

// Agent tools execute inside Sprite, not on host
agent := ai.NewAgentActivity("untrusted-agent", llm, ai.AgentActivityOptions{
    Tools: map[string]ai.Tool{
        "run_code": pythonTool, // Executes inside Sprite
    },
})
```

## Best Practices

1. **Set timeouts** - Always configure `Timeout` to prevent runaway agents.

2. **Clean up resources** - Use `CleanupOnComplete: true` or call `Destroy()`.

3. **Handle checkpoint failures** - Have fallback for when Sprite restoration fails.

4. **Use prefixes** - Set `SpritePrefix` to identify workflow-managed Sprites.

5. **Monitor stale Sprites** - Run `CleanupStaleSprites` periodically.

6. **Log Sprite events** - Track creation, checkpoints, and destruction.

## Local Development

For development without Sprites, agents run locally:

```go
// Production: Use Sprites
if os.Getenv("USE_SPRITES") == "true" {
    env, _ := ai.NewSpriteAgentEnvironment(opts)
    // Run in Sprite...
} else {
    // Development: Run locally
    agent.Execute(ctx, params)
}
```

## Resource Management

```go
// List all agent Sprites
sprites, _ := env.ListSprites(ctx)
fmt.Printf("Active Sprites: %d\n", len(sprites))

// Clean up Sprites older than 24 hours
deleted, _ := env.CleanupStaleSprites(ctx, 24*time.Hour)
fmt.Printf("Cleaned up %d stale Sprites\n", deleted)

// Delete specific Sprite
env.DeleteSprite(ctx, "old-agent")
```
