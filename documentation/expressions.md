# Expressions and Templating

Step parameters and edge conditions are evaluated by the bundled expression
engine (`github.com/deepnoodle-ai/expr`), a small Go-subset evaluator. It
is wired in automatically via `DefaultScriptCompiler()` — consumers do not
need to configure anything unless they want a different engine.

## Parameter templates

Parameter values use `${expr}` syntax. Available variables:
- `state.*` — branch-local variables
- `inputs.*` — workflow inputs

```go
Parameters: map[string]any{
    "message": "Counter is ${state.counter}, max is ${inputs.max_count}",
    "url":     "${inputs.api_url}",
    "count":   "${state.record_count}",
}
```

### Type preservation

When a template covers the **entire trimmed value** (a single `${...}` with
nothing around it), the result preserves its native Go type:

```go
"number": "${state.random_number}"    // → int (if state.random_number is int)
"flag":   "${state.is_ready}"         // → bool
"data":   "${state.response}"         // → map, slice, or whatever the type is
```

When mixed with surrounding text, the result is interpolated into a string:

```go
"message": "Count: ${state.counter}"          // → string "Count: 42"
"path":    "/api/${inputs.version}/data"      // �� string "/api/v2/data"
```

This distinction matters when activities expect typed parameters. If your
activity expects an integer, use a pure template `"${state.count}"` rather
than `"${state.count} items"`.

## Edge conditions

Conditions use the same expression syntax **without** the `${...}` wrapper:

```go
Next: []*workflow.Edge{
    {Step: "High",   Condition: "state.score > 80"},
    {Step: "Medium", Condition: "state.score > 50"},
    {Step: "Low"},   // unconditional
}
```

### Comparison operators

Standard comparison operators work as expected:

```go
"state.count > 10"
"state.count <= inputs.max_count"
"state.status == \"ready\""
"state.is_prime == true"
"state.category != \"skip\""
```

### Logical operators

Combine conditions with `&&` and `||`:

```go
"state.is_prime == true && state.category == \"small\""
"state.retries > 3 || state.status == \"failed\""
```

### String literals

String literals must be **double-quoted**. The expression engine follows
Go's lexical rules — single-quoted strings are not valid:

```go
// Correct
Condition: `state.category == "large"`

// Wrong — single quotes are not valid
Condition: `state.category == 'large'`
```

Use Go raw string literals (backticks) for the outer Condition string to
avoid escaping double quotes.

## State mutation

The expression engine is **expression-only** — it cannot mutate state.
There is no `script` activity in this library. When a workflow needs to
change state, compute the new value in a Go activity and write it back
via the step's `Store` field:

```go
// Define a simple increment activity
increment := workflow.TypedActivityFunc("increment",
    func(ctx workflow.Context, input struct{ Value int `json:"value"` }) (int, error) {
        return input.Value + 1, nil
    },
)

// Use it in a step
{
    Name:     "Increment",
    Activity: "increment",
    Parameters: map[string]any{"value": "${state.counter}"},
    Store:    "counter",  // result overwrites state.counter
}
```

## Store field

The `Store` field names a branch variable where the activity's return value
is saved. Store names must be **bare variable names** — `"counter"`, not
`"state.counter"`. The `state.` prefix is only used in templates and
conditions.

```go
{
    Name:     "Fetch",
    Activity: "http",
    Parameters: map[string]any{"url": "${inputs.api_url}"},
    Store:    "response",     // correct: bare name
    // Store: "state.response"  // wrong: don't use state. prefix
}
```

## Validation

Templates and conditions are validated at two stages:

1. **`workflow.New`** — structural validation: template syntax
   (`${...}` delimiters are balanced), no empty expressions.
2. **`NewExecution`** — binding validation: every expression compiles
   against the configured script compiler. Failures are returned as a
   `*ValidationError` with one `ValidationProblem` per issue.

This means syntax errors in templates are caught before any step runs.

## Swapping in a different engine

Consumers who want Risor, CEL, expr-lang, or anything else implement
the `script.Compiler` interface:

```go
type Compiler interface {
    Compile(ctx context.Context, code string) (Script, error)
}

type Script interface {
    Evaluate(ctx context.Context, env map[string]any) (Value, error)
}
```

Pass it when creating an execution:

```go
exec, err := workflow.NewExecution(wf, reg,
    workflow.WithScriptCompiler(myCompiler),
)
```

The `script` package exports helpers for writing a custom `Value`
implementation:

| Helper | Description |
|--------|-------------|
| `script.IsTruthyValue(val)` | Converts a Go value to a boolean (for edge conditions) |
| `script.EachValue(val)` | Converts a Go value to `[]any` (for `Each` iteration) |

See `script_compiler.go` in the root module for the expr adapter that the
library ships by default — it serves as a reference implementation.

### When to swap

The default expr engine covers most use cases: arithmetic, comparisons,
boolean logic, and field access. Consider a custom engine when you need:

- **Full scripting** (loops, functions, assignments) — Risor, Starlark
- **Policy evaluation** — CEL, OPA/Rego
- **Domain-specific logic** — a custom evaluator for your business rules
