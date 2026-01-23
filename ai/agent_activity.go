package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// AgentActivityParams is the input for an AgentActivity execution.
type AgentActivityParams struct {
	// Input is the initial user message or task for the agent.
	Input string `json:"input"`

	// Checkpoint is restored conversation state from a previous execution.
	// This is set automatically during recovery.
	Checkpoint *ConversationState `json:"checkpoint,omitempty"`

	// MaxTurns limits the number of LLM turns (default: 10).
	MaxTurns int `json:"max_turns,omitempty"`

	// MaxTokens is the per-turn token limit (passed to LLM).
	MaxTokens int `json:"max_tokens,omitempty"`

	// Temperature controls LLM randomness.
	Temperature *float64 `json:"temperature,omitempty"`
}

// AgentActivityResult is the output of an AgentActivity execution.
type AgentActivityResult struct {
	// Response is the agent's final text response.
	Response string `json:"response"`

	// Conversation is the final conversation state (for checkpointing).
	Conversation *ConversationState `json:"conversation"`

	// TurnsUsed is the number of LLM turns used.
	TurnsUsed int `json:"turns_used"`

	// TotalTokens is the total tokens used across all turns.
	TotalTokens int `json:"total_tokens"`

	// Artifacts contains any artifacts produced during execution.
	Artifacts map[string]any `json:"artifacts,omitempty"`
}

// AgentActivityOptions configures an AgentActivity.
type AgentActivityOptions struct {
	// SystemPrompt is the system message for the agent.
	SystemPrompt string

	// Tools available to the agent.
	Tools map[string]Tool

	// EventLog for recording reasoning events (optional).
	EventLog workflow.EventLog

	// OnToolCall is called before executing each tool (optional).
	OnToolCall func(ctx context.Context, call ToolCall) error

	// OnToolResult is called after executing each tool (optional).
	OnToolResult func(ctx context.Context, call ToolCall, result *ToolResult) error
}

// AgentActivity wraps an AI agent loop as a workflow activity.
// It checkpoints conversation state at each tool call boundary, allowing
// recovery after crashes or restarts.
type AgentActivity struct {
	name         string
	llm          LLMProvider
	systemPrompt string
	tools        map[string]Tool
	durableTools map[string]*DurableTool
	eventLog     workflow.EventLog
	onToolCall   func(ctx context.Context, call ToolCall) error
	onToolResult func(ctx context.Context, call ToolCall, result *ToolResult) error
}

// NewAgentActivity creates a new agent activity.
func NewAgentActivity(name string, llm LLMProvider, opts AgentActivityOptions) *AgentActivity {
	// Wrap all tools as DurableTools for idempotency
	durableTools := make(map[string]*DurableTool)
	for toolName, tool := range opts.Tools {
		durableTools[toolName] = NewDurableTool(tool)
	}

	return &AgentActivity{
		name:         name,
		llm:          llm,
		systemPrompt: opts.SystemPrompt,
		tools:        opts.Tools,
		durableTools: durableTools,
		eventLog:     opts.EventLog,
		onToolCall:   opts.OnToolCall,
		onToolResult: opts.OnToolResult,
	}
}

// Name returns the activity name.
func (a *AgentActivity) Name() string {
	return a.name
}

