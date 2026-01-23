package ai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/wonton/schema"
)

// DiveLLMProvider adapts Dive's LLM interface to the workflow AI LLMProvider.
type DiveLLMProvider struct {
	llm         llm.LLM
	model       string
	providerName string
}

// DiveLLMProviderOptions configures a DiveLLMProvider.
type DiveLLMProviderOptions struct {
	// Model is the model name (e.g., "claude-3-opus-20240229").
	Model string

	// ProviderName is the provider name (e.g., "anthropic").
	ProviderName string
}

// NewDiveLLMProvider creates a new DiveLLMProvider wrapping a Dive LLM.
func NewDiveLLMProvider(dllm llm.LLM, opts DiveLLMProviderOptions) *DiveLLMProvider {
	return &DiveLLMProvider{
		llm:         dllm,
		model:       opts.Model,
		providerName: opts.ProviderName,
	}
}

// Name returns the provider name.
func (d *DiveLLMProvider) Name() string {
	if d.providerName != "" {
		return d.providerName
	}
	return d.llm.Name()
}

// Model returns the model name.
func (d *DiveLLMProvider) Model() string {
	return d.model
}

// Generate produces a response from the LLM.
func (d *DiveLLMProvider) Generate(ctx context.Context, messages []Message, opts GenerateOptions) (*GenerateResponse, error) {
	// Convert our messages to Dive messages
	diveMessages := convertToDiveMessages(messages)

	// Build Dive options
	diveOpts := []llm.Option{
		llm.WithMessages(diveMessages...),
	}

	if opts.SystemPrompt != "" {
		diveOpts = append(diveOpts, llm.WithSystemPrompt(opts.SystemPrompt))
	}
	if opts.MaxTokens > 0 {
		diveOpts = append(diveOpts, llm.WithMaxTokens(opts.MaxTokens))
	}
	if opts.Temperature != nil {
		diveOpts = append(diveOpts, llm.WithTemperature(*opts.Temperature))
	}
	if len(opts.Tools) > 0 {
		diveTools := convertToDiveTools(opts.Tools)
		diveOpts = append(diveOpts, llm.WithTools(diveTools...))
	}
	if opts.ToolChoice != nil {
		diveOpts = append(diveOpts, llm.WithToolChoice(convertToDiveToolChoice(opts.ToolChoice)))
	}

	// Call Dive
	resp, err := d.llm.Generate(ctx, diveOpts...)
	if err != nil {
		return nil, fmt.Errorf("dive generate: %w", err)
	}

	// Convert response back
	return convertFromDiveResponse(resp), nil
}

