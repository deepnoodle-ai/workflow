# Tools

Tools are the actions an AI agent can take during its execution. The `ai` package provides a `Tool` interface and several built-in tools for common operations.

## Tool Interface

```go
type Tool interface {
    // Name returns the unique name of the tool.
    Name() string

    // Description returns a human-readable description for the LLM.
    Description() string

    // Schema returns the JSON schema for the tool's input parameters.
    Schema() *ToolSchema

    // Execute runs the tool with the given arguments.
    Execute(ctx context.Context, args map[string]any) (*ToolResult, error)
}
```

## ToolResult

```go
type ToolResult struct {
    CallID  string `json:"call_id"`
    Output  string `json:"output,omitempty"`
    Error   string `json:"error,omitempty"`
    Success bool   `json:"success"`
}
```

## Creating Custom Tools

### Using ToolFunc

The simplest way to create a tool is with `ToolFunc`:

```go
searchTool := ai.NewToolFunc(
    "search",
    "Search a knowledge base for relevant information",
    ai.NewObjectSchema().
        AddProperty("query", ai.StringProperty("The search query")).
        AddRequired("query"),
    func(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
        query := args["query"].(string)
        results := doSearch(query)
        return &ai.ToolResult{
            Output:  results,
            Success: true,
        }, nil
    },
)
```

### Implementing the Interface

For more complex tools, implement the `Tool` interface directly:

```go
type DatabaseTool struct {
    db *sql.DB
}

func (t *DatabaseTool) Name() string {
    return "query_database"
}

func (t *DatabaseTool) Description() string {
    return "Execute a read-only SQL query"
}

func (t *DatabaseTool) Schema() *ai.ToolSchema {
    return ai.NewObjectSchema().
        AddProperty("query", ai.StringProperty("SQL SELECT query")).
        AddRequired("query")
}

func (t *DatabaseTool) Execute(ctx context.Context, args map[string]any) (*ai.ToolResult, error) {
    query := args["query"].(string)
    // Execute query...
    return &ai.ToolResult{Output: result, Success: true}, nil
}
```

## Schema Helpers

The package provides helpers for building JSON schemas:

```go
// Basic types
ai.StringProperty("description")
ai.IntegerProperty("description")
ai.NumberProperty("description")
ai.BooleanProperty("description")

// Arrays
ai.ArrayProperty("description", ai.StringProperty("item description"))

// Enums
ai.EnumProperty("description", "option1", "option2", "option3")

// Objects
ai.NewObjectSchema().
    AddProperty("name", ai.StringProperty("User's name")).
    AddProperty("age", ai.IntegerProperty("User's age")).
    AddRequired("name")
```

## DurableTool

`DurableTool` wraps any tool with idempotency support for workflow recovery:

```go
// Wrap a tool for durability
durableTool := ai.NewDurableTool(myTool)

// Execute with a deterministic call ID
result, err := durableTool.Execute(ctx, callID, args)

// On recovery, same callID returns cached result
```

### How It Works

1. Each tool call is assigned a deterministic ID via `ctx.DeterministicID()`
2. Results are cached by call ID
3. On workflow recovery, the same call ID produces the same cached result
4. This prevents duplicate side effects (e.g., sending emails twice)

### Cache Management

```go
// Export cache for checkpointing
cache := durableTool.ExportCache()

// Restore cache from checkpoint
durableTool.RestoreCache(savedCache)

// Clear cache
durableTool.ClearCache()
```

## Built-in Tools

The `ai/tools` package provides ready-to-use tools.

### File Tools

#### FileReadTool

```go
readTool := tools.NewFileReadTool(tools.FileReadToolOptions{
    AllowedPaths: []string{"/data", "/config"}, // Restrict access
})
```

Parameters:
- `path` (string, required): File path to read

#### FileWriteTool

```go
writeTool := tools.NewFileWriteTool(tools.FileWriteToolOptions{
    AllowedPaths: []string{"/output"},
})
```

Parameters:
- `path` (string, required): File path to write
- `content` (string, required): Content to write

#### FileListTool

```go
listTool := tools.NewFileListTool(tools.FileListToolOptions{
    AllowedPaths: []string{"/data"},
})
```

Parameters:
- `path` (string, required): Directory to list
- `recursive` (boolean): Whether to list recursively

### HTTP Tool

```go
httpTool := tools.NewHTTPTool(tools.HTTPToolOptions{
    Timeout:      30 * time.Second,
    AllowedHosts: []string{"api.example.com"}, // Restrict hosts
})
```

Parameters:
- `url` (string, required): URL to request
- `method` (string): HTTP method (GET, POST, PUT, DELETE, PATCH)
- `headers` (object): HTTP headers
- `body` (string): Request body
- `json_body` (object): JSON body (auto-sets Content-Type)

### Script Tools

#### ShellTool

```go
shellTool := tools.NewShellTool(tools.ShellToolOptions{
    Shell:           "bash",
    Timeout:         60 * time.Second,
    AllowedCommands: []string{"ls", "cat", "grep"}, // Restrict commands
    WorkingDir:      "/workspace",
})
```

Parameters:
- `command` (string, required): Shell command to execute
- `working_dir` (string): Working directory

#### PythonTool

```go
pythonTool := tools.NewPythonTool(tools.PythonToolOptions{
    PythonPath: "python3",
    Timeout:    60 * time.Second,
    WorkingDir: "/workspace",
})
```

Parameters:
- `code` (string, required): Python code to execute

## Security Considerations

1. **Restrict paths** - Use `AllowedPaths` for file tools to prevent unauthorized access.

2. **Restrict hosts** - Use `AllowedHosts` for HTTP tools to prevent SSRF attacks.

3. **Restrict commands** - Use `AllowedCommands` for shell tools to limit execution.

4. **Set timeouts** - Always configure appropriate timeouts to prevent hanging.

5. **Validate inputs** - Check tool arguments before processing.

6. **Run in isolation** - For untrusted agents, use Sprites for VM-level isolation.

## Registering Tools with Agents

```go
agent := ai.NewAgentActivity("assistant", llm, ai.AgentActivityOptions{
    SystemPrompt: "You are a helpful assistant.",
    Tools: map[string]ai.Tool{
        "read_file":    readTool,
        "write_file":   writeTool,
        "http_request": httpTool,
        "run_command":  shellTool,
    },
})
```

## Tool Call Lifecycle

1. **LLM requests tool call** - Returns tool name and arguments
2. **OnToolCall hook** - Optional pre-execution hook
3. **Tool executes** - `Execute()` is called
4. **OnToolResult hook** - Optional post-execution hook
5. **Result added to conversation** - For next LLM turn
6. **Checkpoint** - Conversation state is checkpointed
