# Conversation State

The `ConversationState` type manages conversation history for AI agents, providing JSON-serializable state that can be checkpointed and restored during workflow recovery.

## Types

### Message

Represents a single message in a conversation:

```go
type Message struct {
    Role       Role           `json:"role"`
    Content    string         `json:"content"`
    ToolCalls  []ToolCall     `json:"tool_calls,omitempty"`
    ToolResult *ToolResult    `json:"tool_result,omitempty"`
    Timestamp  time.Time      `json:"timestamp"`
    Metadata   map[string]any `json:"metadata,omitempty"`
}
```

### Roles

```go
const (
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleSystem    Role = "system"
    RoleTool      Role = "tool"
)
```

### ToolCall

Represents a tool invocation requested by the LLM:

```go
type ToolCall struct {
    ID        string         `json:"id"`
    Name      string         `json:"name"`
    Arguments map[string]any `json:"arguments"`
}
```

### ToolResult

Represents the result of a tool invocation:

```go
type ToolResult struct {
    CallID  string `json:"call_id"`
    Output  string `json:"output,omitempty"`
    Error   string `json:"error,omitempty"`
    Success bool   `json:"success"`
}
```

### ConversationState

The complete conversation state for checkpointing:

```go
type ConversationState struct {
    Messages     []Message      `json:"messages"`
    SystemPrompt string         `json:"system_prompt"`
    Model        string         `json:"model"`
    TotalTokens  int            `json:"total_tokens"`
    Artifacts    map[string]any `json:"artifacts,omitempty"`
}
```

## Usage

### Creating a Conversation

```go
conv := ai.NewConversationState()
conv.SystemPrompt = "You are a helpful assistant."
conv.Model = "claude-3-opus"
```

### Adding Messages

```go
// Add user message
conv.AddUserMessage("Hello, can you help me?")

// Add assistant message
conv.AddAssistantMessage("Of course! How can I assist you?")

// Add system message
conv.AddMessage(ai.NewSystemMessage("Remember to be concise."))

// Add tool result
conv.AddToolResult(&ai.ToolResult{
    CallID:  "call_123",
    Output:  "Search results: ...",
    Success: true,
})
```

### Querying Messages

```go
// Get last message
last := conv.LastMessage()

// Get last assistant message
lastAssistant := conv.LastAssistantMessage()

// Check for pending tool calls
if conv.HasPendingToolCalls() {
    calls := conv.GetPendingToolCalls()
    for _, call := range calls {
        fmt.Printf("Tool: %s, Args: %v\n", call.Name, call.Arguments)
    }
}
```

### Managing Artifacts

Artifacts allow storing arbitrary data alongside the conversation:

```go
// Store artifact
conv.SetArtifact("search_results", results)
conv.SetArtifact("user_preferences", prefs)

// Retrieve artifact
if results, ok := conv.GetArtifact("search_results"); ok {
    // Use results
}
```

### Cloning

Create an independent copy for branching conversations:

```go
clone := conv.Clone()
clone.AddUserMessage("Different path...")
// Original conversation is unchanged
```

## Checkpointing

ConversationState is designed to be checkpointed in workflow activity parameters:

```go
type AgentActivityParams struct {
    Input      string             `json:"input"`
    Checkpoint *ConversationState `json:"checkpoint,omitempty"`
    MaxTurns   int                `json:"max_turns,omitempty"`
}
```

On recovery, the checkpoint is restored automatically:

```go
func (a *AgentActivity) Execute(ctx workflow.Context, params AgentActivityParams) (AgentActivityResult, error) {
    // Restore from checkpoint or start fresh
    conv := params.Checkpoint
    if conv == nil {
        conv = ai.NewConversationState()
        conv.SystemPrompt = a.systemPrompt
        conv.AddUserMessage(params.Input)
    }

    // Continue from where we left off...
}
```

## JSON Serialization

All types are fully JSON-serializable:

```go
// Serialize
data, _ := json.Marshal(conv)

// Deserialize
var restored ai.ConversationState
json.Unmarshal(data, &restored)
```

## Message Helpers

Convenience functions for creating messages:

```go
msg := ai.NewUserMessage("Hello")
msg := ai.NewAssistantMessage("Hi there!")
msg := ai.NewSystemMessage("Be helpful")
msg := ai.NewToolResultMessage(&ai.ToolResult{...})
```

## Best Practices

1. **Use artifacts for non-message data** - Store intermediate results, preferences, or context in artifacts rather than encoding in messages.

2. **Clone before branching** - When exploring multiple conversation paths, clone the state first.

3. **Track token usage** - Update `TotalTokens` to monitor context window usage.

4. **Clear old messages when needed** - For long conversations, consider summarizing and clearing old messages to stay within token limits.
