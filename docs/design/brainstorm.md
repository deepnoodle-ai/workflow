# Brainstorm: AI-Native Workflow Engine

What would a workflow engine designed to be AI-native—easily usable by and for AI agents—look like?

---

## The Core Tension

Traditional workflow engines (Temporal, Airflow, Step Functions) were designed for:
- Human programmers defining workflows in code
- Deterministic, predictable execution paths
- Structured data flowing between steps
- Clear success/failure semantics

AI agents operate differently:
- Dynamic, emergent behavior based on context
- Reasoning that adapts mid-execution
- Unstructured conversations as primary state
- Probabilistic outcomes with fuzzy success criteria

**The question**: Can we design a workflow engine that embraces both paradigms?

---

## Divergent Thinking: Raw Ideas

### 1. Conversation as First-Class State

**Idea**: The conversation history IS the workflow state.

Instead of:
```go
type WorkflowState struct {
    OrderID     string
    Items       []Item
    TotalPrice  float64
}
```

Consider:
```go
type AgentState struct {
    Messages     []llm.Message  // The conversation IS the state
    Artifacts    []Artifact     // Things the agent created/discovered
    Permissions  []Permission   // What the agent is allowed to do
}
```

**Why this matters**: AI agents reason through conversation. Checkpointing a conversation checkpoint captures the agent's entire reasoning context. Recovery means resuming mid-thought.

**Sprites angle**: Each agent runs in an isolated Sprite. The conversation history is checkpointed with the VM state. 300ms checkpoint = capture the agent mid-inference.

---

### 2. Tool Calls as Workflow Steps

**Idea**: Every tool call is implicitly a checkpoint boundary.

```go
// Traditional: explicit activities
workflow.Activity("fetch_data", &FetchActivity{})
workflow.Activity("process", &ProcessActivity{})

// AI-native: tools ARE the activities
agent.Tool("read_file", &ReadFileTool{})
agent.Tool("write_code", &WriteCodeTool{})
agent.Tool("run_tests", &RunTestsTool{})
// Each tool call triggers checkpoint
```

**The insight**: LLM tool calls already have the properties we need:
- Clear input/output boundaries
- Named operations with schemas
- Natural idempotency points

**Dive integration**: `dive.Tool` becomes the primitive. The workflow engine wraps tool execution with checkpointing, retries, and persistence.

---

### 3. Intent-Based Workflows

**Idea**: Define workflows by desired outcome, not step sequence.

```go
// Traditional: imperative sequence
workflow := NewWorkflow("process_order").
    Path("main").
        Activity("validate", &ValidateActivity{}).
        Activity("charge", &ChargeActivity{}).
        Activity("fulfill", &FulfillActivity{}).
    Build()

// AI-native: declarative intent
workflow := NewIntentWorkflow("process_order").
    Goal("Order is validated, charged, and shipped").
    Constraints(
        "Customer card must be charged exactly once",
        "Inventory must be reserved before charging",
        "Shipping label must be generated after payment",
    ).
    Tools(validateTool, chargeTool, inventoryTool, shippingTool).
    Build()
```

**The engine's job**: Provide the AI agent with tools + goal + constraints, let it figure out the path. The engine handles durability, the agent handles planning.

**Wild implication**: Workflows become self-healing. If a step fails, the agent reasons about alternatives rather than following a fixed retry policy.

---

### 4. Multi-Agent Orchestration Patterns

**Idea**: First-class support for agent-to-agent workflows.

```go
// Supervisor pattern: one agent delegates to specialists
supervisor := NewSupervisorAgent("project_manager").
    Delegates(
        NewWorkerAgent("coder").WithTools(codingTools...),
        NewWorkerAgent("reviewer").WithTools(reviewTools...),
        NewWorkerAgent("tester").WithTools(testTools...),
    ).
    Coordinates("Implement the feature described in the ticket")

// Pipeline pattern: agents hand off context
pipeline := NewAgentPipeline("content_creation").
    Stage("research", researchAgent).
    Stage("outline", outlineAgent).
    Stage("draft", draftAgent).
    Stage("edit", editAgent)

// Debate pattern: agents argue to consensus
debate := NewDebateWorkflow("architecture_decision").
    Participants(conservativeAgent, innovativeAgent, practicalAgent).
    Moderator(seniorArchitectAgent).
    UntilConsensus()
```

**Sprites synergy**: Each agent runs in its own Sprite. Isolation = security. Checkpoints = durable handoffs. Network policies = controlled communication.

---

### 5. Reasoning Traces as Event Logs

**Idea**: The AI's chain-of-thought becomes the audit trail.

```go
type ReasoningEvent struct {
    Timestamp    time.Time
    AgentID      string
    Type         ReasoningEventType  // thought, observation, action, reflection
    Content      string              // The actual reasoning
    Confidence   float64             // How sure was the agent
    Alternatives []string            // What else was considered
}
```

**Why**: Traditional event logs capture WHAT happened. AI event logs capture WHY it happened. This enables:
- Debugging agent decisions
- Training data extraction
- Human review of reasoning
- Compliance with AI transparency requirements

---

### 6. Adaptive Checkpointing

**Idea**: Checkpoint frequency based on inference cost, not time.

```go
type CheckpointPolicy interface {
    ShouldCheckpoint(ctx AgentContext) bool
}

// Cost-based: checkpoint after expensive operations
type TokenCostPolicy struct {
    MaxTokensBeforeCheckpoint int
}

// Semantic: checkpoint at "natural" reasoning boundaries
type SemanticPolicy struct {
    Model           llm.LLM
    CheckpointHint  string  // "Has the agent completed a logical unit of work?"
}

// Confidence-based: checkpoint when agent is uncertain
type ConfidencePolicy struct {
    Threshold float64  // Checkpoint if confidence drops below this
}
```

**Rationale**: LLM inference is expensive. Optimal checkpoint placement minimizes re-inference on recovery.

---

### 7. Tool Capability Discovery

**Idea**: Agents discover available tools dynamically, not statically.

```go
// Static (traditional)
agent := NewAgent(tools...)

// Dynamic (AI-native)
agent := NewAgent().
    WithToolDiscovery(
        MCPRegistry("https://tools.company.com/mcp"),
        LocalToolkit(fileTools, codeTools),
        SpriteToolkit(sprite.ID),  // Tools available in this Sprite
    )
```

**MCP integration**: Model Context Protocol already defines tool discovery. The workflow engine becomes an MCP host that manages tool availability per execution context.

**Security angle**: Different workflow executions get different tool sets based on permissions, not code changes.

---

### 8. Human-in-the-Loop as First Class

**Idea**: Pausing for human input is a native workflow primitive.

```go
// Traditional: awkward webhook + polling
workflow.Activity("notify_human", &WebhookActivity{})
workflow.Activity("poll_response", &PollActivity{})

// AI-native: native await
decision := workflow.AwaitHuman(HumanRequest{
    Question:  "Should we proceed with deployment?",
    Timeout:   24 * time.Hour,
    Escalate:  "manager@company.com",
    Context:   agent.ReasoningTrace(),  // Show them WHY
})
```

**Implementation**: The workflow checkpoints and the Sprite hibernates. When human responds, Sprite wakes with sub-second restore.

---

### 9. Forking and Speculative Execution

**Idea**: Agents can fork to explore alternatives in parallel.

```go
// Agent is uncertain about approach
fork := workflow.Fork("explore_implementations", 3)

// Each fork runs in isolated Sprite
results := fork.Execute(func(ctx ForkContext) any {
    agent := ctx.Agent()
    agent.SetTemperature(0.9)  // Different approaches
    return agent.Implement(ctx.Goal())
})

// Evaluate and pick best
best := workflow.Evaluate(results, evaluationCriteria)
```

**Sprites power**: Forking is just Sprite cloning. Copy-on-write means near-instant fork. Each fork can diverge wildly without affecting others.

---

### 10. Progressive Delegation

**Idea**: Start simple, escalate complexity as needed.

```go
executor := NewProgressiveExecutor().
    First(cheapFastModel).     // Try Haiku first
    IfStuck(mediumModel).      // Escalate to Sonnet
    IfStillStuck(expertModel). // Escalate to Opus
    WithHumanFallback()        // Finally, ask a human
```

**Cost optimization**: Most workflows don't need Opus. Start cheap, escalate only when necessary. The workflow engine tracks which model succeeded for future routing.

---

### 11. Semantic Versioning for Agent Prompts

**Idea**: Treat system prompts like code—versioned, tested, deployed.

```go
type AgentPromptVersion struct {
    Version     string
    SystemPrompt string
    Tools       []ToolRef
    Constraints []string
    ValidatedOn time.Time
    Metrics     PromptMetrics  // Success rate, avg tokens, etc.
}

// Engine routes executions to appropriate prompt versions
workflow := NewWorkflow().
    Agent("processor").
        PromptVersion("v2.3.1").  // Pin to specific version
        PromptCanary("v2.4.0", 10). // 10% traffic to new version
```

