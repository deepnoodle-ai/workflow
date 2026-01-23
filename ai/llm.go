package ai

import (
	"context"
)

// LLMProvider is the interface for LLM backends. Implementations adapt specific
// LLM libraries (like Dive) to a common interface for use in agent activities.
type LLMProvider interface {
	// Generate produces a response from the LLM given messages and options.
	Generate(ctx context.Context, messages []Message, opts GenerateOptions) (*GenerateResponse, error)

	// Name returns the name of the LLM provider (e.g., "anthropic", "openai").
	Name() string

	// Model returns the current model being used (e.g., "claude-3-opus-20240229").
	Model() string
}

// StreamingLLMProvider extends LLMProvider with streaming support.
type StreamingLLMProvider interface {
	LLMProvider

	// Stream produces a streaming response from the LLM.
	Stream(ctx context.Context, messages []Message, opts GenerateOptions) (StreamIterator, error)
}

// StreamIterator allows iteration over streaming LLM responses.
type StreamIterator interface {
	// Next advances to the next event. Returns false when done or on error.
	Next() bool

	// Event returns the current event.
	Event() *StreamEvent

	// Err returns any error that occurred.
	Err() error

	// Close releases resources.
	Close() error
}

// StreamEvent represents a single event in a streaming response.
type StreamEvent struct {
	Type    StreamEventType `json:"type"`
	Delta   string          `json:"delta,omitempty"`   // Text delta for content events
	Content string          `json:"content,omitempty"` // Full content so far
}

// StreamEventType indicates the type of streaming event.
type StreamEventType string

const (
	StreamEventStart       StreamEventType = "start"
	StreamEventContentDelta StreamEventType = "content_delta"
	StreamEventToolUse     StreamEventType = "tool_use"
	StreamEventStop        StreamEventType = "stop"
	StreamEventError       StreamEventType = "error"
)

// GenerateOptions configures an LLM generation request.
type GenerateOptions struct {
	// SystemPrompt overrides the default system prompt.
	SystemPrompt string

	// MaxTokens limits the response length.
	MaxTokens int

	// Temperature controls randomness (0.0 = deterministic, 1.0 = creative).
	Temperature *float64

	// Tools available for the LLM to call.
	Tools []Tool

	// ToolChoice influences which tool the LLM selects.
	ToolChoice *ToolChoiceConfig

	// StopSequences are strings that stop generation when encountered.
	StopSequences []string

	// Metadata for tracing and logging.
	Metadata map[string]string
}

// ToolChoiceConfig configures how the LLM chooses tools.
type ToolChoiceConfig struct {
	Type ToolChoiceType `json:"type"`
	Name string         `json:"name,omitempty"` // For ToolChoiceTypeTool
}

// ToolChoiceType indicates how the LLM should choose tools.
type ToolChoiceType string

const (
	// ToolChoiceAuto lets the LLM decide whether to use tools.
	ToolChoiceAuto ToolChoiceType = "auto"

	// ToolChoiceAny requires the LLM to use at least one tool.
	ToolChoiceAny ToolChoiceType = "any"

	// ToolChoiceTool forces the LLM to use a specific tool (set Name).
	ToolChoiceTool ToolChoiceType = "tool"

	// ToolChoiceNone prevents the LLM from using tools.
	ToolChoiceNone ToolChoiceType = "none"
)

// GenerateResponse contains the result of an LLM generation.
type GenerateResponse struct {
	// Content is the text content of the response.
	Content string `json:"content"`

	// ToolCalls requested by the LLM.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// StopReason indicates why generation stopped.
	StopReason StopReason `json:"stop_reason"`

	// Usage contains token counts.
	Usage Usage `json:"usage"`

	// Model used for generation.
	Model string `json:"model"`

	// Thinking contains extended thinking/reasoning content if available.
	Thinking string `json:"thinking,omitempty"`
}

// StopReason indicates why the LLM stopped generating.
type StopReason string

const (
	// StopReasonEndTurn indicates natural completion.
	StopReasonEndTurn StopReason = "end_turn"

	// StopReasonToolUse indicates the LLM wants to use tools.
	StopReasonToolUse StopReason = "tool_use"

	// StopReasonMaxTokens indicates the token limit was reached.
	StopReasonMaxTokens StopReason = "max_tokens"

	// StopReasonStopSequence indicates a stop sequence was encountered.
	StopReasonStopSequence StopReason = "stop_sequence"
)

// Usage contains token usage information.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// HasToolCalls returns true if the response contains tool calls.
func (r *GenerateResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

// ToMessage converts the response to an assistant Message.
func (r *GenerateResponse) ToMessage() Message {
	return Message{
		Role:      RoleAssistant,
		Content:   r.Content,
		ToolCalls: r.ToolCalls,
	}
}
