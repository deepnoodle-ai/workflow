# Activities

Activities are the units of work in a workflow. Each step that performs an
action references an activity by name. The engine looks up the activity in
an `ActivityRegistry`, resolves parameters, and calls its `Execute` method.

## The Activity interface

The basic interface is intentionally minimal:

```go
type Activity interface {
    Name() string
    Execute(ctx workflow.Context, parameters map[string]any) (any, error)
}
```

The `parameters` map comes from the step's `Parameters` field after template
evaluation. The return value is stored in the branch variable named by the
step's `Store` field. If `Store` is empty, the return value is discarded.

## Creating activities

### From a function (untyped)

The quickest way to create an activity is `ActivityFunc`:

```go
greet := workflow.ActivityFunc("greet", func(ctx workflow.Context, params map[string]any) (any, error) {
    name := params["name"].(string)
    return fmt.Sprintf("Hello, %s!", name), nil
})
```

Parameters arrive as `map[string]any` and you cast them yourself. This is
fine for simple activities but gets tedious with many parameters.

### From a function (typed)

`TypedActivityFunc` auto-marshals the parameter map into a struct:

```go
type FetchInput struct {
    URL     string `json:"url"`
    Timeout int    `json:"timeout"`
}

fetch := workflow.TypedActivityFunc("fetch",
    func(ctx workflow.Context, input FetchInput) (string, error) {
        // input.URL and input.Timeout are populated from the step's Parameters
        resp, err := http.Get(input.URL)
        if err != nil {
            return "", err
        }
        defer resp.Body.Close()
        body, _ := io.ReadAll(resp.Body)
        return string(body), nil
    },
)
```

The struct's `json` tags must match the parameter keys in the step definition.
The marshaling uses `encoding/json` internally, so the same rules apply.

The result type is also preserved. If your function returns `(int, error)`,
the value stored via `Store` is an `int`, not `any`.

### From a struct (typed)

If your activity has dependencies (an HTTP client, a database connection),
implement the `TypedActivity` interface on a struct and wrap it:

```go
type TypedActivity[TParams, TResult any] interface {
    Name() string
    Execute(ctx workflow.Context, parameters TParams) (TResult, error)
}
```

```go
type EmailSender struct {
    client *smtp.Client
}

func (e *EmailSender) Name() string { return "send_email" }

func (e *EmailSender) Execute(ctx workflow.Context, input EmailInput) (string, error) {
    // use e.client to send the email
    return "sent", nil
}

// Wrap so it satisfies the Activity interface for the registry
activity := workflow.NewTypedActivity(&EmailSender{client: smtpClient})
```

## Registering activities

Activities must be registered before creating an execution. `NewExecution`
validates that every step's activity name exists in the registry:

```go
reg := workflow.NewActivityRegistry()

// Register returns an error if the name is already taken
err := reg.Register(myActivity)

// MustRegister panics on duplicate — convenient at init time
reg.MustRegister(activity1, activity2, activity3)

// Lookup
activity, ok := reg.Get("fetch")

// List all registered names
names := reg.Names()
```

Pass the registry when creating an execution:

```go
exec, err := workflow.NewExecution(wf, reg,
    workflow.WithInputs(map[string]any{"url": "https://example.com"}),
)
```

## Using context inside activities

Activities receive `workflow.Context`, which embeds `context.Context`. Pass
it directly to any stdlib API:

```go
func(ctx workflow.Context, params map[string]any) (any, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
    // ...
}
```

Beyond the standard context methods, `workflow.Context` provides:

| Method | Description |
|--------|-------------|
| `Get(key)` | Read a branch-local variable |
| `Set(key, value)` | Write a branch-local variable |
| `Delete(key)` | Remove a branch-local variable |
| `Inputs()` | Read-only workflow inputs |
| `Logger()` | Structured logger (`*slog.Logger`) |
| `BranchID()` | Current branch identifier |
| `StepName()` | Current step name |
| `Wait(topic, timeout)` | Durable signal wait (see [Signals, Sleep, and Pause](signals-sleep-pause.md)) |
| `History()` | Replay-safe cache (see [Signals, Sleep, and Pause](signals-sleep-pause.md)) |
| `ReportProgress(detail)` | Report intra-activity progress |

### Storing results

The `Store` field on a step saves the activity's return value into a branch
variable. Store names must be bare variable names — `"result"`, not
`"state.result"`. The `state.` prefix is only used in templates and edge
conditions.

```go
{
    Name:     "Fetch Data",
    Activity: "fetch",
    Parameters: map[string]any{"url": "${inputs.api_url}"},
    Store:    "data",  // activity return value → state.data
}
```

### Reporting progress

Long-running activities can report progress during execution:

```go
func(ctx workflow.Context, params map[string]any) (any, error) {
    for i, batch := range batches {
        ctx.ReportProgress(workflow.ProgressDetail{
            Message: fmt.Sprintf("Processing batch %d of %d", i+1, len(batches)),
            Data:    map[string]any{"batch": i + 1, "total": len(batches)},
        })
        process(batch)
    }
    return len(batches), nil
}
```

Progress updates are dispatched asynchronously to the configured
`StepProgressStore`. If no store is configured, `ReportProgress` is a no-op.

## Returning errors

Return a plain `error` for transient failures that should be retried:

```go
return nil, fmt.Errorf("connection refused")
```

Return a `WorkflowError` for structured errors with a type that retry and
catch handlers can match on:

```go
return nil, workflow.NewWorkflowError("validation-error", "email is invalid")
```

See [Error Handling](error-handling.md) for details on retry and catch
configuration.

## Built-in activities

The library ships activities in three packages:

### `activities/` — core activities

| Name | Constructor | Description |
|------|-------------|-------------|
| `print` | `NewPrintActivity()` | Print a message to stdout |
| `print` | `NewPrintActivityTo(w)` | Print a message to a custom writer |
| `time` | `NewTimeActivity()` | Return the current time |
| `json` | `NewJSONActivity()` | Parse or stringify JSON |
| `random` | `NewRandomActivity()` | Generate a random number (`min`, `max`) |
| `fail` | `NewFailActivity()` | Always fail with a given error (`error`, `type`) |
| `workflow.child` | `NewChildWorkflowActivity(executor)` | Execute a child workflow |

### `activities/httpx/` — HTTP client

| Name | Constructor | Description |
|------|-------------|-------------|
| `http` | `NewHTTPActivity()` | Make an HTTP request (`url`, `method`, `headers`, `body`) |

### `activities/contrib/` — host-touching activities

These are useful for prototyping and CLI workflows. Review security
implications before enabling in multi-tenant or untrusted-input contexts.

| Name | Constructor | Description |
|------|-------------|-------------|
| `shell` | `NewShellActivity()` | Execute a shell command (`command`) |
| `file` | `NewFileActivity()` | Read/write files (`operation`, `path`, `content`) |

### Registering built-ins

```go
reg := workflow.NewActivityRegistry()
reg.MustRegister(
    activities.NewPrintActivity(),
    activities.NewTimeActivity(),
    activities.NewRandomActivity(),
    httpx.NewHTTPActivity(),
)
```
