# Retry Example

This example demonstrates error handling and retry logic in workflows.

## What it shows

- **Retry Configuration**: Different retry policies for different steps
- **Error Handling**: Graceful handling of failures with exponential backoff
- **State Management**: Preserving state across retry attempts
- **Service Simulation**: Realistic simulation of unreliable external services

## The Workflow

1. **Initialize** - Welcome message
2. **Call Unreliable Service** - Simulates an external API with 70% failure rate
   - Retries up to 3 times with exponential backoff
   - Base delay: 500ms, max delay: 2s
3. **Service Success** - Confirms successful service call
4. **Validate Response** - Validates the service response
   - Retries up to 2 times with 200ms base delay
5. **Validation Success** - Confirms successful validation
6. **Final Success** - Completion message

## Running the Example

```bash
cd examples/retry
go run main.go
```

## Key Concepts Demonstrated

- **RetryConfig**: Configure different retry policies per step
- **Random Failures**: Realistic simulation of network/service issues
- **Exponential Backoff**: Intelligent retry timing to avoid overwhelming services
- **Error Context**: Meaningful error messages and state preservation 
