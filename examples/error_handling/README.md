# Error Handling Example

This example demonstrates the comprehensive error handling features of the workflow library, including retry mechanisms and catch handlers.

## Features Demonstrated

### 1. Retry Configuration

The workflow supports multiple retry configurations per step, each targeting specific error types. The first matching configuration determines the retry behavior for the entire retry sequence:

```go
Retry: []*workflow.RetryConfig{
    {
        // Retry timeout errors with exponential backoff
        ErrorEquals:    []string{workflow.ErrorTimeout},
        MaxRetries:     3,
        BaseDelay:      time.Second * 2,
        BackoffRate:    2.0,
        MaxDelay:       time.Second * 10,
        JitterStrategy: workflow.JitterFull,
    },
    {
        // Retry generic task failures with different settings
        ErrorEquals:    []string{workflow.ErrorTaskFailed},
        MaxRetries:     2,
        BaseDelay:      time.Second * 1,
        BackoffRate:    1.5,
        JitterStrategy: workflow.JitterNone,
    },
},
```

### 2. Catch Handlers

When retries are exhausted or specific errors occur, catch handlers provide fallback mechanisms:

```go
Catch: []*workflow.CatchConfig{
    {
        // Catch permission errors and redirect to permission handler
        ErrorEquals: []string{"PermissionDenied"},
        Next:        "handle-permission-error",
        Store:       "error_info",
    },
    {
        // Catch all other errors and redirect to general error handler
        ErrorEquals: []string{workflow.ErrorAll},
        Next:        "handle-general-error",
        Store:       "error_info",
    },
},
```

### 3. Error Types

The library includes essential error types and supports custom error types:

#### Built-in Essential Types:
- `States.ALL` - Matches any known error name (except terminal errors)
- `States.TaskFailed` - Matches any error except timeout
- `States.Timeout` - Timeout-related errors
- `States.Runtime` - Runtime exceptions (terminal)

#### Custom Error Types:
- `PermissionDenied` - Custom permission/authorization errors
- Any other string - Define your own domain-specific error types

### 4. Retry Features

#### Backoff Rate
Configure exponential backoff multiplier:
- `BackoffRate: 2.0` - Default exponential backoff (doubles delay each attempt)
- `BackoffRate: 1.5` - Custom multiplier for gentler backoff

#### Maximum Delay
Prevent excessive wait times:
- `MaxDelay: time.Second * 10` - Cap retry delays at 10 seconds

#### Jitter Strategy
Reduce retry thundering herd:
- `JitterStrategy: workflow.JitterFull` - Randomize delay between 0 and calculated delay
- `JitterStrategy: workflow.JitterNone` - No jitter (default)

### 5. Error Output Structure

When catch handlers are triggered, error information is provided in structured format:

```json
{
  "Error": "States.TaskFailed",
  "Cause": "TaskFailure: Generic task failure",
  "Details": null
}
```

### 6. Store Field

Control where error information is stored in workflow state:
- `"error_info"` - Store error under `error_info` variable
- `"state.error_info"` - Explicit state variable notation
- Empty - Error info not stored

### 7. Go Error Wrapping

The WorkflowError type supports Go's standard error wrapping patterns:

```go
// Activities can wrap existing errors
originalErr := errors.New("connection failed")
return nil, workflow.NewWorkflowErrorWrapping(workflow.ErrorTimeout, originalErr)

// Use with errors.Is and errors.As
if errors.Is(workflowErr, originalErr) {
    // Handle specific wrapped error
}
```

## Running the Example

```bash
go run main.go
```

The example simulates an unreliable task that:
- Fails 60% of the time with different error types
- 20% timeout errors (will be classified as `States.Timeout`)
- 20% permission errors (custom `PermissionDenied` type)  
- 20% generic task failures (will be classified as `States.TaskFailed`)
- 40% success rate

The workflow will:
1. Attempt the task with appropriate retries based on error type
2. If retries are exhausted, redirect to appropriate catch handlers
3. Execute recovery procedures

## Expected Behavior

- **Timeout errors**: Retried up to 3 times with exponential backoff and jitter
- **Generic failures**: Retried up to 2 times with custom backoff rate
- **Permission errors**: Immediately caught and handled by permission-specific handler
- **All other errors**: Caught by general error handler
- **Success**: Proceed to success step and complete normally

## Key Improvements in This Version

1. **Simplified Error Types**: Only essential built-in types, with support for custom types
2. **Go Error Wrapping**: Full support for `errors.Is`, `errors.As`, and `Unwrap()`
3. **Store Field**: Simplified error storage using variable names instead of JSON paths
4. **Better Error Classification**: Automatic classification with wrapping support
5. **Removed Legacy Dependencies**: No longer depends on RecoverableError interface

## Output

The execution will show detailed logging including:
- Retry attempts with delays and error types
- Catch handler execution when triggered
- Final workflow completion status 