// convertToDiveMessages converts workflow AI messages to Dive messages.
func convertToDiveMessages(messages []Message) []*llm.Message {
	result := make([]*llm.Message, 0, len(messages))

	for _, msg := range messages {
		diveMsg := &llm.Message{
			Role:    llm.Role(msg.Role),
			Content: make([]llm.Content, 0),
		}

		// Add text content if present
		if msg.Content != "" {
			diveMsg.Content = append(diveMsg.Content, &llm.TextContent{Text: msg.Content})
		}

		// Add tool calls as content
		for _, tc := range msg.ToolCalls {
			input, _ := json.Marshal(tc.Arguments)
			diveMsg.Content = append(diveMsg.Content, &llm.ToolUseContent{
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}

		// Add tool result as content
		if msg.ToolResult != nil {
			content := msg.ToolResult.Output
			if msg.ToolResult.Error != "" {
				content = msg.ToolResult.Error
			}
			diveMsg.Content = append(diveMsg.Content, &llm.ToolResultContent{
				ToolUseID: msg.ToolResult.CallID,
				Content:   content,
				IsError:   !msg.ToolResult.Success,
			})
		}

		result = append(result, diveMsg)
	}

	return result
}

// convertToDiveTools converts workflow AI tools to Dive tools.
func convertToDiveTools(tools []Tool) []llm.Tool {
	result := make([]llm.Tool, 0, len(tools))

	for _, tool := range tools {
		diveTool := llm.NewToolDefinition().
			WithName(tool.Name()).
			WithDescription(tool.Description()).
			WithSchema(convertToWontonSchema(tool.Schema()))
		result = append(result, diveTool)
	}

	return result
}

// convertToWontonSchema converts our ToolSchema to Wonton schema.
func convertToWontonSchema(ts *ToolSchema) *schema.Schema {
	if ts == nil {
		return nil
	}

	s := &schema.Schema{
		Type:     schema.SchemaType(ts.Type),
		Required: ts.Required,
	}

	if ts.Properties != nil {
		s.Properties = make(map[string]*schema.Property)
		for name, prop := range ts.Properties {
			s.Properties[name] = convertPropertyToWonton(prop)
		}
	}

	if ts.Items != nil {
		s.Items = convertToolSchemaToProperty(ts.Items)
	}

	return s
}

// convertToolSchemaToProperty converts a ToolSchema to Wonton Property.
func convertToolSchemaToProperty(ts *ToolSchema) *schema.Property {
	if ts == nil {
		return nil
	}

	p := &schema.Property{
		Type:     schema.SchemaType(ts.Type),
		Required: ts.Required,
	}

	if ts.Properties != nil {
		p.Properties = make(map[string]*schema.Property)
		for name, prop := range ts.Properties {
			p.Properties[name] = convertPropertyToWonton(prop)
		}
	}

	if ts.Items != nil {
		p.Items = convertToolSchemaToProperty(ts.Items)
	}

	if ts.Enum != nil {
		p.Enum = make([]any, len(ts.Enum))
		for i, v := range ts.Enum {
			p.Enum[i] = v
		}
	}

	return p
}

// convertPropertyToWonton converts our Property to Wonton Property.
func convertPropertyToWonton(p *Property) *schema.Property {
	if p == nil {
		return nil
	}

	wp := &schema.Property{
		Type:        schema.SchemaType(p.Type),
		Description: p.Description,
		Required:    p.Required,
	}

	if p.Enum != nil {
		wp.Enum = make([]any, len(p.Enum))
		for i, v := range p.Enum {
			wp.Enum[i] = v
		}
	}

	if p.Items != nil {
		wp.Items = convertPropertyToWonton(p.Items)
	}

	if p.Properties != nil {
		wp.Properties = make(map[string]*schema.Property)
		for name, prop := range p.Properties {
			wp.Properties[name] = convertPropertyToWonton(prop)
		}
	}

	return wp
}

// convertToDiveToolChoice converts our ToolChoiceConfig to Dive's ToolChoice.
func convertToDiveToolChoice(tc *ToolChoiceConfig) *llm.ToolChoice {
	if tc == nil {
		return nil
	}

	switch tc.Type {
	case ToolChoiceAuto:
		return llm.ToolChoiceAuto
	case ToolChoiceAny:
		return llm.ToolChoiceAny
	case ToolChoiceNone:
		return llm.ToolChoiceNone
	case ToolChoiceTool:
		return &llm.ToolChoice{
			Type: llm.ToolChoiceTypeTool,
			Name: tc.Name,
		}
	default:
		return llm.ToolChoiceAuto
	}
}

// convertFromDiveResponse converts a Dive response to our GenerateResponse.
func convertFromDiveResponse(resp *llm.Response) *GenerateResponse {
	result := &GenerateResponse{
		StopReason: convertStopReason(resp.StopReason),
		Model:      resp.Model,
		Usage: Usage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			TotalTokens:  resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}

	// Extract content and tool calls from response
	for _, content := range resp.Content {
		switch c := content.(type) {
		case *llm.TextContent:
			result.Content = c.Text
		case *llm.ThinkingContent:
			result.Thinking = c.Thinking
		case *llm.ToolUseContent:
			var args map[string]any
			if len(c.Input) > 0 {
				_ = json.Unmarshal(c.Input, &args)
			}
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: args,
			})
		}
	}

	return result
}

// convertStopReason converts Dive stop reason to our StopReason.
func convertStopReason(reason string) StopReason {
	switch reason {
	case "end_turn":
		return StopReasonEndTurn
	case "tool_use":
		return StopReasonToolUse
	case "max_tokens":
		return StopReasonMaxTokens
	case "stop_sequence":
		return StopReasonStopSequence
	default:
		return StopReasonEndTurn
	}
}

// Verify interface compliance.
var _ LLMProvider = (*DiveLLMProvider)(nil)
