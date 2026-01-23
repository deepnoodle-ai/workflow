# Reasoning Events

Reasoning events provide observability into AI agent behavior by capturing their thinking, decisions, and tool interactions. Events are emitted via the workflow engine's `EventLog` interface.

## Event Types

```go
const (
    // EventAgentThinking captures the agent's internal reasoning/thinking.
    EventAgentThinking workflow.EventType = "agent_thinking"

    // EventAgentToolCall captures when the agent requests a tool call.
    EventAgentToolCall workflow.EventType = "agent_tool_call"

    // EventAgentToolResult captures the result of a tool execution.
    EventAgentToolResult workflow.EventType = "agent_tool_result"

    // EventAgentDecision captures high-level agent decisions.
    EventAgentDecision workflow.EventType = "agent_decision"

    // EventAgentError captures agent-level errors.
    EventAgentError workflow.EventType = "agent_error"
)
```

## Event Data Records

### ThinkingRecord

Captures agent reasoning/thinking content:

```go
type ThinkingRecord struct {
    // Content is the thinking content.
    Content string `json:"content"`

    // Turn is the conversation turn number.
    Turn int `json:"turn"`

    // Model is the model that produced the thinking.
    Model string `json:"model,omitempty"`
}
```

### ToolCallRecord

Captures a tool invocation:

```go
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
```

### ToolResultRecord

Captures the result of a tool execution:

```go
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
```

### DecisionRecord

Captures high-level agent decisions:

```go
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
```

## Enabling Event Logging

Pass an `EventLog` to the agent activity:

```go
// Create an event log (memory or PostgreSQL)
eventLog := workflow.NewMemoryEventLog()

// Create agent with event logging
agent := ai.NewAgentActivity("assistant", llm, ai.AgentActivityOptions{
    SystemPrompt: "You are a helpful assistant.",
    Tools:        tools,
    EventLog:     eventLog,
})
```

## Querying Events

### List Events

```go
events, err := eventLog.List(ctx, workflow.EventLogListOptions{
    ExecutionID: executionID,
    Types:       []workflow.EventType{ai.EventAgentToolCall},
    Limit:       100,
})
```

### Filter by Type

```go
// Get all tool calls
toolCalls, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    ExecutionID: executionID,
    Types:       []workflow.EventType{ai.EventAgentToolCall},
})

// Get all thinking events
thinking, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    ExecutionID: executionID,
    Types:       []workflow.EventType{ai.EventAgentThinking},
})
```

### Reconstruct Agent Trace

```go
// Get all agent events in order
events, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    ExecutionID: executionID,
    Types: []workflow.EventType{
        ai.EventAgentThinking,
        ai.EventAgentToolCall,
        ai.EventAgentToolResult,
        ai.EventAgentDecision,
    },
})

for _, event := range events {
    switch event.Type {
    case ai.EventAgentThinking:
        var record ai.ThinkingRecord
        json.Unmarshal(event.Data, &record)
        fmt.Printf("Turn %d: Thinking: %s\n", record.Turn, record.Content)

    case ai.EventAgentToolCall:
        var record ai.ToolCallRecord
        json.Unmarshal(event.Data, &record)
        fmt.Printf("Turn %d: Tool call: %s\n", record.Turn, record.ToolName)

    case ai.EventAgentToolResult:
        var record ai.ToolResultRecord
        json.Unmarshal(event.Data, &record)
        fmt.Printf("Turn %d: Tool result: success=%v\n", record.Turn, record.Result.Success)

    case ai.EventAgentDecision:
        var record ai.DecisionRecord
        json.Unmarshal(event.Data, &record)
        fmt.Printf("Decision: %s (confidence: %.2f)\n", record.Decision, record.Confidence)
    }
}
```

## Event Flow During Execution

```
┌────────────────────────────────────────────────────────────────┐
│                      Agent Execution                           │
│                                                                │
│  Turn 1:                                                       │
│    [Thinking Event] - "I need to search for information"       │
│    [ToolCall Event] - search(query: "workflow patterns")       │
│    [ToolResult Event] - Results returned                       │
│                                                                │
│  Turn 2:                                                       │
│    [Thinking Event] - "Now I'll analyze the results"           │
│    [Decision Event] - "Use pattern A based on requirements"    │
│                                                                │
│  Final:                                                        │
│    Response returned to workflow                               │
└────────────────────────────────────────────────────────────────┘
```

## ReasoningCallbacks

The `ReasoningCallbacks` type implements `workflow.ExecutionCallbacks` to automatically emit events:

```go
type ReasoningCallbacks struct {
    eventLog workflow.EventLog
}

func NewReasoningCallbacks(eventLog workflow.EventLog) *ReasoningCallbacks {
    return &ReasoningCallbacks{eventLog: eventLog}
}
```

## Use Cases

### Debugging

Track why an agent made certain decisions:

```go
// Find all decisions for a failed execution
events, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    ExecutionID: failedExecutionID,
    Types:       []workflow.EventType{ai.EventAgentDecision, ai.EventAgentError},
})
```

### Auditing

Create an audit trail of agent actions:

```go
// Export all tool calls for compliance review
events, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    Types: []workflow.EventType{ai.EventAgentToolCall, ai.EventAgentToolResult},
    After: startOfAuditPeriod,
})
```

### Analytics

Analyze agent behavior patterns:

```go
// Count tool usage across all executions
toolUsage := make(map[string]int)
events, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    Types: []workflow.EventType{ai.EventAgentToolCall},
})
for _, event := range events {
    var record ai.ToolCallRecord
    json.Unmarshal(event.Data, &record)
    toolUsage[record.ToolName]++
}
```

### Cost Tracking

Monitor token usage and tool execution time:

```go
// Track tool execution durations
events, _ := eventLog.List(ctx, workflow.EventLogListOptions{
    ExecutionID: executionID,
    Types:       []workflow.EventType{ai.EventAgentToolResult},
})

var totalDuration time.Duration
for _, event := range events {
    var record ai.ToolResultRecord
    json.Unmarshal(event.Data, &record)
    if d, err := time.ParseDuration(record.Duration); err == nil {
        totalDuration += d
    }
}
```

## Best Practices

1. **Enable in production** - Events provide crucial debugging information for agent failures.

2. **Set retention policies** - Configure appropriate retention for PostgreSQL event logs.

3. **Index by execution** - Always query with `ExecutionID` when possible for performance.

4. **Correlate with workflow events** - Agent events complement standard workflow events.

5. **Monitor error events** - Set up alerts for `EventAgentError` occurrences.

6. **Track turn counts** - High turn counts may indicate inefficient agent behavior.