**Why**: Agent behavior is defined by prompts. Prompts need the same rigor as code: versioning, testing, gradual rollout, rollback.

---

### 12. Context Window as Memory Tier

**Idea**: Explicit management of what's in context vs. retrievable.

```go
type MemoryTier int

const (
    TierImmediate  MemoryTier = iota  // In current context window
    TierRetrievable                    // Can be fetched if needed
    TierArchived                       // Requires explicit load
)

agent.Remember("critical_decision", decision, TierImmediate)
agent.Remember("background_research", research, TierRetrievable)
agent.Remember("historical_data", history, TierArchived)
```

**Checkpointing integration**: Different tiers have different checkpoint strategies. Immediate context is always checkpointed. Retrievable might be stored separately. Archived is just a reference.

---

### 13. Workflow Composition via Natural Language

**Idea**: Define workflow structure through conversation.

```
Human: "I need a workflow that processes customer refunds"

Agent: "I'll create a refund processing workflow. Let me understand the requirements:
1. What triggers a refund request?
2. What approvals are needed?
3. What systems need to be updated?
..."

// The conversation becomes the workflow definition
workflow := CompileWorkflowFromConversation(conversation)
```

**Meta-level**: The workflow engine itself is an agent that builds workflows. We're building the tool that agents use to create other agents' workflows.

---

### 14. Execution Replay for Debugging

**Idea**: Re-run an execution with modified parameters for debugging.

```go
// Original execution failed
original := engine.Get(ctx, "exec-123")

// Replay with modifications
replay := engine.Replay(ctx, original.ID, ReplayOptions{
    ModifyInput: func(input map[string]any) map[string]any {
        input["debug"] = true
        return input
    },
    InjectAtStep: map[string]any{
        "after:validate": mockData,  // Inject mock data mid-workflow
    },
    Breakpoints: []string{"charge"},  // Pause before this step
})
```

**AI debugging**: When an agent makes a bad decision, replay the execution with the same context to understand why. Tweak prompts, try different models, observe different outcomes.

---

### 15. Capability-Based Security

**Idea**: Fine-grained permissions based on what agents CAN do, not who they ARE.

```go
type Capability struct {
    Resource    string        // "file:/tmp/*", "http://api.company.com/*"
    Operations  []string      // ["read", "write", "execute"]
    Constraints []Constraint  // Rate limits, quotas, approval requirements
}

agent := NewAgent().
    WithCapabilities(
        cap.FileSystem("/workspace/*", "rw"),
        cap.Network("*.company.com", "https"),
        cap.Tool("dangerous_tool").RequiresApproval(),
    )
```

**Sprites integration**: Capabilities map to Sprite network policies and filesystem mounts. The workflow engine enforces capabilities; Sprites provide isolation.

---

### 16. Cost Attribution and Budgeting

**Idea**: Built-in tracking and limits for AI costs.

```go
workflow := NewWorkflow().
    Budget(Budget{
        MaxTokens:   100_000,
        MaxDollars:  5.00,
        MaxDuration: 10 * time.Minute,
    }).
    OnBudgetWarning(0.8, notifyOwner).
    OnBudgetExceeded(pauseAndEscalate)

// During execution
ctx.ReportUsage(Usage{
    Tokens:  tokens,
    Model:   model,
    Cost:    cost,
})
```

**Why essential**: AI workflows can be expensive. Cost visibility and control must be built-in, not bolted-on.

---

### 17. Streaming Intermediate Results

**Idea**: Expose agent thinking in real-time, not just final results.

```go
// Subscribe to execution stream
stream := engine.Stream(ctx, executionID)

for event := range stream {
    switch e := event.(type) {
    case *ThinkingEvent:
        ui.ShowReasoning(e.Content)
    case *ToolCallEvent:
        ui.ShowToolCall(e.Tool, e.Input)
    case *ToolResultEvent:
        ui.ShowToolResult(e.Output)
    case *DecisionEvent:
        ui.ShowDecision(e.Choice, e.Alternatives)
    }
}
```

**UX implication**: Users see the agent working, not a black box. Builds trust, enables early intervention.

---

### 18. Self-Improving Workflows

**Idea**: Workflows that learn from their own execution history.

```go
workflow := NewAdaptiveWorkflow("customer_support").
    LearnFrom(
        SuccessfulExecutions(workflow.ID),
        UserFeedback(workflow.ID),
    ).
    Optimize(
        ReduceTokenUsage(),
        ImproveSuccessRate(),
        MinimizeLatency(),
    )
```

**Implementation**: After N executions, analyze patterns. Which prompts work better? Which tool sequences succeed? Automatically tune parameters or suggest improvements.

---

### 19. Semantic Retry Policies

**Idea**: Retry logic that understands WHY something failed.

```go
// Traditional: fixed retry policy
retryPolicy := Exponential(3, time.Second)

// AI-native: semantic retry
retryPolicy := SemanticRetry{
    Analyzer:     errorAnalyzerModel,
    Strategies: map[ErrorCategory]Strategy{
        RateLimited:    BackoffAndRetry(time.Minute),
        InvalidInput:   ReformulateAndRetry(),
        Hallucination:  ReduceTemperatureAndRetry(),
        Confused:       AddContextAndRetry(),
        Impossible:     EscalateToHuman(),
    },
}
```

**The insight**: AI errors are semantic, not just technical. "API returned 429" vs "The agent hallucinated a non-existent function" require different retry strategies.

---

### 20. Workflow Templates as Agents

**Idea**: Templates aren't static—they're agents that customize themselves.

```go
// Request a workflow from a template agent
req := TemplateRequest{
    BaseTemplate: "data_pipeline",
    Requirements: "Process customer data from Salesforce, enrich with Clearbit, load to Snowflake",
    Constraints:  "Must handle PII according to GDPR",
}

customized := templateAgent.Customize(ctx, req)
// Returns a workflow tailored to the requirements
```

**Why**: Static templates never quite fit. An agent can understand requirements and generate appropriate workflows.

---

## Convergent Thinking: Themes

Looking at the 20 ideas above, several themes emerge:

### Theme A: Conversation-Centric State
Ideas 1, 5, 17 center on treating AI reasoning as first-class state. The conversation IS the workflow.

### Theme B: Dynamic Tool Orchestration
Ideas 2, 7, 15 focus on tools as the primitive, with dynamic discovery and capability-based access.

### Theme C: Multi-Agent Patterns
Ideas 4, 9, 10 explore how multiple agents coordinate—supervisor/worker, pipelines, debates, forking.

### Theme D: Adaptive Behavior
Ideas 3, 6, 18, 19 embrace non-determinism: intent-based goals, semantic retries, self-improvement.

### Theme E: Human-AI Collaboration
Ideas 8, 13, 14 integrate humans naturally into the workflow, not as exceptions.

### Theme F: Operational Excellence
Ideas 11, 12, 16, 20 address production concerns: versioning, costs, memory management, templates.

---

## Top 3 Ideas to Develop

Based on feasibility, impact, and alignment with existing architecture:

### 1. Tool Calls as Checkpoint Boundaries (Idea #2)

**Problem solved**: Bridge the gap between LLM tool calling and durable workflows.

**Who benefits**: Anyone building AI agents that need reliability.

**Next steps**:
- Define `DurableTool` interface that wraps `dive.Tool`
- Implement automatic checkpointing after tool calls
- Integrate with existing `Checkpointer` interface
- Test with Dive's tool execution patterns

**Technical sketch**:
```go
// DurableTool wraps a dive.Tool with checkpoint semantics
type DurableTool struct {
    tool         dive.Tool
    checkpointer Checkpointer
}

func (d *DurableTool) Execute(ctx workflow.Context, input map[string]any) (any, error) {
    // Check if we have a cached result (idempotency)
    if cached := d.checkpointer.GetToolResult(ctx.ExecutionID(), d.tool.Name()); cached != nil {
        return cached, nil
    }

    // Execute the tool
    result, err := d.tool.Call(ctx, input)
    if err != nil {
        return nil, err
    }

    // Checkpoint the result
    d.checkpointer.SaveToolResult(ctx.ExecutionID(), d.tool.Name(), result)
    return result, nil
}
```

### 2. Agent-as-Workflow-Step Primitive (Ideas #4 + #9)

**Problem solved**: Treat AI agents as composable workflow components.

**Who benefits**: Teams building complex multi-agent systems.

**Next steps**:
- Define `AgentStep` that wraps a Dive agent as a workflow activity
- Implement agent forking using Sprite cloning
- Design inter-agent message passing
- Handle agent failures and escalation

