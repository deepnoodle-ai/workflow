# Definitions

A definition is a JSON document (or a YAML document the host decodes and
passes to `ParseMap`). `Parse` checks its shape; `Validate(kinds)` resolves
step kinds against the host's, compiles every expression, and checks the
graph. Validation reports every problem at once, each with its step name.

```yaml
name: string                 # optional
description: string          # optional
inputs:                      # optional; when present, unknown inputs are rejected
  repo: {type: string, required: true, description: "..."}
  depth: {type: integer, default: 2}
steps: [...]                 # the first step is the start step
on_failure: [...]            # optional; runs once, in order, when the run fails
outputs:                     # optional; templates over step values
  verdict: '${review.verdict}'
```

Input types are `string`, `number`, `integer`, `boolean`, `object`, `array`,
or omitted for any. Declared inputs without a default read as `nil`.

Without `outputs`, a run's outputs are the value of the last step on every
path that ended normally, keyed by step name.

## Steps

| Key | Meaning |
| --- | --- |
| `name` | Required. An identifier, unique across `steps` and `on_failure`, and not a global (`inputs`, `event`, `item`, `index`, `error`) or function name (`len`, `split`, `first`, `count`, ...). Expressions read the step's value by this name. |
| *kind key* | Exactly one: a host kind (`agent: reviewer`, `action: github.create_review`) or `join`, `set`, `sleep`. |
| `with` | Parameters, evaluated at dispatch. Host kinds only. |
| `each` | An expression producing a list. One dispatch per item, in parallel. |
| `concurrency` | With `each`: how many items may be in flight. Default: all. |
| `continue_on_error` | With `each`: failed items become `{error: {type, message}}` instead of failing the step. |
| `next` | Edges: a step name, or a list of names and `{step, when}`. |
| `match` | `all` (default): follow every matching edge. `first`: only the first. |
| `retry` | `{max_attempts, backoff, max_backoff, factor, errors}`, or a number for `max_attempts`. |
| `catch` | `[{errors, next}]`, or a step name: route a terminal failure instead of failing the run. |
| `timeout` | A duration such as `5m`. The attempt fails with type `timeout` (retryable). |
| `max_visits` | How many times a path may enter the step. Bounds loops. |

For a host kind, the kind key and every key not listed above (`model`,
`instructions`, `output`, ...) go into `Command.Config` unevaluated, and
`Kinds.ValidateConfig` checks them.

### Control kinds

- `join: [a, b, c]` waits until no live path can still reach the join, then
  merges the paths that arrived and continues (see Paths below).
  Every named step must have settled on an arriving path; if one did not
  (a conditional edge skipped it), the run fails with type `join`.
  `join: []` names nothing and waits for every path that can reach it.
- `set: <value>` produces a value without the host. Strings may hold
  templates: `set: {total: '${len(items)}'}`.
- `sleep: 1h` waits by asking the host for a timer.

## Expressions

`each` and `when` are bare [expr](https://github.com/deepnoodle-ai/expr)
expressions. In `with`, `set` and `outputs`, a string without `${...}` is a
literal, a string that is exactly one `${...}` keeps the expression's type (a
list stays a list), and anything else renders as text:

```yaml
with:
  state: pending                         # the string "pending"
  findings: '${correctness.items}'       # a list
  title: 'Review of #${event.pull_request.number}'   # a string
```

Globals:

| Name | Where | Value |
| --- | --- | --- |
| `inputs` | everywhere | the run's inputs, with defaults applied |
| `event` | everywhere | the trigger passed to Start |
| *step name* | after the step settled on this path | its output; an `each` step's is the list of item outputs in item order; a caught failure's is `{error: {type, message}}` |
| `item`, `index` | `with` of an `each` step | the element and its position |
| `error` | `on_failure` | `{step, step_id, type, message}` |

Validation rejects an expression that reads a name that is not a global or a
step, or a step that cannot have settled before the reader runs. Where that
depends on the path taken, reading an unsettled step fails at run time; use
`try(a.x, b.x)` to read whichever of two alternatives ran.

Besides expr's builtins, string, math and collection functions, expressions
have `concat(a, b, ...)`, which joins lists.

Field names that are Go keywords (`type`, `func`, `range`, ...) cannot follow
a dot; write `error["type"]`.

## Paths

A run starts with one path, `main`. When more than one edge matches, the path
forks: it ends, and each target starts a child path named after it
(`main.correctness`). A join merges the arriving paths. When they account
for every path their deepest common ancestor forked into, the run continues on
that ancestor, so after a fork and a join it is back on `main`. When they are
only part of that fork (two joins each take half), the join continues on a new
path named after it (`main.j1`). Either way the choice depends only on which
paths arrived, never on the order they arrived in. A path that matches no edge
ends.

A join waits only for paths that can reach it in the current pass of any loop
it is in: a path that could reach it only by going around a loop again does
not hold it up. When a loop re-enters a step, the steps downstream of it count
as not yet settled for that pass. The run completes when every path
has ended.

Step IDs name the path, the step, the visit (`@2` for a loop's second pass),
the item, and the attempt: `main.tests/lint@2[0]#3`.

## Failure

A step fails when its attempt fails and no retry applies, when its `with` or
`each` expression fails, or when its output is too large. A matching `catch`
routes the path instead. Otherwise the run fails: everything in flight is
cancelled, and the `on_failure` steps run in order with `error` bound. If one
of them fails, the run fails with both errors (`RunError.OnFailure`).

A cancelled run does not run `on_failure`.

## Validation

Besides shape and expressions, `Validate` rejects: unknown `next`, `catch` and
`join` targets; steps unreachable from the start step; a join naming a step
that is not upstream of it; a cycle with no `max_visits` or `sleep` step;
modifiers on kinds that do not take them; and kind config errors from
`Kinds.ValidateConfig`.
