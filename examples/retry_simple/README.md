# Simple Retry

This example demonstrates basic retry configuration for handling transient failures.

## Key Concepts

### Retry Configuration

Steps can specify retry behavior using the `Retry` field:

```go
{
    Name:     "Call My Operation",
    Activity: "my_operation",
    Retry: []*workflow.RetryConfig{{
        ErrorEquals: []string{workflow.ErrorTypeActivityFailed},
        MaxRetries:  2,
    }},
}
```

### Error Types

The `ErrorEquals` field specifies which error types trigger a retry:

- `ErrorTypeActivityFailed` - Matches any non-timeout activity failure
- `ErrorTypeTimeout` - Matches timeout errors (context deadline, connection timeout)
- `ErrorTypeAll` or `*` - Matches all errors

### Retry Behavior

In this example:
1. First attempt fails with "service is temporarily unavailable"
2. First retry (attempt 2) also fails
3. Second retry (attempt 3) succeeds with "SUCCESS"

The workflow completes successfully after 3 attempts.

### Typed Activity Functions

The example uses `NewTypedActivityFunction` for type-safe activity definitions:

```go
myOperation := func(ctx workflow.Context, input map[string]any) (string, error) {
    // Return typed result
    return "SUCCESS", nil
}

workflow.NewTypedActivityFunction("my_operation", myOperation)
```

## Running the Example

```bash
go run main.go
```

Expected output:
```
Workflow completed successfully! Result: SUCCESS
Outputs:
{
  "result": "SUCCESS"
}
```

## See Also

- [retry](../retry/) - Advanced retry with backoff and catch handlers
- [error_handling](../error_handling/) - Catch handlers for error recovery
