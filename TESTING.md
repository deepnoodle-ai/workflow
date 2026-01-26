# Testing Guide

This document describes how to run tests and the testing conventions used in this project.

## Running Tests

### All Tests

```bash
go test ./...
```

Or using make:

```bash
make test
```

### With Race Detection

```bash
go test -race ./...
```

### With Coverage

```bash
make cover
```

This generates a coverage report and opens it in your browser.

### Specific Package

```bash
go test ./runners
go test ./internal/services
go test ./cmd/worker
```

## Test Categories

### Unit Tests

Most tests are unit tests that run in isolation with mocked dependencies. These tests:
- Run fast (< 1 second)
- Don't require external services
- Use the `internal/testutil` package for mock implementations

### Integration Tests

Some tests require Docker for testcontainers:

```bash
# PostgreSQL integration tests
go test -run "TestPostgres" ./...
```

These tests:
- Start a real PostgreSQL instance via Docker
- Test actual database operations
- Are slower but provide higher confidence

### Container Tests

Tests in `cmd/worker/executor_test.go` include container execution tests that:
- Require Docker to be available
- Are skipped automatically if Docker is not installed
- Test actual container execution with Alpine images

## Test Patterns

### Table-Driven Tests

We use table-driven tests for testing multiple scenarios:

```go
func TestSomething(t *testing.T) {
    tests := []struct {
        name     string
        input    string
        expected string
    }{
        {"empty", "", "default"},
        {"valid", "test", "test"},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := DoSomething(tt.input)
            assert.Equal(t, tt.expected, result)
        })
    }
}
```

### Subtests

We use subtests for related test cases:

```go
func TestService(t *testing.T) {
    t.Run("Create", func(t *testing.T) {
        // test create
    })

    t.Run("Get", func(t *testing.T) {
        // test get
    })
}
```

### Mock Utilities

The `internal/testutil` package provides mock implementations:

- `MockExecutionRepository` - Mock for execution storage
- `MockTaskRepository` - Mock for task storage
- `MockEventLog` - Mock for event logging
- `TestExecution()` - Helper to create test execution records
- `TestTask()` - Helper to create test task records

Example usage:

```go
func TestMyService(t *testing.T) {
    repo := testutil.NewMockTaskRepository()
    events := testutil.NewMockEventLog()

    // Inject errors for testing error paths
    repo.CreateErr = errors.New("db error")

    svc := NewService(repo, events)

    err := svc.DoSomething()
    assert.Error(t, err)

    // Verify events were logged
    evts := events.GetEventsByType(domain.EventTypeStepStarted)
    assert.Len(t, evts, 1)
}
```

## Adding New Tests

1. Create a `*_test.go` file alongside the code being tested
2. Use `testing` package and `github.com/stretchr/testify` for assertions
3. Follow existing patterns for consistency
4. Use mocks from `internal/testutil` where appropriate
5. Add subtests for different scenarios

## Coverage

To view coverage for a specific package:

```bash
go test -coverprofile=cover.out ./internal/services
go tool cover -html=cover.out
```

## Continuous Integration

Tests run automatically on:
- Push to `main` branch
- Pull requests to `main` branch

The CI pipeline includes:
- `go test -race ./...` - Tests with race detection
- `golangci-lint` - Static analysis
- Build verification for server and worker binaries

## Troubleshooting

### PostgreSQL Tests Failing

Ensure Docker is running:

```bash
docker info
```

### Container Tests Skipped

Install Docker if you want to run container execution tests:

```bash
# macOS
brew install --cask docker

# Linux
sudo apt-get install docker.io
```

### Import Errors

Run `go mod tidy` to fix dependency issues:

```bash
go mod tidy
```