**Technical sketch**:
```go
// AgentStep wraps an AI agent as a workflow step
type AgentStep struct {
    name        string
    llm         llm.LLM
    tools       []dive.Tool
    systemPrompt string
    maxTurns    int
}

func (a *AgentStep) Execute(ctx workflow.Context) error {
    // Create agent execution in Sprite
    sprite := ctx.SpritesEnvironment().Create(a.spriteConfig())
    defer sprite.Destroy()

    // Run agent loop
    for turn := 0; turn < a.maxTurns; turn++ {
        response, err := a.llm.Generate(ctx, ...)
        if err != nil {
            return err
        }

        // Process tool calls, checkpoint after each
        for _, toolCall := range response.ToolCalls {
            result := a.executeToolWithCheckpoint(ctx, toolCall)
            // ...
        }

        if response.StopReason == "end_turn" {
            break
        }
    }
    return nil
}
```

### 3. Semantic Reasoning Traces (Idea #5)

**Problem solved**: Visibility into WHY agents made decisions.

**Who benefits**: Anyone debugging, auditing, or improving AI agents.

**Next steps**:
- Extend `EventLog` interface for reasoning events
- Capture chain-of-thought during agent execution
- Build query API for reasoning analysis
- Design UI for trace visualization

**Technical sketch**:
```go
type ReasoningTrace struct {
    ExecutionID string
    AgentID     string
    Events      []ReasoningEvent
}

type ReasoningEvent struct {
    Timestamp   time.Time
    Type        ReasoningType  // observation, thought, action, reflection
    Content     string
    TokenUsage  int
    Model       string
    Confidence  float64  // If model provides calibration
    Context     map[string]any
}

// Capture during agent execution
func (a *AgentStep) captureReasoning(response *llm.Response) {
    for _, content := range response.Content {
        switch c := content.(type) {
        case *llm.TextContent:
            a.trace.Add(ReasoningEvent{
                Type:    ReasoningTypeThought,
                Content: c.Text,
            })
        case *llm.ToolUseContent:
            a.trace.Add(ReasoningEvent{
                Type:    ReasoningTypeAction,
                Content: fmt.Sprintf("Call %s(%v)", c.Name, c.Input),
            })
        }
    }
}
```

---

## Open Questions

1. **State serialization**: How do we checkpoint LLM conversation state efficiently? Full message history? Compressed summaries?

2. **Tool idempotency**: Not all tools are idempotent. How do we handle tools with side effects on replay?

3. **Context window limits**: What happens when conversation state exceeds context limits? Automatic summarization?

4. **Cost allocation**: In multi-agent workflows, how do we attribute costs to specific workflow paths?

5. **Determinism trade-offs**: Do we enforce deterministic behavior (like Temporal) or embrace non-determinism with appropriate safeguards?

6. **Human trust**: How do we build UX that gives humans appropriate visibility and control without overwhelming them?

---

## Integration Points

### With Dive
- `dive.Tool` → `DurableTool` wrapper
- `llm.LLM` → Agent execution primitive
- `mcp.Manager` → Dynamic tool discovery
- Streaming → Real-time execution visibility

### With Sprites
- VM isolation → Agent security boundaries
- Checkpointing → Durable agent state
- Network policies → Capability enforcement
- Clone/fork → Speculative execution

### With Existing Workflow Engine
- `ExecutionStore` → Agent state persistence
- `WorkQueue` → Agent task distribution
- `Checkpointer` → Tool result caching
- `EngineCallbacks` → Reasoning trace capture

---

## Next Steps

1. **Prototype**: Build a minimal `AgentActivity` that wraps a Dive agent as a workflow step
2. **Test**: Run agents in Sprites with checkpoint/restore
3. **Measure**: Instrument checkpoint overhead and recovery time
4. **Iterate**: Based on learnings, refine the model

---

## Part 2: Three Architectural Perspectives

The relationship between workflows and agents can be viewed from three distinct angles. Each perspective leads to different design decisions and unlocks different capabilities.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                                                                         │
│   Perspective 1:        Perspective 2:        Perspective 3:            │
│   Workflow ABOVE        Workflow = Agent      Workflow BELOW            │
│                                                                         │
│   ┌──────────┐          ┌──────────┐          ┌──────────┐              │
│   │ Workflow │          │ Workflow │          │  Agent   │              │
│   │ Engine   │          │    =     │          │          │              │
│   └────┬─────┘          │  Agent   │          └────┬─────┘              │
│        │                │  Brain   │               │                    │
│   ┌────┴────┐           └──────────┘          ┌────┴─────┐              │
│   │ Agent A │                                 │ Workflow │              │
│   │ Agent B │                                 │ (as tool)│              │
│   │ Agent C │                                 └──────────┘              │
│   └─────────┘                                                           │
│                                                                         │
│   Orchestrator          The workflow IS       Agents invoke             │
│   manages agents        the agent itself      workflows as tools        │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

---

### Perspective 1: Workflow Orchestrates Agents (Workflow Above)

**Mental model**: The workflow is the "conductor" and agents are "musicians" in an orchestra.

This is the most intuitive framing—similar to how traditional workflows orchestrate microservices. The workflow provides:
- Overall structure and sequencing
- Durability and recovery
- Resource management and scheduling
- Inter-agent communication channels

Agents provide:
- Intelligence and reasoning
- Dynamic problem-solving
- Natural language understanding

#### Design Implications

**Agent as Activity**: Agents become a special type of workflow activity.

```go
workflow := NewWorkflow("code_review_pipeline").
    Path("main").
        AgentActivity("analyze", &CodeAnalyzer{
            Model:  sonnet,
            Tools:  analysisTools,
            Prompt: "Analyze this code for bugs and style issues",
        }).
        AgentActivity("suggest", &CodeSuggester{
            Model:  sonnet,
            Tools:  refactoringTools,
            Prompt: "Suggest improvements based on the analysis",
        }).
        AgentActivity("apply", &CodeApplier{
            Model:    haiku,  // Cheaper model for mechanical task
            Tools:    editTools,
            Prompt:   "Apply the approved suggestions",
            Approval: RequireHuman(),  // Gate before changes
        }).
    Build()
```

**Structured Handoffs**: Data flows between agents through typed interfaces.

```go
type AnalysisResult struct {
    Issues      []Issue
    Suggestions []Suggestion
    Confidence  float64
}

// Workflow enforces the contract
analyzeStep := AgentActivity[CodeInput, AnalysisResult]("analyze", ...)
suggestStep := AgentActivity[AnalysisResult, Suggestions]("suggest", ...)
```

**Failure Boundaries**: The workflow decides what happens when an agent fails.

```go
workflow.Path("main").
    AgentActivity("risky_analysis", analyzer).
        OnFailure(
            Retry(3),
            ThenEscalate(expertAgent),
            ThenFallback(humanReview),
        )
```

#### New Ideas from This Perspective

**21. Agent Pools with Specialization**

```go
// Define a pool of specialized agents
pool := NewAgentPool("code_experts").
    Add(rustExpert, Specialization("*.rs")).
    Add(goExpert, Specialization("*.go")).
    Add(pythonExpert, Specialization("*.py")).
    Add(generalist, Fallback())

// Workflow routes to appropriate agent
workflow.Path("main").
    AgentFromPool("review", pool, routing.ByFileExtension())
```

**22. Agent SLAs and Deadlines**

```go
// Agents have performance contracts
workflow.AgentActivity("quick_triage", triageAgent).
    SLA(SLA{
        MaxDuration: 30 * time.Second,
        MaxTokens:   5000,
        MaxCost:     0.10,
    }).
    OnSLABreach(escalateToFasterAgent)
```

**23. Conversation Bridges Between Agents**

```go
// Agents can "talk" to each other through the workflow
workflow.Path("debate").
    AgentActivity("propose", proposer).
    AgentActivity("critique", critic).
        WithContext(FromPreviousAgent()).  // See proposer's reasoning
    AgentActivity("synthesize", synthesizer).
        WithContext(FromAllPrevious())     // See entire debate
```

#### Sprites Integration (Workflow Above)

Each agent runs in its own Sprite:

```go
type AgentActivity struct {
    spriteConfig SpriteConfig
    // ...
}

func (a *AgentActivity) Execute(ctx workflow.Context) error {
    // Spawn isolated Sprite for this agent
    sprite, err := sprites.Create(ctx, a.spriteConfig)
    if err != nil {
        return err
    }
    defer sprite.Destroy()

    // Agent runs in isolation
    result, err := sprite.Run(ctx, a.agentBinary, a.input)

    // Checkpoint after agent completes
    ctx.Checkpoint()

    return err
}
```

**Power move**: Fork the Sprite to run multiple agents in parallel with identical starting state.

---

### Perspective 2: Workflow IS the Agent (Workflow = Agent)

**Mental model**: The workflow primitives ARE the agent's cognitive architecture. A workflow doesn't *contain* an agent—it *is* the agent's "brain".

This is the most radical reframing. What if we map workflow concepts to cognitive concepts?