// Execute runs the agent activity.
// This implements the workflow.Activity interface through TypedActivity adapter.
func (a *AgentActivity) Execute(ctx workflow.Context, params AgentActivityParams) (AgentActivityResult, error) {
	// Initialize or restore conversation state
	conv := params.Checkpoint
	if conv == nil {
		conv = NewConversationState()
		conv.SystemPrompt = a.systemPrompt
		conv.Model = a.llm.Model()
		conv.AddUserMessage(params.Input)
	}

	// Restore durable tool caches from conversation artifacts
	if toolCache, ok := conv.GetArtifact("_tool_cache"); ok {
		if cacheMap, ok := toolCache.(map[string]any); ok {
			for toolName, cache := range cacheMap {
				if dt, exists := a.durableTools[toolName]; exists {
					if toolCacheMap, ok := cache.(map[string]any); ok {
						_ = dt.RestoreCache(toolCacheMap)
					}
				}
			}
		}
	}

	maxTurns := params.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 10
	}

	result := AgentActivityResult{
		Conversation: conv,
		Artifacts:    make(map[string]any),
	}

	// Main agent loop
	for turn := 0; turn < maxTurns; turn++ {
		// Check for context cancellation
		if err := ctx.Err(); err != nil {
			return result, err
		}

		// Build tools list for LLM
		llmTools := make([]Tool, 0, len(a.tools))
		for _, tool := range a.tools {
			llmTools = append(llmTools, tool)
		}

		// Generate LLM response
		genOpts := GenerateOptions{
			SystemPrompt: conv.SystemPrompt,
			Tools:        llmTools,
			MaxTokens:    params.MaxTokens,
			Temperature:  params.Temperature,
		}

		resp, err := a.llm.Generate(ctx, conv.Messages, genOpts)
		if err != nil {
			return result, fmt.Errorf("llm generate: %w", err)
		}

		// Update token counts
		conv.TotalTokens += resp.Usage.TotalTokens
		result.TotalTokens = conv.TotalTokens
		result.TurnsUsed = turn + 1

		// Record thinking if present
		if resp.Thinking != "" && a.eventLog != nil {
			_ = a.eventLog.Append(ctx, workflow.Event{
				ExecutionID: ctx.GetExecutionID(),
				Timestamp:   ctx.Now(),
				Type:        EventAgentThinking,
				StepName:    ctx.GetStepName(),
				PathID:      ctx.GetPathID(),
				Data: map[string]any{
					"thinking": resp.Thinking,
					"turn":     turn,
				},
			})
		}

		// Add assistant message to conversation
		assistantMsg := resp.ToMessage()
		assistantMsg.Timestamp = time.Now()
		conv.AddMessage(assistantMsg)

		// If no tool calls, we're done
		if !resp.HasToolCalls() {
			result.Response = resp.Content
			result.Conversation = conv
			return result, nil
		}

		// Process tool calls
		for _, toolCall := range resp.ToolCalls {
			// Generate deterministic call ID for idempotency
			callID := ctx.DeterministicID("tool")

			// Record tool call event
			if a.eventLog != nil {
				_ = a.eventLog.Append(ctx, workflow.Event{
					ExecutionID: ctx.GetExecutionID(),
					Timestamp:   ctx.Now(),
					Type:        EventAgentToolCall,
					StepName:    ctx.GetStepName(),
					PathID:      ctx.GetPathID(),
					Data: map[string]any{
						"call_id":   callID,
						"tool_name": toolCall.Name,
						"arguments": toolCall.Arguments,
						"turn":      turn,
					},
				})
			}

			// Call onToolCall hook if set
			if a.onToolCall != nil {
				if err := a.onToolCall(ctx, toolCall); err != nil {
					return result, fmt.Errorf("tool call hook: %w", err)
				}
			}

			// Execute the tool via DurableTool for idempotency
			var toolResult *ToolResult
			dt, ok := a.durableTools[toolCall.Name]
			if !ok {
				toolResult = &ToolResult{
					CallID:  toolCall.ID,
					Error:   fmt.Sprintf("unknown tool: %s", toolCall.Name),
					Success: false,
				}
			} else {
				// Execute with durable caching
				toolResult, err = dt.Execute(ctx, callID, toolCall.Arguments)
				if err != nil {
					toolResult = &ToolResult{
						CallID:  toolCall.ID,
						Error:   err.Error(),
						Success: false,
					}
				} else if toolResult != nil {
					toolResult.CallID = toolCall.ID
				}
			}

			// Record tool result event
			if a.eventLog != nil {
				_ = a.eventLog.Append(ctx, workflow.Event{
					ExecutionID: ctx.GetExecutionID(),
					Timestamp:   ctx.Now(),
					Type:        EventAgentToolResult,
					StepName:    ctx.GetStepName(),
					PathID:      ctx.GetPathID(),
					Data: map[string]any{
						"call_id":   callID,
						"tool_name": toolCall.Name,
						"result":    toolResult,
						"turn":      turn,
					},
				})
			}

			// Call onToolResult hook if set
			if a.onToolResult != nil {
				if err := a.onToolResult(ctx, toolCall, toolResult); err != nil {
					return result, fmt.Errorf("tool result hook: %w", err)
				}
			}

			// Add tool result to conversation
			conv.AddToolResult(toolResult)
		}

		// Checkpoint tool caches in conversation artifacts
		toolCache := make(map[string]any)
		for toolName, dt := range a.durableTools {
			toolCache[toolName] = dt.ExportCache()
		}
		conv.SetArtifact("_tool_cache", toolCache)

		// Update checkpoint in params for workflow checkpointing
		// This is the key to recovery - the params are checkpointed by the workflow
		result.Conversation = conv
	}

	// Max turns exceeded
	return result, fmt.Errorf("max turns (%d) exceeded without agent completing", maxTurns)
}

// ToActivity wraps the AgentActivity as a workflow.Activity.
func (a *AgentActivity) ToActivity() workflow.Activity {
	return workflow.NewTypedActivity(a)
}

// AgentActivityParamsFromMap converts a map to AgentActivityParams.
func AgentActivityParamsFromMap(m map[string]any) (AgentActivityParams, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return AgentActivityParams{}, err
	}
	var params AgentActivityParams
	if err := json.Unmarshal(data, &params); err != nil {
		return AgentActivityParams{}, err
	}
	return params, nil
}

// Verify interface compliance.
var _ workflow.TypedActivity[AgentActivityParams, AgentActivityResult] = (*AgentActivity)(nil)
