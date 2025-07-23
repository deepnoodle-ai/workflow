# Error Handling

The workflow library provides comprehensive error handling with retry
mechanisms, catch handlers, and structured error classification.

## Error Types

Error types are primarily **matching patterns** used in `error_equals` fields of
retry and catch configurations, not rigid error categories.

### Built-in Error Types

| Error Type | Value | Matching Behavior | Retry Eligible |
|-----------|-------|-------------------|----------------|
| `ErrorTypeAll` | "all" | Matches any error except fatal errors | Yes |
| `ErrorTypeActivityFailed` | "activity_failed" | Matches any error except timeouts and fatal errors | Yes |
| `ErrorTypeTimeout` | "timeout" | Matches timeout/cancellation errors exactly | Yes |
| `ErrorTypeFatal` | "fatal_error" | Matches fatal errors exactly | No |

### Custom Error Types

Activities can return custom error types that can be matched exactly by name:

```go
return nil, workflow.NewWorkflowError("permission-denied", "Access forbidden")
```

### Error Classification & Matching

Regular Go errors are automatically classified for matching:

- Context timeouts/cancellation → `ErrorTypeTimeout` ("timeout")
- All other errors → `ErrorTypeActivityFailed` ("activity_failed")

**Matching Logic:**

- `"all"` - Matches any error except fatal errors (wildcard)
- `"activity_failed"` - Matches any error except timeouts and fatal errors  
- `"timeout"` - Only matches timeout/cancellation errors
- `"permission-denied"` - Only matches errors with exactly this custom type
- `"fatal_error"` - Only matches fatal errors

## Retry Configuration

Steps can define multiple retry configurations using error type matching
patterns. The first error determines which configuration is used for the entire
retry sequence.

```yaml
retry:
  - error_equals: ["timeout"] # Match timeout errors only
    max_retries: 3
    base_delay: "2s"
    backoff_rate: 2.0
    max_delay: "10s"
    jitter_strategy: "FULL"
  - error_equals: ["activity_failed"] # Match non-timeout activity errors
    max_retries: 2
```

### Key Retry Behavior

- `error_equals` uses the matching patterns described above
- First error determines retry policy for entire sequence
- Subsequent errors use same configuration regardless of type
- Empty `error_equals` defaults to match `"all"`
- Exponential backoff with optional jitter and max delay

## Catch Handlers

Catch handlers provide fallback execution when retries are exhausted, using the
same error matching patterns:

```yaml
catch:
  - error_equals: ["permission-denied"]  # Match custom error type exactly
    next: "handle-auth-error"
    store: "error_info"
  - error_equals: ["all"]              # Match any non-fatal error (fallback)
    next: "general-error-handler"  
    store: "error_info"
```

### Catch Behavior

- `error_equals` uses the same matching patterns as retry configurations
- Evaluated after retries are exhausted
- First matching handler executes
- Error info stored in specified variable
- Workflow continues from catch step

## Error Information Format

Error information stored in catch handlers:

```json
{
  "Error": "timeout",
  "Cause": "context deadline exceeded", 
  "Details": {}
}
```

## Programming API

### Working with Errors

```go
// Create a custom error
err := workflow.NewWorkflowError("permission-denied", "Access was denied")

// Classify an existing error, returning a WorkflowError
workflowErr := workflow.ClassifyError(err)

// Determine if an error matches a given error type
matches := workflow.MatchesErrorType(err, workflow.ErrorTypeTimeout)
```

### Error Wrapping

WorkflowError supports Go's error wrapping:

```go
originalErr := errors.New("network failed")
workflowErr := &workflow.WorkflowError{
    Type:    workflow.ErrorTypeTimeout,
    Cause:   originalErr.Error(),
    Wrapped: originalErr,
}

// Use with errors.Is and errors.As
if errors.Is(workflowErr, originalErr) {
    // Handle wrapped error
}
```

## Example

```yaml
name: "resilient-workflow"
steps:
  - name: "risky-operation"
    activity: "http"
    parameters:
      url: "https://api.example.com/data"
    retry:
      - error_equals: ["timeout"]
        max_retries: 3
        base_delay: "2s"
        backoff_rate: 2.0
        jitter_strategy: "FULL"
    catch:
      - error_equals: ["permission-denied"] 
        next: "handle-auth-error"
        store: "auth_error"
      - error_equals: ["all"]
        next: "handle-error"
        store: "error_info"
    next:
      - step: "process-data"

  - name: "handle-auth-error"
    activity: "print"
    parameters:
      message: "Auth failed: ${state.auth_error.Cause}"

  - name: "handle-error"
    activity: "print" 
    parameters:
      message: "Error: ${state.error_info.Error}"
```

## Best Practices

1. Order catch handlers from most specific to most general
2. Use custom error types for domain-specific failures
3. Consider including an `"all"` catch handler as final fallback
4. Fatal errors cannot be caught by `"all"` - only by `"fatal_error"` 
