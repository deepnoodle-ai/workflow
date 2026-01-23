// Package ai provides AI-native workflow extensions including agent activities,
// durable tools, and LLM integration for the workflow engine.
package ai

import (
	"encoding/json"
	"maps"
	"time"
)

// Role indicates the role of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// Message represents a single message in a conversation.
type Message struct {
	Role       Role         `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []ToolCall   `json:"tool_calls,omitempty"`
	ToolResult *ToolResult  `json:"tool_result,omitempty"`
	Timestamp  time.Time    `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// NewUserMessage creates a new user message.
func NewUserMessage(content string) Message {
	return Message{
		Role:      RoleUser,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// NewAssistantMessage creates a new assistant message.
func NewAssistantMessage(content string) Message {
	return Message{
		Role:      RoleAssistant,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// NewSystemMessage creates a new system message.
func NewSystemMessage(content string) Message {
	return Message{
		Role:      RoleSystem,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// NewToolResultMessage creates a new tool result message.
func NewToolResultMessage(result *ToolResult) Message {
	return Message{
		Role:       RoleTool,
		ToolResult: result,
		Timestamp:  time.Now(),
	}
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult represents the result of a tool invocation.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Success bool   `json:"success"`
}

// ConversationState represents the complete state of a conversation that can be
// checkpointed and restored. This is stored in activity params for durability.
type ConversationState struct {
	Messages     []Message      `json:"messages"`
	SystemPrompt string         `json:"system_prompt"`
	Model        string         `json:"model"`
	TotalTokens  int            `json:"total_tokens"`
	Artifacts    map[string]any `json:"artifacts,omitempty"`
}

// NewConversationState creates a new empty conversation state.
func NewConversationState() *ConversationState {
	return &ConversationState{
		Messages:  make([]Message, 0),
		Artifacts: make(map[string]any),
	}
}

// AddMessage appends a message to the conversation.
func (c *ConversationState) AddMessage(msg Message) {
	c.Messages = append(c.Messages, msg)
}

// AddUserMessage appends a user message to the conversation.
func (c *ConversationState) AddUserMessage(content string) {
	c.AddMessage(NewUserMessage(content))
}

// AddAssistantMessage appends an assistant message to the conversation.
func (c *ConversationState) AddAssistantMessage(content string) {
	c.AddMessage(NewAssistantMessage(content))
}

// AddToolResult appends a tool result message to the conversation.
func (c *ConversationState) AddToolResult(result *ToolResult) {
	c.AddMessage(NewToolResultMessage(result))
}

// LastMessage returns the last message in the conversation, or nil if empty.
func (c *ConversationState) LastMessage() *Message {
	if len(c.Messages) == 0 {
		return nil
	}
	return &c.Messages[len(c.Messages)-1]
}

// LastAssistantMessage returns the last assistant message, or nil if none.
func (c *ConversationState) LastAssistantMessage() *Message {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role == RoleAssistant {
			return &c.Messages[i]
		}
	}
	return nil
}

// HasPendingToolCalls returns true if the last assistant message has tool calls.
func (c *ConversationState) HasPendingToolCalls() bool {
	last := c.LastAssistantMessage()
	return last != nil && len(last.ToolCalls) > 0
}

// GetPendingToolCalls returns tool calls from the last assistant message.
func (c *ConversationState) GetPendingToolCalls() []ToolCall {
	last := c.LastAssistantMessage()
	if last == nil {
		return nil
	}
	return last.ToolCalls
}

// SetArtifact stores an artifact in the conversation state.
func (c *ConversationState) SetArtifact(key string, value any) {
	if c.Artifacts == nil {
		c.Artifacts = make(map[string]any)
	}
	c.Artifacts[key] = value
}

// GetArtifact retrieves an artifact from the conversation state.
func (c *ConversationState) GetArtifact(key string) (any, bool) {
	if c.Artifacts == nil {
		return nil, false
	}
	v, ok := c.Artifacts[key]
	return v, ok
}

// Clone creates a deep copy of the conversation state.
func (c *ConversationState) Clone() *ConversationState {
	data, err := json.Marshal(c)
	if err != nil {
		// Fall back to shallow copy if marshaling fails
		clone := &ConversationState{
			SystemPrompt: c.SystemPrompt,
			Model:        c.Model,
			TotalTokens:  c.TotalTokens,
			Messages:     make([]Message, len(c.Messages)),
			Artifacts:    make(map[string]any),
		}
		copy(clone.Messages, c.Messages)
		maps.Copy(clone.Artifacts, c.Artifacts)
		return clone
	}

	var clone ConversationState
	if err := json.Unmarshal(data, &clone); err != nil {
		// Fall back to shallow copy
		return &ConversationState{
			SystemPrompt: c.SystemPrompt,
			Model:        c.Model,
			TotalTokens:  c.TotalTokens,
			Messages:     c.Messages,
			Artifacts:    c.Artifacts,
		}
	}
	return &clone
}