| Workflow Concept | Cognitive Equivalent |
|------------------|----------------------|
| `Path` | A chain of thought |
| `Activity` | A cognitive operation (perceive, reason, act) |
| `Fork` | Consider multiple hypotheses |
| `Join` | Synthesize conclusions |
| `Checkpoint` | Commit to working memory |
| `Timer` | Deliberate pause / incubation |
| `State` | Working memory |
| `Input/Output` | Perception / Action |

#### Design Implications

**Cognitive Primitives**: Workflow steps map to thinking operations.

```go
// An agent IS a workflow
agent := NewCognitiveWorkflow("problem_solver").

    // Perception phase
    Perceive("observe", func(ctx Context, input any) Observation {
        return ctx.LLM().Observe(input)
    }).

    // Reasoning phase - can branch
    Fork("hypothesize", 3, func(ctx ForkContext) Hypothesis {
        return ctx.LLM().Generate("What could explain this?")
    }).

    // Evaluation phase - join branches
    Join("evaluate", func(ctx Context, hypotheses []Hypothesis) Hypothesis {
        return ctx.LLM().Evaluate(hypotheses)
    }).

    // Action phase
    Act("respond", func(ctx Context, conclusion Hypothesis) Action {
        return ctx.LLM().DecideAction(conclusion)
    }).

    // Reflection phase (optional)
    Reflect("learn", func(ctx Context, action Action, outcome Outcome) {
        ctx.Memory().Store(lesson)
    }).

    Build()
```

**Memory as Workflow State**: The agent's memory IS the workflow's state.

```go
type CognitiveState struct {
    // Working memory (in context window)
    Attention     []Observation
    CurrentGoal   Goal
    ActivePlan    Plan

    // Episodic memory (checkpointed)
    RecentEvents  []Event

    // Semantic memory (retrievable)
    Knowledge     VectorStore

    // Procedural memory (the workflow definition itself!)
    Skills        []Workflow
}
```

**Metacognition as Workflow Control**: The agent can reason about its own workflow.

```go
// The agent can modify its own execution
agent.Path("metacognition").
    Activity("assess", func(ctx Context) {
        if ctx.IsStuck() {
            ctx.Workflow().InsertStep("seek_help", askForHelp)
        }
        if ctx.IsTooSlow() {
            ctx.Workflow().SkipTo("quick_answer")
        }
        if ctx.HasNewInfo() {
            ctx.Workflow().Restart("perceive")
        }
    })
```

#### New Ideas from This Perspective

**24. Thought Checkpointing**

```go
// Checkpoint at semantic boundaries, not just activity boundaries
agent.Path("deep_thinking").
    Think("analyze").
        CheckpointWhen(ThoughtComplete()).  // LLM decides when thought is "done"
    Think("synthesize").
        CheckpointWhen(ConfidenceAbove(0.8))
```

**25. Cognitive Modes**

```go
// Agent switches between cognitive modes (like System 1 vs System 2)
agent := NewCognitiveWorkflow("adaptive_thinker").
    Mode("fast", FastThinkingPath()).     // Quick pattern matching
    Mode("slow", DeepReasoningPath()).    // Careful analysis
    Mode("creative", DivergentPath()).    // Brainstorming

    // Meta-controller decides which mode
    Controller(func(ctx Context, input any) string {
        if input.IsRoutine() { return "fast" }
        if input.IsNovel() { return "slow" }
        if input.NeedsIdeas() { return "creative" }
    })
```

**26. Interruptible Cognition**

```go
// Agent can be interrupted mid-thought
agent.Path("working").
    Interruptible().
    OnInterrupt(func(ctx Context, interrupt Interrupt) {
        if interrupt.Priority > ctx.CurrentTask().Priority {
            ctx.PushState()  // Save current thinking
            ctx.SwitchTo(interrupt.Task)
        }
    })
```

**27. Learned Workflows (Procedural Memory)**

```go
// Agent learns new workflows from experience
agent.Path("learning").
    Activity("attempt", tryNewThing).
    Activity("reflect", func(ctx Context, result Result) {
        if result.Success {
            // Extract the successful pattern as a reusable workflow
            newSkill := ctx.ExtractWorkflow(ctx.RecentTrace())
            ctx.Memory().StoreSkill(newSkill)
        }
    })

// Later, agent can invoke learned skills
agent.Path("skilled").
    Activity("recall", func(ctx Context, task Task) Workflow {
        return ctx.Memory().FindSkill(task)
    }).
    Activity("execute", func(ctx Context, skill Workflow) {
        ctx.ExecuteSubworkflow(skill)
    })
```

#### Sprites Integration (Workflow = Agent)

The Sprite IS the agent's "body":

```go
// The cognitive workflow runs inside a persistent Sprite
sprite := sprites.Create(ctx, SpriteConfig{
    Persistent: true,  // Sprite persists between invocations
    State:      agent.CognitiveState,
})

// Sprite checkpointing = memory consolidation
sprite.OnCheckpoint(func() {
    agent.ConsolidateMemory()
})

// Sprite restore = "waking up"
sprite.OnRestore(func() {
    agent.ReloadContext()
    agent.ResumeThinking()
})
```

**Insight**: Sprites' 300ms checkpoint is like falling asleep and the sub-second restore is like waking up—the agent can "sleep" on hard problems.

---

### Perspective 3: Workflow Called BY Agents (Workflow Below)

**Mental model**: Agents are the top-level intelligence. Workflows are tools they use—like how a human might "start a build and check on it later."

This inverts the typical hierarchy. The agent decides WHEN to invoke workflows, WHICH workflow to use, and HOW to interpret results.

#### Design Implications

**Workflows as Tools**: Workflows appear in the agent's toolkit.

```go
// Define workflow as a tool
dataProcessingTool := WorkflowTool{
    Name:        "process_data_pipeline",
    Description: "Runs a data processing pipeline. Returns job ID for tracking.",
    Workflow:    dataPipelineWorkflow,
    Schema:      dataPipelineSchema,
}

// Agent has workflows in its toolkit
agent := NewAgent(
    tools...,
    dataProcessingTool,       // Workflow as a tool
    orderProcessingTool,      // Another workflow
    reportGenerationTool,     // And another
)
```

**LLM-Friendly Workflow Status**: Status must be readable by models.

```go
// Traditional status (machine-oriented)
type ExecutionRecord struct {
    ID        string
    Status    string  // "running"
    Progress  float64 // 0.45
}

// AI-native status (LLM-readable)
type AgentReadableStatus struct {
    Summary     string   // "Processing customer data: 45% complete. Currently enriching records with Clearbit."
    Issues      []string // ["Rate limited by Clearbit API, will retry in 60s"]
    ETA         string   // "Approximately 10 minutes remaining"
    CanCancel   bool
    CanModify   bool
    Suggestions []string // ["Consider reducing batch size to avoid rate limits"]
}
```

**Agent Monitors Workflow**: Agent can reason about workflow progress.

```go
// Agent's tool for checking workflow status
checkWorkflowTool := Tool{
    Name: "check_workflow_status",
    Execute: func(ctx Context, jobID string) string {
        status := engine.GetAgentReadableStatus(ctx, jobID)
        return status.ToNaturalLanguage()
    },
}

// Agent's tool for modifying running workflow
modifyWorkflowTool := Tool{
    Name: "modify_workflow",
    Execute: func(ctx Context, jobID string, modification string) string {
        // LLM interprets modification request
        action := interpretModification(modification)
        return engine.ApplyModification(ctx, jobID, action)
    },
}
```

#### New Ideas from This Perspective

**28. Workflow Discovery and Selection**

```go
// Agent discovers available workflows dynamically
discoverWorkflowsTool := Tool{
    Name: "discover_workflows",
    Execute: func(ctx Context, need string) []WorkflowSummary {
        // Search workflow registry for matching capabilities
        return registry.Search(need)
    },
}

// Agent reasons about which workflow to use
// "I need to process these customer records. Let me check what workflows are available..."
// "The 'batch_enrichment' workflow seems appropriate. It handles rate limiting automatically."
```

**29. Workflow Composition by Agent**

```go
// Agent can compose workflows on the fly
composeWorkflowTool := Tool{
    Name: "compose_workflow",
    Execute: func(ctx Context, steps []StepDescription) Workflow {
        // Agent describes steps in natural language
        // Engine compiles to executable workflow
        return compiler.FromDescription(steps)
    },
}

// Example agent reasoning:
// "I need to: 1) fetch data from API, 2) transform to CSV, 3) upload to S3"
// "Let me compose a workflow for this..."
```

**30. Workflow Delegation Chains**

```go
// Agent can delegate to another agent, which might invoke workflows
agentA := NewAgent(
    delegateTool(agentB),  // Can ask agent B
    workflowTool(pipeline), // Can invoke workflow directly
)

// Agent A might reason:
// "This task is complex. I'll delegate to Agent B who specializes in data processing."
// Agent B might then invoke the workflow as a tool.
```

