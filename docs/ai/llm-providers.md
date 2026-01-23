# LLM Providers

The `ai` package defines a generic `LLMProvider` interface that abstracts LLM backends. This allows agents to work with different LLM providers interchangeably.

## LLMProvider Interface

```go
type LLMProvider interface {
    // Generate produces a response from the LLM given messages and options.
    Generate(ctx context.Context, messages []Message, opts GenerateOptions) (*GenerateResponse, error)

    // Name returns the name of the LLM provider (e.g., "anthropic", "openai").
    Name() string

    // Model returns the current model being used (e.g., "claude-3-opus-20240229").
    Model() string
}
```

## StreamingLLMProvider Interface

For providers that support streaming:

```go
type StreamingLLMProvider interface {
    LLMProvider

    // Stream produces a streaming response from the LLM.
    Stream(ctx context.Context, messages []Message, opts GenerateOptions) (StreamIterator, error)
}
```

## GenerateOptions

```go
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
```

## GenerateResponse

```go
type GenerateResponse struct {
    // Content is the text content of the response.
    Content string

    // ToolCalls requested by the LLM.
    ToolCalls []ToolCall

    // StopReason indicates why generation stopped.
    StopReason StopReason

    // Usage contains token counts.
    Usage Usage

    // Model used for generation.
    Model string

    // Thinking contains extended thinking/reasoning content if available.
    Thinking string
}
```

## Stop Reasons

```go
const (
    StopReasonEndTurn      // Natural completion
    StopReasonToolUse      // LLM wants to use tools
    StopReasonMaxTokens    // Token limit reached
    StopReasonStopSequence // Stop sequence encountered
)
```

## Tool Choice Configuration

```go
type ToolChoiceConfig struct {
    Type ToolChoiceType
    Name string // For ToolChoiceTool
}

const (
    ToolChoiceAuto // Let LLM decide
    ToolChoiceAny  // Require at least one tool
    ToolChoiceTool // Force specific tool (set Name)
    ToolChoiceNone // Prevent tool use
)
```

## Dive Integration

The `DiveLLMProvider` adapts the [Dive](https://github.com/deepnoodle-ai/dive) LLM library:

```go
import (
    "github.com/deepnoodle-ai/dive/llm"
    "github.com/deepnoodle-ai/workflow/ai"
)

// Create a Dive LLM instance
diveLLM, err := llm.New(llm.ProviderAnthropic, llm.Options{
    APIKey: os.Getenv("ANTHROPIC_API_KEY"),
    Model:  "claude-3-opus-20240229",
})

// Wrap with DiveLLMProvider
provider := ai.NewDiveLLMProvider(diveLLM, ai.DiveLLMProviderOptions{
    Model:        "claude-3-opus-20240229",
    ProviderName: "anthropic",
})
```

### Features

- Automatic message format conversion
- Tool schema conversion to Wonton format
- Tool choice mapping
- Usage tracking
- Extended thinking support

### Message Conversion

The provider handles conversion between workflow `ai.Message` and Dive `llm.Message`:

- Text content maps to `llm.TextContent`
- Tool calls map to `llm.ToolUseContent`
- Tool results map to `llm.ToolResultContent`
- Thinking content maps to `llm.ThinkingContent`

## Creating Custom Providers

Implement the `LLMProvider` interface for custom backends:

```go
type OpenAIProvider struct {
    client *openai.Client
    model  string
}

func (p *OpenAIProvider) Name() string {
    return "openai"
}

func (p *OpenAIProvider) Model() string {
    return p.model
}

func (p *OpenAIProvider) Generate(ctx context.Context, messages []ai.Message, opts ai.GenerateOptions) (*ai.GenerateResponse, error) {
    // Convert messages to OpenAI format
    openaiMessages := convertToOpenAI(messages)

    // Call OpenAI API
    resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
        Model:       p.model,
        Messages:    openaiMessages,
        MaxTokens:   opts.MaxTokens,
        Temperature: opts.Temperature,
        Tools:       convertToolsToOpenAI(opts.Tools),
    })
    if err != nil {
        return nil, err
    }

    // Convert response back
    return convertFromOpenAI(resp), nil
}
```

## Mock Provider for Testing

```go
type MockLLMProvider struct {
    responses []*ai.GenerateResponse
    index     int
}

func (m *MockLLMProvider) Name() string  { return "mock" }
func (m *MockLLMProvider) Model() string { return "mock-model" }

func (m *MockLLMProvider) Generate(ctx context.Context, messages []ai.Message, opts ai.GenerateOptions) (*ai.GenerateResponse, error) {
    if m.index >= len(m.responses) {
        return &ai.GenerateResponse{
            Content:    "No more responses",
            StopReason: ai.StopReasonEndTurn,
        }, nil
    }
    resp := m.responses[m.index]
    m.index++
    return resp, nil
}
```

## Best Practices

1. **Handle rate limits** - Implement retry logic with exponential backoff.

2. **Track tokens** - Monitor `Usage` to stay within budget and context limits.

3. **Set appropriate temperature** - Use 0 for deterministic tasks, higher for creative ones.

4. **Use tool choice wisely** - `ToolChoiceAny` forces tool use, `ToolChoiceNone` prevents it.

5. **Handle streaming** - For long responses, use streaming to show progress.

6. **Log metadata** - Use `GenerateOptions.Metadata` for tracing and debugging.
