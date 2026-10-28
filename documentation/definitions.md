# Definitions

A definition is a JSON document (or a YAML document the host decodes and
passes to `LoadMap`). `Load(data, kinds)` checks its shape, resolves step
kinds against the host's, compiles every expression, checks the graph, and
**type-checks every expression and parameter block**. It reports every
problem at once, each with its step name, and returns a definition only when
there are none.

```yaml
name: string                 # optional
description: string          # optional
schemas:                     # optional; named types for output, inputs, event_schema
  triage: {type: object, required: [kind], properties: {kind: {enum: [bug_fix, feature, other]}}}
inputs:                      # optional; when present, unknown inputs are rejected
  repo: {type: string, required: true, description: "..."}
  depth: {type: integer, default: 2}
event_schema: github_pull_request   # optional; types the trigger, read as `event`
steps: [...]                 # the first step is the start step
on_failure: [...]            # optional; runs once, in order, when the run fails
outputs:                     # optional; templates over step values
  verdict: '${{ review?.verdict }}'
```

Without `outputs`, a run's outputs are the value of the last step on every
path that ended normally, keyed by step name.

## Steps

| Key | Meaning |
| --- | --- |
| `name` | Required. An identifier, unique across `steps` and `on_failure`, and not a global (`inputs`, `event`, `item`, `index`, `error`) or function name (`len`, `split`, `first`, `count`, ...). Expressions read the step's value by this name. |
| *kind key* | Exactly one: a host kind (`agent: reviewer`, `action: github.create_review`) or `join`, `set`, `sleep`. |
| `output` | The schema of the step's output: inline, a basic type (`output: string`), or a name under `schemas:`. Host kinds only. |
| `with` | Parameters, evaluated at dispatch. Host kinds only. |
| `each` | An expression producing a list. One dispatch per item, in parallel. |
| `concurrency` | With `each`: how many items may be in flight. Default: all. |
| `continue_on_error` | With `each`: failed items become `{error: {type, message}}` instead of failing the step. |
| `next` | Where to go: a step name, a list of names and `{step, when}`, or a `switch`. |
| `match` | For a list: `all` (default) follows every matching edge, `first` only the first. |
| `retry` | `{max_attempts, backoff, max_backoff, factor, errors}`, or a number for `max_attempts`. |
| `catch` | `[{errors, next}]`, or a step name: route a terminal failure instead of failing the run. |
| `timeout` | A duration such as `5m`, counted from when the attempt may start (dispatch, or its retry backoff), so time spent waiting for a worker counts. The attempt fails with type `timeout` (retryable). |
| `max_visits` | How many times a path may enter the step. Bounds loops. |

For a host kind, the kind key and every key not listed above (`model`,
`instructions`, ...) go into `Command.Config` unevaluated, and
`Kinds.ValidateConfig` checks them. The output schema travels with the
dispatch as `Command.OutputSchema`, so a host can ask a model for exactly that
shape.

### Control kinds

- `join: [a, b, c]` waits until no live path can still reach the join, then
  merges the paths that arrived and continues (see Paths below).
  Every named step must have settled on an arriving path; if one did not
  (a conditional edge skipped it), the run fails with type `join`.
  `join: []` names nothing and waits for every path that can reach it.
- `set: <value>` produces a value without the host. Strings may hold
  templates: `set: {total: '${{ len(items) }}'}`. Its type is inferred.
- `sleep: 1h` waits by asking the host for a timer.

## Routing

Two forms, for two jobs.

**`switch` routes on one value.** Most branching is "a step classified
something; go one of three ways":

```yaml
- name: classify
  agent: triage
  output: triage                    # kind: bug_fix | feature | other
  next:
    switch: classify.kind
    cases:
      bug_fix: fix
      feature: [build, announce]    # several targets fork the path
    default: other                  # or [] to end the path
```

Validation checks the cases against the value's type: a case the value can
never take is an error (`bugfix`), and so is a switch that does not handle
every value of an enum (or both booleans) without a `default`. A value with no
fixed set of values, or of unknown type, needs a `default`. Keys are compared
with a string value directly, and with the JSON encoding of a number or
boolean (`"true"`, `"3"`).

**`when` is for conditions.** An edge with `when` is followed when the
expression is true. It must be a boolean, statically and at run time:
`when: verify` (a list) is an error; write `len(verify) > 0`. With `match:
first`, a list of `when` edges is an if / else-if / else chain.

## Types

Every expression is type-checked when the definition is loaded, against:

- `inputs`, typed by their schemas. An input that is not required and has no
  default may be nil.
- `event`, typed by `event_schema`, or unchecked without one.
- each step's value: its `output` schema (or the host's, see
  [hosts](hosts.md)), the list of item outputs for an `each`, the inferred
  type of a `set`, nil for `join` and `sleep`. A step with no schema is
  unchecked.
- `item` and `index` in an `each` step's `with`, `error` in `on_failure`.

The checker reports misspelled fields (`classify.knd`, with a suggestion),
operations on the wrong kind of value, comparisons that are never true
(`classify.kind == "bugfix"`), `when` and `switch` misuse, and `with:` blocks
that do not fit the host's parameter schema for the kind.

**Maybe-nil values.** A step is certain to have run only if it settles on
every path to the reader. Validation works this out from the graph: steps
before a fork, steps a named join waits for, and steps every arrival saw.
Anything else may be nil, and reading through it needs `?.`:

```yaml
- name: post
  with:
    event: '${{ coalesce(report, approve).verdict }}'   # whichever ran
    note: '${{ report?.body }}'                          # nil if approve ran
```