**31. Workflow Results as Context**

```go
// Workflow results are formatted for agent consumption
type WorkflowResult struct {
    // Machine-readable
    Output    map[string]any
    Artifacts []Artifact

    // Agent-readable
    Summary       string    // "Successfully processed 10,000 records..."
    KeyInsights   []string  // ["15% of records had missing emails"]
    Anomalies     []string  // ["Unusually high error rate from Clearbit"]
    Recommendations []string // ["Consider validating email format before enrichment"]
}
```

**32. Autonomous Workflow Management**

```go
// Agent manages a portfolio of running workflows
portfolioAgent := NewAgent(
    listRunningWorkflows,
    checkWorkflowHealth,
    cancelWorkflow,
    restartWorkflow,
    adjustWorkflowPriority,
)

// Agent prompt:
// "You are a workflow operations agent. Monitor running workflows,
//  handle failures, and optimize resource usage. Escalate to humans
//  only for business decisions."
```

#### Sprites Integration (Workflow Below)

Workflows run in Sprites; agents decide when to spawn them:

```go
// Agent's tool to spawn a workflow in a Sprite
spawnWorkflowTool := Tool{
    Name: "spawn_workflow",
    Execute: func(ctx Context, workflow string, input any) string {
        // Create isolated Sprite for the workflow
        sprite := sprites.Create(ctx, SpriteConfig{
            Resources: workflow.EstimatedResources(),
            Timeout:   workflow.MaxDuration(),
        })

        // Start workflow in background
        jobID := engine.SubmitInSprite(ctx, workflow, input, sprite)

        return fmt.Sprintf("Started workflow %s (job: %s). Check status with check_workflow_status.", workflow, jobID)
    },
}
```

**Key insight**: The agent doesn't need to know about Sprites—it just knows workflows are reliable, isolated, and long-running.

---

### Synthesis: All Three Perspectives Together

The most powerful design might embrace all three perspectives simultaneously:

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           UNIFIED MODEL                                 │
│                                                                         │
│    ┌──────────────────────────────────────────────────────────────┐     │
│    │                    Top-Level Agent                           │     │
│    │    (Uses workflows as tools - Perspective 3)                 │     │
│    └───────────────────────┬──────────────────────────────────────┘     │
│                            │                                            │
│                            ▼                                            │
│    ┌──────────────────────────────────────────────────────────────┐     │
│    │               Orchestration Workflow                         │     │
│    │    (Coordinates sub-agents - Perspective 1)                  │     │
│    └──────┬────────────────┬────────────────┬─────────────────────┘     │
│           │                │                │                           │
│           ▼                ▼                ▼                           │
│    ┌────────────┐   ┌────────────┐   ┌────────────┐                     │
│    │  Agent A   │   │  Agent B   │   │  Agent C   │                     │
│    │ (Workflow  │   │ (Workflow  │   │ (Workflow  │                     │
│    │  IS brain) │   │  IS brain) │   │  IS brain) │                     │
│    │   - P2     │   │   - P2     │   │   - P2     │                     │
│    └────────────┘   └────────────┘   └────────────┘                     │
│                                                                         │
│   Each layer can use any perspective as appropriate                     │
└─────────────────────────────────────────────────────────────────────────┘
```

**Example: Code Review System**

```go
// Top level: Agent uses workflows as tools (P3)
reviewAgent := NewAgent(
    reviewWorkflowTool,  // Can spawn review workflows
    checkStatusTool,     // Can monitor them
    // ...
)

// Middle level: Workflow orchestrates specialized agents (P1)
reviewWorkflow := NewWorkflow("code_review").
    AgentActivity("security", securityReviewer).
    AgentActivity("performance", perfReviewer).
    AgentActivity("style", styleReviewer).
    JoinActivity("synthesize", synthesizer)

// Bottom level: Each reviewer IS a cognitive workflow (P2)
securityReviewer := NewCognitiveWorkflow("security_reviewer").
    Perceive("scan_code").
    Fork("analyze_vectors", 3).  // Consider multiple attack vectors
    Join("prioritize").
    Act("report")
```

---

### New Ideas from the Synthesis

**33. Recursive Workflow Invocation**

```go
// Workflows can invoke workflows can invoke workflows...
// Each level adds durability and observability

deepWorkflow := NewWorkflow("inception").
    Path("main").
        Activity("analyze", func(ctx Context) {
            // This activity IS an agent (P2)
            // That can invoke workflows (P3)
            // Which orchestrate other agents (P1)
            result := ctx.Agent().InvokeWorkflow(subWorkflow, input)
        })
```

**34. Perspective-Aware Tooling**

```go
// Tools that understand which perspective they're in
type PerspectiveAwareTool struct {
    // Different behavior based on context
    AsOrchestrated func(ctx Context, input any) any  // P1: Being coordinated
    AsCognitive    func(ctx Context, input any) any  // P2: Part of thinking
    AsInvoked      func(ctx Context, input any) any  // P3: Being used as tool
}
```

**35. Cross-Perspective Observability**

```go
// Unified trace across all perspectives
type UnifiedTrace struct {
    // P3: What the top-level agent decided
    AgentDecisions    []Decision

    // P1: How the orchestration proceeded
    WorkflowSteps     []StepTrace

    // P2: What each sub-agent was thinking
    CognitiveTraces   map[string][]ThoughtTrace

    // Connections between layers
    Causality         CausalGraph  // Why did X lead to Y?
}
```

**36. Dynamic Perspective Shifting**

```go
// An agent can promote itself to orchestrator
agent.Path("self_organize").
    Activity("assess", func(ctx Context, task Task) {
        if task.Complexity > threshold {
            // Shift from P2 to P1: become an orchestrator
            subAgents := ctx.SpawnSubAgents(task.Decompose())
            ctx.BecomeOrchestrator(subAgents)
        }
    })

// A workflow can demote itself to a tool
workflow.OnSimpleCase(func(ctx Context) {
    // Shift from P1 to P3: just be a tool
    ctx.SimplifyToTool()
})
```

---

## Updated Open Questions

Adding to the original questions:

7. **Perspective boundaries**: When should an agent use a workflow vs. be a workflow vs. orchestrate with a workflow?

8. **Cross-perspective state**: How does state flow between perspectives? Can an orchestrating workflow see inside a cognitive workflow?

9. **Failure propagation**: If a cognitive workflow (P2) fails, how does that propagate up through the orchestrator (P1) to the invoking agent (P3)?

10. **Resource allocation**: How do we allocate Sprites across perspectives? One per agent? One per workflow? Shared?

11. **Observability unification**: Can we have a single trace format that makes sense across all three perspectives?

---

## Revised Top Ideas to Develop

Based on the three perspectives analysis:

### 1. Multi-Perspective Agent Primitive

Build a primitive that can operate in any of the three modes:

```go
type FlexibleAgent struct {
    // Can be orchestrated (P1)
    AsActivity() Activity

    // Can be cognitive architecture (P2)
    AsWorkflow() Workflow

    // Can be tool-user (P3)
    AsToolUser() Agent
}
```

### 2. Unified State Model

Design state that works across perspectives:

```go
type UnifiedState struct {
    // P1: Structured data passed between activities
    ActivityState map[string]any

    // P2: Conversation and working memory
    CognitiveState CognitiveState

    // P3: Tool results and pending jobs
    ToolState ToolState

    // Cross-cutting: always available
    SharedContext SharedContext
}
```

### 3. Sprites as Universal Substrate

Sprites provide isolation regardless of perspective:

```go
// Every agent, at every level, runs in a Sprite
// Perspective determines how the Sprite is used

type SpriteUsageMode int

