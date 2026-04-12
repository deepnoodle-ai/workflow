# Testing

The `workflowtest` package provides test utilities following the Go stdlib
convention (like `net/http/httptest`). It simplifies writing tests for both
complete workflows and individual activities.

## Quick start

The simplest way to test a workflow:

```go
import (
    "testing"

    "github.com/deepnoodle-ai/workflow"
    "github.com/deepnoodle-ai/workflow/workflowtest"
)

func TestMyWorkflow(t *testing.T) {
    wf, err := workflow.New(workflow.Options{
        Name: "test-workflow",
        Steps: []*workflow.Step{
            {
                Name:     "Greet",
                Activity: "greet",
                Parameters: map[string]any{"name": "${inputs.name}"},
                Store:    "greeting",
            },
        },
        Outputs: []*workflow.Output{
            {Name: "greeting", Variable: "greeting"},
        },
    })
    if err != nil {
        t.Fatal(err)
    }

    greet := workflow.TypedActivityFunc("greet",
        func(ctx workflow.Context, params map[string]any) (string, error) {
            return "Hello, " + params["name"].(string) + "!", nil
        },
    )

    result := workflowtest.Run(t, wf, []workflow.Activity{greet},
        map[string]any{"name": "Alice"},
    )

    if !result.Completed() {
        t.Fatalf("expected completed, got %s", result.Status)
    }
    if greeting, ok := result.OutputString("greeting"); !ok || greeting != "Hello, Alice!" {
        t.Fatalf("unexpected greeting: %q", greeting)
    }
}
```

`workflowtest.Run` handles the boilerplate: it creates a registry, sets up
an in-memory checkpointer, discards logs, and fails the test on
infrastructure errors.

## Run with options

For more control, use `RunWithOptions`:

```go
cp := workflowtest.NewMemoryCheckpointer()

result := workflowtest.RunWithOptions(t, wf,
    []workflow.Activity{myActivity},
    map[string]any{"key": "value"},
    workflowtest.TestOptions{
        ExecutionID:       "test-exec-001",
        Checkpointer:     cp,
        Callbacks:        myCallbacks,
        StepProgressStore: myStore,
    },
)
```

### TestOptions fields

| Field | Description |
|-------|-------------|
| `ExecutionID` | Fixed execution ID (auto-generated if empty) |
| `Checkpointer` | Override the default in-memory checkpointer |
| `Callbacks` | Receive execution lifecycle events |
| `StepProgressStore` | Receive step progress updates |

## Mock activities

Stub activities that return a fixed result or error:

```go
// Always returns the given value
stub := workflowtest.MockActivity("fetch", map[string]any{"items": 42})

// Always returns the given error
failing := workflowtest.MockActivityError("fetch", errors.New("connection refused"))
```

These are useful when testing workflow logic (branching, error handling,
retries) without exercising real activity implementations.

### Example: testing error handling

```go
func TestRetryExhausted(t *testing.T) {
    wf, _ := workflow.New(workflow.Options{
        Name: "retry-test",
        Steps: []*workflow.Step{
            {
                Name:     "Flaky",
                Activity: "flaky_api",
                Retry: []*workflow.RetryConfig{
                    {ErrorEquals: []string{"all"}, MaxRetries: 2, BaseDelay: time.Millisecond},
                },
                Catch: []*workflow.CatchConfig{
                    {ErrorEquals: []string{"all"}, Next: "Fallback", Store: "error_info"},
                },
                Next: []*workflow.Edge{{Step: "Success"}},
            },
            {Name: "Success", Activity: "noop"},
            {Name: "Fallback", Activity: "noop"},
        },
    })

    result := workflowtest.Run(t, wf,
        []workflow.Activity{
            workflowtest.MockActivityError("flaky_api", errors.New("timeout")),
            workflowtest.MockActivity("noop", nil),
        },
        nil,
    )

    if !result.Completed() {
        t.Fatalf("expected completed (via catch), got %s", result.Status)
    }
}
```

## Unit testing activities with FakeContext

`FakeContext` implements `workflow.Context` without constructing a full
execution. Use it to test activity logic in isolation:

```go
import "github.com/deepnoodle-ai/workflow/workflowtest"

func TestMyActivity(t *testing.T) {
    ctx := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{
        Inputs:    map[string]any{"api_key": "test-key"},
        Variables: map[string]any{"counter": 5},
    })

    result, err := myActivity.Execute(ctx, map[string]any{"url": "https://example.com"})
    if err != nil {
        t.Fatal(err)
    }

    // Check the result
    if result != "expected value" {
        t.Fatalf("unexpected result: %v", result)
    }

    // Check state mutations
    counter, _ := ctx.Get("counter")
    if counter != 6 {
        t.Fatalf("expected counter=6, got %v", counter)
    }
}
```

