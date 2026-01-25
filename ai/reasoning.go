package ai

import "github.com/deepnoodle-ai/workflow/domain"

// Event types for AI agent reasoning traces.
// These use the existing EventLog interface's Data field for custom content.
const (
	// EventAgentThinking captures the agent's internal reasoning/thinking.
	EventAgentThinking domain.EventType = "agent_thinking"

	// EventAgentToolCall captures when the agent requests a tool call.
	EventAgentToolCall domain.EventType = "agent_tool_call"

	// EventAgentToolResult captures the result of a tool execution.
	EventAgentToolResult domain.EventType = "agent_tool_result"

	// EventAgentDecision captures high-level agent decisions.
	EventAgentDecision domain.EventType = "agent_decision"

	// EventAgentError captures agent-level errors.
	EventAgentError domain.EventType = "agent_error"
)

// DecisionRecord captures a high-level agent decision for auditing.
type DecisionRecord struct {
	// Decision is a short description of the decision made.
	Decision string `json:"decision"`

	// Rationale explains why the decision was made.
	Rationale string `json:"rationale,omitempty"`

	// Alternatives lists other options that were considered.
	Alternatives []string `json:"alternatives,omitempty"`

	// Confidence is a 0-1 score of decision confidence.
	Confidence float64 `json:"confidence,omitempty"`

	// Metadata contains additional context.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ThinkingRecord captures agent thinking/reasoning.
type ThinkingRecord struct {
	// Content is the thinking content.
	Content string `json:"content"`

	// Turn is the conversation turn number.
	Turn int `json:"turn"`

	// Model is the model that produced the thinking.
	Model string `json:"model,omitempty"`
}

// ToolCallRecord captures a tool call.
type ToolCallRecord struct {
	// CallID is the unique identifier for this call.
	CallID string `json:"call_id"`

	// ToolName is the name of the tool being called.
	ToolName string `json:"tool_name"`

	// Arguments are the tool arguments.
	Arguments map[string]any `json:"arguments"`

	// Turn is the conversation turn number.
	Turn int `json:"turn"`
}

// ToolResultRecord captures a tool result.
type ToolResultRecord struct {
	// CallID matches the ToolCallRecord.
	CallID string `json:"call_id"`

	// ToolName is the name of the tool.
	ToolName string `json:"tool_name"`

	// Result is the tool result.
	Result *ToolResult `json:"result"`

	// Duration is how long the tool took to execute.
	Duration string `json:"duration,omitempty"`

	// Turn is the conversation turn number.
	Turn int `json:"turn"`
}