`outputs` are evaluated once every path has ended, over everything that
settled on any path. There a step is certain if no run can complete
without it: the start step, both sides of an unconditional fork, a step
every exit passes through. A step after a `when` edge, a switch case that
ends the path, or a catch may still be nil.

A step's value on the paths only its `catch` leads to is `{error: {type,
message}}`, so a handler step reads `charge.error.message` directly. Where
a catch and a normal edge both lead, the value is either.

`coalesce(a, b, ...)` returns its first non-nil argument. When its arguments
are alternatives one of which must have run (both branches of a `match:
first` or a `switch` converge on the reader), and none of them can settle
with a nil value, its result is known not to be nil. Optional schema
properties (not in `required`) may be nil too, and a nil check narrows:
`a.n != nil && a.n > 3` and `if(a.n != nil, a.n, 0)` are fine.

`&&` and `||` return one of their operands, as in JavaScript, so
`a.name || "anon"` is a string, and `a.items && len(a.items) > 0` is not a
boolean (it may be the list). A value that may be of several kinds, such as
`a.name || 0`, cannot be used in arithmetic, comparisons or field reads, and
cannot be a `when` or a `switch`.

**The promise.** If a definition validates and every step's output matches
its schema, no expression fails at run time with a missing name or field, a
nil, or a wrong kind. Steps without schemas, and `event` without
`event_schema`, are outside the promise; Apply's runtime validation of
params and outputs still applies. `soundness_test.go` holds the
counterexamples found in review.

Schemas are a JSON Schema subset: `type` (one or a list; `null` makes the
value nullable), `properties`, `required`, `items`, `enum`,
`additionalProperties`, `description`. Two rules lean toward catching
mistakes: expressions may read only declared properties unless
`additionalProperties` is true or a schema, and at run time undeclared
properties are allowed unless `additionalProperties` is false.

At run time Apply enforces the same contract: inputs and the trigger are
validated at Start (an error to the host), params at dispatch (the step fails
with `invalid_params` and is never dispatched), and outputs on completion
(the step fails with `invalid_output`, not retried). A declared property that
is absent reads as nil.

## Expressions

`each`, `when` and `switch` are bare
[expr](https://github.com/deepnoodle-ai/expr) expressions. In `with`, `set`
and `outputs`, a string without `${{ ... }}` is a literal, a string that is
exactly one `${{ ... }}` keeps the expression's type (a list stays a list), and
anything else renders as text, including a template with spaces around it
(`' ${{ 1 + 2 }} '` is the string `" 3 "`):

```yaml
with:
  state: pending                                         # the string "pending"
  findings: '${{ correctness.items }}'                   # a list
  title: 'Review of #${{ event.pull_request.number }}'   # a string
  run: 'echo ${HOME}'                                    # text: only ${{ }} is a template
  note: 'write $${{ x }} for a literal'                  # $${{ escapes the opener
```

Globals:

| Name | Where | Value |
| --- | --- | --- |
| `inputs` | everywhere | the run's inputs, with defaults applied |
| `event` | everywhere | the trigger passed to Start |
| *step name* | everywhere | its output once it settled on this path, else nil; an `each` step's is the list of item outputs in item order; a caught failure's is `{error: {type, message}}` |
| `item`, `index` | `with` of an `each` step | the element and its position |
| `error` | `on_failure` | `{step, step_id, type, message}` |

Besides expr's builtins, string, math and collection functions, expressions
have `concat(a, b, ...)`, which joins lists, and `coalesce(a, b, ...)`.
Field names may be Go keywords: `error.type`, `event.sender.type`.

Keep expressions as glue: references and a little shaping. Logic that is
complex, consults the outside world, or deserves a record in the run's history
belongs in a step.

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
`each` expression fails, when its params or output do not match their schemas,
or when its output is too large. An `each` step fails as soon as one item
fails (after its retries), unless `continue_on_error`: the items still in
flight are cancelled, the rest are never dispatched, and their late results
are ignored. The step's error is that item's. A matching `catch` routes the
path instead.
Otherwise the run fails: everything in flight is cancelled, and the
`on_failure` steps run in order with `error` bound. If one of them fails, the
run fails with both errors (`RunError.OnFailure`).

A cancelled run does not run `on_failure`.

## Limits

A run keeps step values in its state because expressions read them. The
host sets the caps in `Definition.Limits`:

- `MaxOutputBytes` (default 256 KiB) caps one value: a task's output or a
  `set` value, as JSON. A larger one fails the step with
  `output_too_large`.
- `MaxStateBytes` (default 8 MiB) caps what the run holds at once: every
  kept output, `set` value and `each` item list (`State.Bytes`). A value
  that would exceed it fails its step with `state_too_large`.

What a run holds is what it can still read, not everything it has done.
When a loop goes round again, a step's new visit supersedes the old one
once it settles, and the old visit is dropped from the state along with
its bytes. A path that has not moved on keeps the visit it saw, and the
run outputs see the latest visit of each step. So a run's state is bounded
by its latest passes, and a loop whose values fit twice under the cap can
run indefinitely: a poll and `sleep` loop that runs for months holds one
poll's output (two while the next poll's result arrives). Results
delivered for a dropped visit are ignored like any late result. The
state's `Nodes` are therefore not a history; hosts keep their own (the
postgres host's `steps` and `events` tables).

One exception remains: a loop that forks and joins on every pass adds a
small record per branch per pass to `State.Paths` (no step values). It is
not counted in `MaxStateBytes`.

## Validation

Besides shape, expressions and types, `Load` rejects: unknown `next`,
`switch`, `catch` and `join` targets; unknown schema names; steps unreachable
from the start step; a join naming a step that is not upstream of it; a cycle
with no `max_visits` or `sleep` step; modifiers on kinds that do not take
them; and kind config errors from `Kinds.ValidateConfig`.