### FakeContextOptions

| Field | Description | Default |
|-------|-------------|---------|
| `Inputs` | Workflow inputs | empty |
| `Variables` | Initial branch-local state | empty |
| `Logger` | Structured logger | discard logger |
| `Compiler` | Script compiler (needed for template tests) | nil |
| `BranchID` | Value returned by `ctx.BranchID()` | `"fake-branch"` |
| `StepName` | Value returned by `ctx.StepName()` | `"fake-step"` |
| `WaitFunc` | Custom handler for `ctx.Wait()` calls | returns `(nil, nil)` |
| `OnProgress` | Callback for `ctx.ReportProgress()` | no-op |

### Testing Wait behavior

```go
ctx := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{
    WaitFunc: func(topic string, timeout time.Duration) (any, error) {
        if topic == "approval" {
            return "approved", nil
        }
        return nil, workflow.ErrWaitTimeout
    },
})

result, err := myActivity.Execute(ctx, params)
```

### Testing progress reporting

```go
var reports []workflow.ProgressDetail

ctx := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{
    OnProgress: func(detail workflow.ProgressDetail) {
        reports = append(reports, detail)
    },
})

myActivity.Execute(ctx, params)

if len(reports) != 3 {
    t.Fatalf("expected 3 progress reports, got %d", len(reports))
}
```

### Testing with cancellation

```go
ctx := workflowtest.NewFakeContext(workflowtest.FakeContextOptions{})

cancelCtx, cancel := context.WithCancel(context.Background())
cancel() // cancel immediately
ctx.SetContext(cancelCtx)

_, err := myActivity.Execute(ctx, params)
if !errors.Is(err, context.Canceled) {
    t.Fatal("expected context.Canceled")
}
```

## MemoryCheckpointer

The in-memory checkpointer is useful for testing suspend/resume cycles and
inspecting checkpoint state:

```go
cp := workflowtest.NewMemoryCheckpointer()

// Run a workflow that suspends
exec1, _ := workflow.NewExecution(wf, reg, workflow.WithCheckpointer(cp))
res1, _ := exec1.Execute(context.Background())

// Inspect the checkpoint
checkpoints := cp.Checkpoints()
checkpoint := checkpoints[exec1.ID()]
// assert on checkpoint.Status, checkpoint.BranchStates, etc.

// Resume
exec2, _ := workflow.NewExecution(wf, reg,
    workflow.WithCheckpointer(cp),
    workflow.WithExecutionID(exec1.ID()),
)
res2, _ := exec2.Execute(context.Background(), workflow.ResumeFrom(exec1.ID()))
```

## Testing patterns

### Test workflow completion

```go
result := workflowtest.Run(t, wf, activities, inputs)
if !result.Completed() {
    t.Fatalf("expected completed, got %s: %v", result.Status, result.Error)
}
```

### Test workflow outputs

```go
result := workflowtest.Run(t, wf, activities, inputs)

count, ok := result.OutputInt("record_count")
if !ok || count != 42 {
    t.Fatalf("expected record_count=42, got %v (ok=%v)", count, ok)
}

// Generic typed output
type Report struct {
    Title string
    Count int
}
report, ok := workflow.OutputAs[Report](result, "report")
```

### Test suspension

```go
cp := workflowtest.NewMemoryCheckpointer()
signals := workflow.NewMemorySignalStore()

result := workflowtest.RunWithOptions(t, wf, activities, inputs,
    workflowtest.TestOptions{Checkpointer: cp},
)

if !result.Suspended() {
    t.Fatal("expected suspension")
}
if result.Topics()[0] != "expected-topic" {
    t.Fatalf("unexpected topic: %s", result.Topics()[0])
}
```

### Test validation errors

```go
_, err := workflow.New(workflow.Options{
    Steps: []*workflow.Step{
        {Name: "A", Activity: "x", Next: []*workflow.Edge{{Step: "Z"}}},
    },
})

var ve *workflow.ValidationError
if !errors.As(err, &ve) {
    t.Fatal("expected ValidationError")
}
for _, p := range ve.Problems {
    t.Logf("problem: %s", p.Message)
}
```

## Assertions

The library uses its own test assertion helpers in `internal/require/`
(a tiny stdlib-only replacement for testify). In your own tests, use
whatever assertion library you prefer — `workflowtest` returns standard
Go types that work with any test framework.