const (
    ModeSandbox     SpriteUsageMode = iota  // P1: Isolated activity execution
    ModePersistent                           // P2: Long-lived cognitive context
    ModeEphemeral                            // P3: Fire-and-forget tool execution
)
```

---

## Part 3: Business Process Guarantees and Governance

AI-powered workflows in enterprise contexts need more than just reliability—they need **auditability**, **repeatability**, **explainability**, and **controlled evolution**. This section explores what it means to run AI decision-making in contexts where decisions have consequences and must be defensible.

### The Enterprise AI Challenge

Traditional business processes have:
- Clear inputs and outputs with validation
- Audit trails showing what happened
- Policies that can be reviewed and updated
- Compliance with regulations
- Predictable, repeatable outcomes

AI agents introduce:
- Probabilistic reasoning
- Emergent decision-making
- "Black box" problem—why did it decide that?
- Non-deterministic outputs
- Rapidly evolving capabilities

**The challenge**: How do we get the flexibility of AI while maintaining the governance of traditional processes?

---

### Dimension 1: Decision Provenance

Every AI decision needs a complete lineage—not just WHAT was decided, but WHY, WITH WHAT INFORMATION, and UNDER WHAT POLICIES.

#### Decision Record Structure

```go
type DecisionRecord struct {
    // Identity
    ID              string
    ExecutionID     string
    Timestamp       time.Time

    // The decision itself
    DecisionType    string              // "approve_refund", "route_ticket", "flag_fraud"
    Outcome         any                 // The actual decision
    Confidence      float64             // Model's confidence
    Alternatives    []Alternative       // What else was considered

    // Context at decision time
    InputSnapshot   map[string]any      // Exact inputs seen
    ContextSnapshot ContextSnapshot     // What was in context window
    ToolResults     []ToolResultRecord  // Tool calls that informed this

    // The decision-maker
    ModelID         string              // "claude-3-sonnet-20240307"
    ModelVersion    string
    PromptVersion   string              // "refund-policy-v2.3.1"
    PromptHash      string              // SHA of exact prompt used
    Temperature     float64

    // Governance
    PolicyVersion   string              // Business rules version
    ApprovalChain   []Approval          // Human approvals if any
    Constraints     []Constraint        // What constraints were active

    // Reasoning trace
    ReasoningSteps  []ReasoningStep     // Chain of thought
    KeyFactors      []Factor            // What mattered most
    Assumptions     []string            // Explicit assumptions made
}

type ReasoningStep struct {
    Type        string  // "observation", "inference", "conclusion"
    Content     string  // The actual reasoning
    Evidence    []string // What supported this step
    Confidence  float64
}

type Factor struct {
    Name        string
    Value       any
    Weight      float64  // How much did this influence the decision?
    Direction   string   // "supporting", "opposing", "neutral"
}
```

#### Queryable Decision History

```go
// Query past decisions
decisions, err := engine.QueryDecisions(ctx, DecisionQuery{
    Type:       "approve_refund",
    TimeRange:  Last30Days(),
    Outcome:    "approved",
    Filters: map[string]any{
        "amount_gt": 1000,
        "confidence_lt": 0.8,
    },
})

// Find similar decisions
similar := engine.FindSimilarDecisions(ctx, currentDecision, SimilarityOptions{
    ByInput:     true,
    ByOutcome:   true,
    ByReasoning: true,
    Limit:       10,
})

// Audit trail for compliance
trail := engine.GetAuditTrail(ctx, executionID, AuditOptions{
    IncludeReasoning:   true,
    IncludePolicyRefs:  true,
    Format:             AuditFormatCompliance,  // Legal/compliance friendly
})
```

---

### Dimension 2: Contracts and Guarantees

AI workflows need enforceable contracts—not just type signatures, but semantic guarantees.

#### Input/Output Contracts

```go
type ProcessContract struct {
    Name        string
    Version     string

    // Structural contracts (traditional)
    InputSchema     *schema.Schema
    OutputSchema    *schema.Schema

    // Semantic contracts (AI-native)
    InputConstraints  []SemanticConstraint
    OutputConstraints []SemanticConstraint

    // Quality guarantees
    SLA             SLAContract

    // Decision boundaries
    DecisionPolicy  DecisionPolicy
}

type SemanticConstraint struct {
    Name        string
    Description string  // Human-readable
    Validator   string  // LLM prompt or code reference
    Severity    string  // "error", "warning", "info"
}

// Example: Refund approval process
refundContract := ProcessContract{
    Name:    "refund_approval",
    Version: "2.3.0",

    InputSchema: schema.Object(
        schema.Field("order_id", schema.String()),
        schema.Field("reason", schema.String()),
        schema.Field("amount", schema.Number()),
    ),

    InputConstraints: []SemanticConstraint{
        {
            Name:        "reason_clarity",
            Description: "Refund reason must be clear and specific",
            Validator:   "prompts/validators/reason_clarity.txt",
            Severity:    "warning",
        },
    },

    OutputConstraints: []SemanticConstraint{
        {
            Name:        "decision_justified",
            Description: "Decision must reference specific policy clauses",
            Validator:   "prompts/validators/policy_reference.txt",
            Severity:    "error",
        },
        {
            Name:        "no_pii_leak",
            Description: "Response must not contain customer PII",
            Validator:   "code:validators.NoPIIInOutput",
            Severity:    "error",
        },
    },

    DecisionPolicy: DecisionPolicy{
        RequireHumanApproval: When("amount > 500 OR confidence < 0.7"),
        MaxAutoApproval:      500.00,
        EscalationPath:       []string{"team_lead", "manager", "director"},
    },
}
```

#### Runtime Contract Enforcement

```go
func (e *Engine) ExecuteWithContract(ctx context.Context, contract ProcessContract, input any) (*Result, error) {
    // 1. Validate input against schema
    if err := contract.InputSchema.Validate(input); err != nil {
        return nil, ContractViolation("input_schema", err)
    }

    // 2. Validate semantic input constraints
    for _, constraint := range contract.InputConstraints {
        if err := e.validateSemantic(ctx, constraint, input); err != nil {
            if constraint.Severity == "error" {
                return nil, ContractViolation("input_semantic", err)
            }
            e.logger.Warn("semantic constraint warning", "constraint", constraint.Name, "error", err)
        }
    }

    // 3. Execute the workflow
    result, decision := e.executeWorkflow(ctx, input)

    // 4. Validate output against schema
    if err := contract.OutputSchema.Validate(result.Output); err != nil {
        return nil, ContractViolation("output_schema", err)
    }

    // 5. Validate semantic output constraints
    for _, constraint := range contract.OutputConstraints {
        if err := e.validateSemantic(ctx, constraint, result); err != nil {
            if constraint.Severity == "error" {
                // Don't return bad output - fail loudly
                return nil, ContractViolation("output_semantic", err)
            }
        }
    }

    // 6. Check decision policy
    if contract.DecisionPolicy.RequiresApproval(decision) {
        result.Status = StatusPendingApproval
        result.PendingApproval = e.requestApproval(ctx, decision, contract.DecisionPolicy)
    }

    return result, nil
}
```

---

### Dimension 3: Explainability for Humans

Decisions need to be explainable to different audiences: end users, business analysts, auditors, regulators.

#### Multi-Level Explanations

```go
type Explanation struct {
    // For end users: simple, actionable
    UserSummary     string  // "Your refund was approved because the item arrived damaged."

    // For business analysts: process-oriented
    BusinessSummary string  // "Refund approved under policy RP-2.3: Damaged goods within 30-day window."

    // For auditors: complete reasoning
    AuditSummary    string  // Full decision record with policy references

    // For technical review: model details
    TechnicalDetail TechnicalExplanation
}

type TechnicalExplanation struct {
    ModelUsed       string
    PromptVersion   string
    TokensUsed      int
    ReasoningTrace  []ReasoningStep
    FactorWeights   map[string]float64
    Counterfactuals []Counterfactual  // "If X were different, outcome would be Y"
}

type Counterfactual struct {
    Change          string   // "If the order was placed 45 days ago instead of 25"
    AlternateOutcome string  // "The refund would have been denied"
    Confidence      float64
}
```

#### Explanation Generation

```go
// Generate explanation at query time, not just decision time
func (e *Engine) ExplainDecision(ctx context.Context, decisionID string, audience AudienceType) (*Explanation, error) {
    record := e.getDecisionRecord(ctx, decisionID)

    switch audience {
    case AudienceUser:
        return e.generateUserExplanation(ctx, record)
    case AudienceBusiness:
        return e.generateBusinessExplanation(ctx, record)
    case AudienceAudit:
        return e.generateAuditExplanation(ctx, record)
    case AudienceTechnical:
        return e.generateTechnicalExplanation(ctx, record)
    }
}

// Counterfactual analysis: "What would have happened if...?"
func (e *Engine) WhatIf(ctx context.Context, decisionID string, changes map[string]any) (*Counterfactual, error) {
    record := e.getDecisionRecord(ctx, decisionID)

    // Modify the input
    modifiedInput := applyChanges(record.InputSnapshot, changes)

    // Re-run with same model/prompt versions (deterministic replay)
    alternateResult := e.replayDecision(ctx, record, modifiedInput)

    return &Counterfactual{
        Change:          describeChanges(changes),
        AlternateOutcome: alternateResult.Outcome,
        Confidence:      alternateResult.Confidence,
    }, nil
}
```

---

### Dimension 4: Repeatability and Determinism

For certain decisions, you need the ability to get the same answer given the same inputs.

#### Deterministic Mode

```go
type DeterminismLevel int

const (
    // Best effort: same model, same prompt, temperature=0, but no guarantee
    DeterminismBestEffort DeterminismLevel = iota

    // Cached: if we've seen this exact input before, return cached decision
    DeterminismCached

    // Replay: re-execute with exact same context, verify same outcome
    DeterminismReplay

    // Rule-based: no LLM, pure deterministic logic
    DeterminismStrict
)

type ProcessConfig struct {
    Contract        ProcessContract
    Determinism     DeterminismLevel

    // For cached mode
    CacheKey        func(input any) string
    CacheTTL        time.Duration

    // For replay mode
    ReplayTolerance float64  // How much variance is acceptable?
}
```

#### Decision Caching

```go
// Cache decisions for repeatability
type DecisionCache struct {
    store ExecutionStore
}

func (c *DecisionCache) GetOrDecide(ctx context.Context, key string, decider func() (*Decision, error)) (*Decision, error) {
    // Check cache first
    if cached := c.store.GetCachedDecision(ctx, key); cached != nil {
        // Verify it's still valid (policy hasn't changed, etc.)
        if c.isStillValid(ctx, cached) {
            cached.Source = "cache"
            return cached, nil
        }
    }

    // Make new decision
    decision, err := decider()
    if err != nil {
        return nil, err
    }

    // Cache for future
    c.store.CacheDecision(ctx, key, decision)
    decision.Source = "computed"
    return decision, nil
}
```

#### Replay Testing

```go
// Test that a process produces consistent results
func TestProcessDeterminism(t *testing.T) {
    process := LoadProcess("refund_approval")

    // Load historical decisions
    historicalDecisions := loadTestCases("refund_decisions_2024.json")

    for _, historical := range historicalDecisions {
        // Replay with same inputs
        replayed := process.Replay(ctx, ReplayConfig{
            Input:         historical.Input,
            ModelVersion:  historical.ModelVersion,
            PromptVersion: historical.PromptVersion,
            Temperature:   0,
        })

        // Verify outcome matches (within tolerance)
        assert.Equal(t, historical.Outcome, replayed.Outcome,
            "Decision %s should be reproducible", historical.ID)

        // Verify reasoning is similar
        similarity := compareReasoning(historical.ReasoningSteps, replayed.ReasoningSteps)
        assert.Greater(t, similarity, 0.9,
            "Reasoning should be consistent")
    }
}
```

---

### Dimension 5: Process Evolution

Business processes evolve. The AI decision-making must evolve with them while maintaining auditability and avoiding regressions.

#### Versioned Process Definitions

```go
type ProcessDefinition struct {
    ID          string
    Version     semver.Version

    // The actual process
    Workflow    *Workflow
    Contract    ProcessContract

    // Decision components (all versioned)
    Prompts     map[string]PromptVersion
    Policies    map[string]PolicyVersion
    Models      map[string]ModelConfig

    // Evolution metadata
    ChangeLog   []ChangeLogEntry
    Migration   *MigrationSpec  // How to migrate from previous version
    Rollback    *RollbackSpec   // How to rollback if needed
}

type ChangeLogEntry struct {
    Version     string
    Date        time.Time
    Author      string
    Description string
    Breaking    bool
    Changes     []Change
}

type Change struct {
    Type        string  // "prompt_update", "policy_change", "model_upgrade"
    Component   string  // "main_prompt", "approval_policy"
    Before      string  // Hash or version
    After       string
    Reason      string
}
```

#### Safe Process Updates

```go
type ProcessUpdateStrategy int

const (
    // All new executions use new version immediately
    UpdateImmediate ProcessUpdateStrategy = iota

    // Canary: small percentage uses new version
    UpdateCanary

    // Shadow: run both, compare results, don't use new results yet
    UpdateShadow

    // Staged: roll out to specific segments first
    UpdateStaged
)

type ProcessUpdate struct {
    FromVersion     string
    ToVersion       string
    Strategy        ProcessUpdateStrategy

    // For canary
    CanaryPercent   int
    CanaryDuration  time.Duration

    // For shadow
    ShadowCompare   func(old, new *Decision) CompareResult

    // For staged
    Stages          []UpdateStage

    // Rollback triggers
    RollbackIf      RollbackCondition
}

type RollbackCondition struct {
    ErrorRateAbove      float64  // Rollback if error rate exceeds this
    ConfidenceBelow     float64  // Rollback if avg confidence drops
    OutcomeShift        float64  // Rollback if outcomes shift too much
    HumanOverrideRate   float64  // Rollback if humans override too often
}
```

#### Shadow Mode for Safe Testing

```go
// Run new process version in shadow mode
func (e *Engine) ExecuteShadow(ctx context.Context, input any) (*ShadowResult, error) {
    // Run production version
    prodResult, _ := e.Execute(ctx, e.productionProcess, input)

    // Run candidate version
    candidateResult, _ := e.Execute(ctx, e.candidateProcess, input)

    // Compare results (don't affect production)
    comparison := e.compare(prodResult, candidateResult)

    // Log for analysis
    e.logShadowResult(ctx, ShadowResult{
        ProductionResult:  prodResult,
        CandidateResult:   candidateResult,
        Comparison:        comparison,
    })

    // Return production result (candidate is just for observation)
    return prodResult, nil
}

// Analyze shadow results before promoting
func (e *Engine) AnalyzeShadowRun(ctx context.Context, shadowRunID string) *ShadowAnalysis {
    results := e.getShadowResults(ctx, shadowRunID)

    return &ShadowAnalysis{
        TotalDecisions:      len(results),
        OutcomeAgreement:    calculateAgreement(results),
        ConfidenceComparison: compareConfidences(results),
        ReasoningDivergence: analyzeReasoningDiffs(results),
        RiskAssessment:      assessPromotionRisk(results),
        Recommendation:      makeRecommendation(results),
    }
}
```

---

### Dimension 6: Compliance and Governance Integration

AI decisions in regulated industries need to integrate with existing compliance frameworks.

#### Compliance Hooks

```go
type ComplianceIntegration struct {
    // Before decision
    PreDecisionChecks   []ComplianceCheck

    // After decision
    PostDecisionChecks  []ComplianceCheck

    // Reporting
    ReportingHooks      []ReportingHook

    // Retention
    RetentionPolicy     RetentionPolicy
}

type ComplianceCheck struct {
    Name        string
    Regulation  string  // "GDPR", "SOX", "HIPAA", etc.
    Check       func(ctx context.Context, decision *Decision) error
    OnFailure   ComplianceFailureAction
}

type ReportingHook struct {
    Name        string
    Trigger     ReportTrigger  // "all", "high_value", "denied", etc.
    Destination string         // "compliance_system", "audit_log", etc.
    Format      ReportFormat
}

// Example: Financial services compliance
financialCompliance := ComplianceIntegration{
    PreDecisionChecks: []ComplianceCheck{
        {
            Name:       "sanctions_screening",
            Regulation: "OFAC",
            Check:      checkSanctionsList,
            OnFailure:  BlockAndEscalate,
        },
    },

    PostDecisionChecks: []ComplianceCheck{
        {
            Name:       "fair_lending",
            Regulation: "ECOA",
            Check:      checkForDiscrimination,
            OnFailure:  FlagForReview,
        },
        {
            Name:       "explainability",
            Regulation: "FCRA",
            Check:      ensureAdverseActionExplanation,
            OnFailure:  RequireHumanExplanation,
        },
    },

    RetentionPolicy: RetentionPolicy{
        DecisionRecords:    7 * 365 * 24 * time.Hour,  // 7 years
        ReasoningTraces:    7 * 365 * 24 * time.Hour,
        InputData:          RetainHashOnly,            // Don't retain PII
    },
}
```

#### Bias and Fairness Monitoring

```go
type FairnessMonitor struct {
    ProtectedAttributes []string  // "age", "gender", "race", etc.
    Metrics            []FairnessMetric
    AlertThresholds    map[string]float64
}

type FairnessMetric struct {
    Name        string
    Calculate   func(decisions []Decision) float64
    Threshold   float64
}

// Continuous monitoring
func (m *FairnessMonitor) Monitor(ctx context.Context, decisions <-chan Decision) {
    window := NewSlidingWindow(24 * time.Hour)

    for decision := range decisions {
        window.Add(decision)

        for _, metric := range m.Metrics {
            value := metric.Calculate(window.Decisions())

            if value > m.AlertThresholds[metric.Name] {
                m.Alert(ctx, FairnessAlert{
                    Metric:    metric.Name,
                    Value:     value,
                    Threshold: metric.Threshold,
                    Window:    window.Decisions(),
                })
            }
        }
    }
}
```

---

### New Ideas from Governance Perspective

**37. Decision Fingerprinting**

```go
// Create a fingerprint that uniquely identifies how a decision was made
type DecisionFingerprint struct {
    InputHash       string  // Hash of inputs
    ModelHash       string  // Hash of model version
    PromptHash      string  // Hash of prompts used
    PolicyHash      string  // Hash of active policies
    ToolsHash       string  // Hash of tool versions
    Timestamp       time.Time
}

// Two decisions with same fingerprint should produce same outcome
func (d *DecisionFingerprint) Equals(other *DecisionFingerprint) bool
```

**38. Decision Lineage Graphs**

```go
// Track how decisions relate to and influence each other
type DecisionLineage struct {
    Decision    *DecisionRecord
    DependsOn   []*DecisionLineage  // Decisions this one used as input
    Influences  []*DecisionLineage  // Decisions that used this as input
    SharedContext []string          // Context shared between related decisions
}

// "This loan denial was influenced by a fraud flag from 3 days ago"
lineage := engine.GetDecisionLineage(ctx, decisionID, LineageOptions{
    Depth:     5,
    Direction: Both,
})
```

**39. Policy as Code with AI Interpretation**

```go
// Business policies in structured format, interpreted by AI
type BusinessPolicy struct {
    ID          string
    Name        string
    Version     string

    // Human-readable policy
    Description string

    // Structured rules (deterministic)
    Rules       []PolicyRule

    // Guidance for edge cases (AI interprets)
    Guidance    string  // "When in doubt, err on the side of customer satisfaction..."

    // Examples
    Examples    []PolicyExample
}

type PolicyRule struct {
    Condition   string  // "order.age_days > 30"
    Action      string  // "deny"
    Exception   string  // "unless customer.tier == 'gold'"
}

// AI applies policy with structured rules + guidance for edge cases
func (e *Engine) ApplyPolicy(ctx context.Context, policy BusinessPolicy, input any) (*PolicyApplication, error) {
    // First, try deterministic rules
    for _, rule := range policy.Rules {
        if matches, action := rule.Evaluate(input); matches {
            return &PolicyApplication{
                Outcome:   action,
                Rule:      rule,
                Source:    "deterministic",
            }, nil
        }
    }

    // Edge case: use AI with guidance
    return e.aiInterpretPolicy(ctx, policy, input)
}
```

**40. Retrospective Analysis and Learning**

```go
// Analyze past decisions to improve future ones
type RetrospectiveAnalysis struct {
    Period          time.Duration
    Decisions       int

    // Outcome analysis
    OutcomeDistribution map[string]int
    HumanOverrideRate   float64
    OverrideReasons     map[string]int

    // Quality metrics
    AverageConfidence   float64
    ConfidenceTrend     Trend

    // Discovered patterns
    CommonMistakes      []Pattern
    SuccessPatterns     []Pattern

    // Recommendations
    PromptImprovements  []Suggestion
    PolicyGaps          []Gap
    TrainingNeeds       []TrainingNeed
}

// Regular retrospective
func (e *Engine) RunRetrospective(ctx context.Context, processID string, period time.Duration) *RetrospectiveAnalysis {
    decisions := e.getDecisions(ctx, processID, period)
    overrides := e.getHumanOverrides(ctx, processID, period)

    analysis := &RetrospectiveAnalysis{
        Period:    period,
        Decisions: len(decisions),
    }

    // Analyze patterns in overrides
    for _, override := range overrides {
        pattern := e.analyzeOverride(ctx, override)
        if pattern.IsSystematic {
            analysis.CommonMistakes = append(analysis.CommonMistakes, pattern)
            analysis.PromptImprovements = append(analysis.PromptImprovements,
                pattern.SuggestedFix)
        }
    }

    return analysis
}
```

**41. Decision Simulation Environment**

```go
// Sandbox for testing process changes before production
type SimulationEnvironment struct {
    Process         ProcessDefinition
    HistoricalData  []HistoricalCase
    SyntheticData   []SyntheticCase
}

func (s *SimulationEnvironment) Simulate(ctx context.Context, changes ProcessChanges) *SimulationResult {
    results := make([]SimulatedDecision, 0)

    for _, testCase := range append(s.HistoricalData, s.SyntheticData...) {
        // Run with current process
        current := s.Process.Execute(ctx, testCase.Input)

        // Run with changed process
        changed := s.Process.WithChanges(changes).Execute(ctx, testCase.Input)

        results = append(results, SimulatedDecision{
            Input:           testCase.Input,
            CurrentOutcome:  current,
            ChangedOutcome:  changed,
            ExpectedOutcome: testCase.ExpectedOutcome,  // If known
        })
    }

    return &SimulationResult{
        TotalCases:      len(results),
        OutcomeChanges:  countChanges(results),
        Improvements:    countImprovements(results),
        Regressions:     countRegressions(results),
        RiskAssessment:  assessRisk(results),
    }
}
```

**42. Human Override Learning Loop**

```go
// Learn from human overrides to improve AI decisions
type OverrideLearningLoop struct {
    MinOverrides    int           // Minimum overrides before learning
    LearningWindow  time.Duration
    AutoUpdate      bool          // Automatically update prompts?
}

func (l *OverrideLearningLoop) Process(ctx context.Context, override HumanOverride) {
    // Record the override
    l.store.RecordOverride(ctx, override)

    // Check if we have enough data to learn
    recentOverrides := l.store.GetOverrides(ctx, l.LearningWindow)
    if len(recentOverrides) < l.MinOverrides {
        return
    }

    // Analyze patterns
    patterns := l.analyzeOverridePatterns(ctx, recentOverrides)

    for _, pattern := range patterns {
        if pattern.Confidence > 0.8 {
            suggestion := l.generatePromptImprovement(ctx, pattern)

            if l.AutoUpdate {
                l.applyWithShadow(ctx, suggestion)
            } else {
                l.suggestToHumans(ctx, suggestion)
            }
        }
    }
}
```

---

### Integration: Governance-Aware Engine

Putting it all together:

```go
type GovernanceAwareEngine struct {
    *Engine

    // Contracts
    contracts       map[string]ProcessContract

    // Decision management
    decisionStore   DecisionStore
    decisionCache   *DecisionCache

    // Compliance
    compliance      ComplianceIntegration
    fairnessMonitor *FairnessMonitor

    // Evolution
    versionManager  *VersionManager
    shadowRunner    *ShadowRunner

    // Learning
    retrospective   *RetrospectiveAnalyzer
    overrideLoop    *OverrideLearningLoop
}

// Full lifecycle execution with governance
func (e *GovernanceAwareEngine) ExecuteGoverned(ctx context.Context, processID string, input any) (*GovernedResult, error) {
    process := e.getProcess(processID)
    contract := e.contracts[processID]

    // 1. Validate input contract
    if err := contract.ValidateInput(input); err != nil {
        return nil, err
    }

    // 2. Pre-decision compliance checks
    for _, check := range e.compliance.PreDecisionChecks {
        if err := check.Check(ctx, input); err != nil {
            return nil, ComplianceError{Check: check, Err: err}
        }
    }

    // 3. Check decision cache (for repeatability)
    cacheKey := contract.CacheKey(input)
    if cached := e.decisionCache.Get(ctx, cacheKey); cached != nil {
        return e.wrapCachedResult(cached), nil
    }

    // 4. Execute workflow
    result, decision := e.executeWithTracing(ctx, process, input)

    // 5. Validate output contract
    if err := contract.ValidateOutput(result); err != nil {
        return nil, err
    }

    // 6. Post-decision compliance checks
    for _, check := range e.compliance.PostDecisionChecks {
        if err := check.Check(ctx, decision); err != nil {
            return nil, ComplianceError{Check: check, Err: err}
        }
    }

    // 7. Store decision record
    e.decisionStore.Store(ctx, decision)

    // 8. Cache for repeatability
    e.decisionCache.Set(ctx, cacheKey, decision)

    // 9. Feed to fairness monitor
    e.fairnessMonitor.Observe(decision)

    // 10. Generate explanations
    explanation := e.generateExplanation(ctx, decision)

    return &GovernedResult{
        Result:      result,
        Decision:    decision,
        Explanation: explanation,
        Lineage:     e.getLineage(ctx, decision),
    }, nil
}
```

---

### Updated Synthesis

The governance perspective adds a critical layer to all three architectural perspectives:

| Perspective | Governance Addition |
|-------------|---------------------|
| P1: Workflow Above | Orchestration includes compliance gates, approval workflows |
| P2: Workflow = Agent | Cognitive architecture includes "compliance conscience" |
| P3: Workflow Below | Workflows are governed tools with contracts and audit trails |

**Key insight**: Governance isn't a separate concern—it's woven through every layer. The engine doesn't just execute workflows; it maintains a complete, queryable, explainable record of every decision made.

---

## Revised Top Ideas (Final)

Based on all three sections:

### 1. Decision Record as First-Class Primitive

Every AI decision is captured with full provenance, enabling audit, replay, and learning.

### 2. Multi-Perspective Agent with Contracts

Agents can operate in any mode (P1/P2/P3) while adhering to typed + semantic contracts.

### 3. Shadow Mode for Safe Evolution

Run new process versions alongside production, compare results, promote with confidence.

### 4. Sprites as Governed Execution Environment

Each Sprite runs with explicit capability constraints, and its checkpoint includes the decision record.

### 5. Human Override Learning Loop

Systematically learn from human corrections to improve AI decision-making over time.

---

*Updated during third brainstorming pass (governance focus), January 2026*
