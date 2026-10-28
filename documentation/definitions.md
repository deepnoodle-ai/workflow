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
schemas:                     # optional; named types for input and output
  triage: {type: object, required: [kind], properties: {kind: {enum: [bug_fix, feature, other]}}}
input:                       # optional; an object schema for the run's input, read as `input`
  type: object
  required: [repo]
  properties:
    repo: {type: string, description: "..."}
    depth: {type: integer, default: 2}
steps: [...]                 # the first step is the start step
on_failure: [...]            # optional; runs once, in order, when the run fails
finally: [...]               # optional; runs once, in order, once the outcome is decided
outputs:                     # optional; templates over step values
  verdict: '${{ review?.verdict }}'
```

Without `outputs`, a run's outputs are the value of the last step on every
path that ended normally, keyed by step name.

## Input

A run is started with one input, a JSON object, read in expressions as
`input`. The definition's `input:` is its schema: inline or the name of a
schema under `schemas:`, and it must describe an object (`type: object`).
Apply validates the input against it at Start: required fields come from
`required`, and a property's `default` fills it in when it is absent or nil,
so `input.depth` above is never nil. Load checks every default against its
property's schema. Without `input:`, the input is an untyped object.

The engine does not know what caused a run; that is the host's business.
A host that starts runs from events puts the event into the input. Mobius,
for example, starts an event-triggered run with the provider's payload
under `event` and what it knows about the event under `meta`:

```yaml
schemas:
  pull_request_event:
    type: object
    required: [repository, pull_request]
    properties:
      repository: {type: object, required: [full_name], properties: {full_name: {type: string}}}
      pull_request: {type: object, required: [number], properties: {number: {type: integer}}}
input:
  type: object
  required: [event]
  properties:
    event: pull_request_event
    meta: {type: object, properties: {type: {type: string}, source: {type: string}}}
steps:
  - name: review
    agent: reviewer
    with: {pr: '${{ input.event.pull_request.number }}'}
```

A scheduled or manual run may have no input at all.

## Outputs

`outputs` maps a name to a template, evaluated once every path has ended
(see [Types](#types) for which steps it can rely on). An entry may also
take the long form, which adds a schema and a description; the two forms
mix:

```yaml
outputs:
  verdict: '${{ review.verdict }}'            # short form
  summary:
    value: '${{ review.body }}'
    schema: {type: string}                    # inline, a basic type, or a name under schemas:
    description: The review's summary, for callers of this workflow as a tool.
```

An object with a `value` or `schema` key is the long form, and then only
`value`, `schema` and `description` are allowed. Any other object is a
short-form template object (`counts: {ok: '${{ len(a) }}'}`); to output a
literal object with a `value` key, use the long form: `{value: {value: 1}}`.

`Load` checks the type of each `value` against its `schema` the way it
checks `with:` against a kind's params schema: kinds, required properties,
nil, unexpected properties and enum values it can see. A value that may be
nil needs a nullable schema (`{type: [string, "null"]}`) or a `coalesce`.
When the run completes, Apply validates each value against its schema. A
mismatch Load could not prove (a number where the schema wants an
`integer`, a value from an untyped step) fails the run with type
`invalid_output` and no `Step`, listing every mismatch, and `on_failure`
runs as for any failure. A declared property that is absent is filled with
nil, as in step outputs.

**A workflow as a tool.** A host that lets an agent or another workflow
call a workflow needs its contract. `Definition.Outputs` holds each
output's `Value`, `Schema` and `Description`, and
`Definition.OutputSchema()` returns them as one JSON Schema object (a
property per output, all required, no others), ready for a tool
definition's output schema. Outputs without a schema are unchecked (`{}`).
A definition with no `outputs:` has no output contract (`OutputSchema()`
is nil).

## Steps

| Key | Meaning |
| --- | --- |
| `name` | Required. An identifier, unique across `steps`, `on_failure` and `finally`, and not a global (`input`, `item`, `index`, `error`, `run`) or function name (`len`, `split`, `first`, `count`, ...). Expressions read the step's value by this name. |
| *kind key* | Exactly one: a host kind (`agent: reviewer`, `action: github.create_review`) or `join`, `set`, `sleep`. |
| `output` | The schema of the step's output: inline, a basic type (`output: string`), or a name under `schemas:`. Host kinds only. |
| `with` | Parameters, evaluated at dispatch. Host kinds only. |
| `each` | An expression producing a list. One dispatch per item, in parallel. |
| `concurrency` | With `each`: how many items may be in flight. Default: all. |
| `continue_on_error` | With `each`: failed items become `{error: {type, message}}` instead of failing the step. Read item fields with `?.` (`it?.error`). |
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
boolean (`"true"`, `"3"`, `"1000000"`, never `"1e+06"`).

**`when` is for conditions.** An edge with `when` is followed when the
expression is true. It must be a boolean, statically and at run time:
`when: verify` (a list) is an error; write `len(verify) > 0`. With `match:
first`, a list of `when` edges is an if / else-if / else chain.

## Types

Every expression is type-checked when the definition is loaded, against:

- `input`, typed by the `input:` schema. A field that is not required and
  has no default may be nil, and reading a field that is not declared is an
  error. A definition with no `input:` accepts any object, so there `input`
  is an object whose fields are unchecked: `input.foo` validates and fails
  at run time if `foo` was not passed. Declare `input:` to have reads of it
  checked.
- each step's value: its `output` schema (or the host's, see
  [hosts](hosts.md)), the list of item outputs for an `each`, the inferred
  type of a `set` (inferred before anything is checked, so a step that
  reads a `set` step around a loop sees its type), nil for `join` and
  `sleep`. A step with no schema is
  unchecked.
- `item` and `index` in an `each` step's `with`, `error` in `on_failure`,
  `run` in `finally`.

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
a catch and a normal edge both lead, the value is either, so a field only
one of them has may be absent: read it with `?.` (`a?.error`, `a?.id`).
The same holds for any value that is one of several object shapes, such as
`coalesce(a, b)` over steps with different schemas, or the items of an
`each` with `continue_on_error`.

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
nil, or a wrong kind. `soundness_test.go` holds the counterexamples found in
review; each is now rejected by `Load` or runs cleanly.

The promise does not cover:

- **Untyped values.** A step with no schema, a schema that names no kind
  (`{}`, `true`, or only `properties` or a `description`), `input` without
  an `input:` schema, and the undeclared
  fields of an object schema with no `properties` or with
  `additionalProperties: true` are unchecked, and so is everything read
  from them. `b.k` on an untyped `b` validates and can fail with a missing
  key or a nil.
- **Values, not types.** A list index can be out of range (`a.items[0]` on
  an empty list fails), and functions can reject a value of the right kind
  (`int("abc")`, `1 / 0`). Check the value first (`len(a.items) > 0 &&
  ...`) or move the logic into a step.
- **Parameters and outputs Load cannot prove.** Load checks `with:`
  against the kind's params schema, and each long-form output against its
  schema, by type: kinds, required properties, nil, unexpected properties,
  and enum values it can see. A number where the schema wants an
  `integer`, or a string where it wants one of an enum, passes Load unless
  it is a literal Load can check. Apply checks the value when it
  dispatches the step, and the step fails with `invalid_params` without
  being sent to the host; an output is checked when the run completes, and
  the run fails with `invalid_output`. A step with no `with:` is checked
  as `with: {}`.

Schemas are a JSON Schema subset: `type` (one or a list; `null` makes the
value nullable), `properties`, `required`, `items`, `enum`,
`additionalProperties`, `default`, `description`, and `$ref` (or a bare
name) to a schema under `schemas:`. Two rules lean toward catching mistakes:
expressions may read only declared properties unless
`additionalProperties` is true or a schema, and at run time undeclared
properties are allowed unless `additionalProperties` is false.

Each schema becomes an expression type:

| Schema | Type | Reading it |
| --- | --- | --- |
| no schema, `{}`, `true`, or no `type` or `enum` | unchecked | anything validates |
| `string`, `boolean` | that kind | |
| `number`, `integer` | number | integer-ness is checked on values, not types |
| `{type: [string, number]}` | `number \| string` | only what every kind allows: `==`, storing it, passing it on |
| `{type: [string, "null"]}` | `string?` | needs `?.`, `coalesce` or a nil check |
| `{enum: [a, b]}` | `"a" \| "b"` | comparisons outside the enum are errors |
| `{enum: ["x", 1, null]}` | `(1 \| "x")?` | the union of the values' kinds; `null` makes it nullable |
| `{type: string, enum: [a, 1]}` | `"a"` | only values the type allows count |
| `{type: array, items: T}` | `list<T>` | `items` absent: a list of unchecked values |
| `{type: object}` | an open object | kind and nil are checked, its fields are unchecked |
| `{type: object, properties: ...}` | a closed object | only declared properties; one not in `required` and without a `default` may be nil |
| `additionalProperties: S` | open, other fields `S` | an undeclared field may be absent: read it with `?.` |
| `additionalProperties: false`, no properties | `{}` | no fields to read |

A required property must be present and not nil unless its schema allows
nil (`type` includes `null`, or `null` is in an allowed enum). A declared
property that is absent reads as its `default`, or as nil without one, also
inside values under `additionalProperties` and in list items. Defaults
apply where Apply validates a value it keeps: the input and step outputs.
A `default` on a required property is never used.

Refs must not form a cycle: an alias cycle (`a: b`, `b: a`) names no
schema, and a recursive schema (a tree node whose children are nodes) has
no finite expression type. Both are rejected. A schema's form with refs
inlined, which is how hosts receive it as `Command.OutputSchema`, is
limited to 10,000 nodes. `Load` resolves refs into its own copy of every
schema, including the ones a host returns from `KindSchemas`, which are
never modified, so one host schema can serve many definitions at once.

At run time Apply enforces the same contract: the input is validated at
Start (`ErrInvalidInput` to the host), params at dispatch (the step fails
with `invalid_params` and is never dispatched), step outputs on completion
(the step fails with `invalid_output`, not retried), and run outputs with a
schema when the run completes (the run fails with `invalid_output`). A
declared property that is absent reads as its default, or nil.

## Expressions

`each`, `when` and `switch` are bare
[expr](https://github.com/deepnoodle-ai/expr) expressions. In `with`, `set`
and `outputs`, a string without `${{ ... }}` is a literal, a string that is
exactly one `${{ ... }}` keeps the expression's type (a list stays a list), and
anything else renders as text, including a template with spaces around it
(`' ${{ 1 + 2 }} '` is the string `" 3 "`):

```yaml
with:
  state: pending                                               # the string "pending"
  findings: '${{ correctness.items }}'                         # a list
  title: 'Review of #${{ input.event.pull_request.number }}'   # a string
  run: 'echo ${HOME}'                                          # text: only ${{ }} is a template
  note: 'write $${{ x }} for a literal'                        # $${{ escapes the opener
```

Globals:

| Name | Where | Value |
| --- | --- | --- |
| `input` | everywhere | the run's input, with defaults applied |
| *step name* | everywhere | its output once it settled on this path, else nil; an `each` step's is the list of item outputs in item order; a caught failure's is `{error: {type, message}}` |
| `item`, `index` | `with` of an `each` step | the element and its position |
| `error` | `on_failure` | `{step, step_id, type, message}` |
| `run` | `finally` | `{status, error}`: `status` is `"completed"`, `"failed"` or `"cancelled"`; `error` is `{step, step_id, type, message}` when the run failed, else nil |

Besides expr's builtins, string, math and collection functions, expressions
have `concat(a, b, ...)`, which joins lists, and `coalesce(a, b, ...)`.
Field names may be Go keywords: `error.type`, `input.event.sender.type`.

Keep expressions as glue: references and a little shaping. Logic that is
complex, consults the outside world, or deserves a record in the run's history
belongs in a step.

## Paths

A run starts with one path, `main`. When more than one edge matches, the path
forks: it ends, and each target starts a child path named after it
(`main.correctness`). A join merges the arriving paths. When they account
for every path their deepest common ancestor forked into (each of those
paths arrived, or forked in turn and its children are accounted for), the
run continues on that ancestor, so after a fork and a join it is back on
`main`. When they are only part of that fork (two joins each take half, or a
branch ended without reaching the join), the join continues on a new path
named after it (`main.j1`). Either way the choice depends only on which paths
arrived, never on the order they arrived in. A path that matches no edge
ends. The run completes when every path has ended.

### Joins and loops

A **loop** is a set of steps that can all reach each other. Its **head** is
the step paths enter it at. A **pass** of a loop starts each time a path
enters its head; a path counts its own passes, and the passes of its
ancestors before it forked. Loops nest: a loop inside a loop body has its own
head and passes. Which steps form a loop, and where its head is, follow from
the graph alone, never from the order steps or edges are listed in.

A join fires once per pass of the loops it is in. A join in no loop fires
once.

- **Who it waits for.** A join waits for every live path that can still
  arrive in the pass it is firing for. A path in that pass holds it if it can
  reach the join without going back to the head of a loop the join is in. A
  path in an earlier pass holds it if it could still catch up. Paths
  going around a loop the join is **not** in hold it, so a join after a poll
  loop waits for the loop to finish:

  ```yaml
  - {name: s, action: x, next: [poll, other]}
  - name: poll
    action: x
    max_visits: 10
    match: first
    next: [{step: wait, when: '!poll.done'}, {step: j}]
  - {name: wait, sleep: 30s, next: poll}
  - {name: other, action: x, next: j}
  - {name: j, join: [], next: fin}      # waits for poll to finish; fin runs once
  ```

- **What it checks.** A step the join names must have settled, on a path
  that arrived, in the current pass of every loop the step shares with the
  join. In a loop `h -> [a, b] -> j: join [a, b] -> h`, the second pass
  of `j` needs `a` and `b` from the second pass. A named step before the
  loop counts in every pass, and a join after a loop sees what settled in
  any of the loop's passes.

- **Branches that go around on their own.** A fork branch may go back to
  the loop head without reaching the join. Its next pass arrives in the
  join's next firing, not this one.

Because a path only ever goes from "can still arrive" to "cannot", each
firing has the same arrivals in every completion order, and the firings of
one join happen in pass order.

Two shapes are rejected at `Load` because their passes would be ambiguous:
a join inside a loop that can be entered at more than one step (route the
other entries through one head), and a join naming a step it follows only
through the head of a loop they share (the step never settles in the
join's pass).

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
run fails with both errors (`RunError.OnFailure`) and the rest do not run.

A cancelled run does not run `on_failure`. A cancel that arrives while
`on_failure` steps run cancels them, and the run is cancelled.

`on_failure` is failure handling, like a `catch` for the whole run: it
reads `error`, and a failure in it is part of the run's failure.

## Finally

`finally` steps are cleanup, like a `finally` block: they run once the
run's outcome is decided, whatever it is, in order, and each runs even if
an earlier one failed.

```yaml
finally:
  - name: release
    action: locks.release
    with: {lock: '${{ lock?.id }}'}
  - name: notify
    action: slack.post
    with:
      text: '${{ run.status }}: ${{ coalesce(run.error?.message, "ok") }}'
      cleanup: '${{ release?.error == nil }}'
```

**When they run.** When every path has ended (after `outputs` are
evaluated), when the run has failed (after `on_failure`), or when it is
cancelled (after the steps in flight are cancelled). They take the same
step shapes as `on_failure` steps: host kinds and `set`, with `with`,
`output`, `retry` and `timeout`; not `next`, `catch`, `each`, `join` or
`sleep`. A host that ends a run without the engine skips them, as the
postgres host does when it aborts a run it cannot advance (see
[hosts](hosts.md#workers)).

**What they read.** `run` is the outcome: `run.status` is `"completed"`,
`"failed"` or `"cancelled"`, and `run.error` is the run's error when it
failed and nil otherwise (read it with `?.`). Every step of `steps` and
`on_failure` may not have run, so reading one needs `?.` or `coalesce`. An
earlier finally step has settled, but if it failed its value is `{error:
{type, message}}`, as after a catch, so read it with `?.` too
(`release?.error`, `release?.id`). `finally` steps cannot read `error`
(use `run.error`), later finally steps, or the run's outputs, which they
can compute from the same steps. Nothing outside `finally` can read a
finally step.

**Failures.** A failing finally step does not change the outcome: a
completed run stays completed. Its failure is recorded in
`State.FinallyErrors` and on the terminal command (`Command.FinallyErrors`,
each a `RunError` with the step, StepID, type and message), so hosts can
alert on cleanup that did not finish, and the next finally step runs.

**The run's status.** While finally steps run, the run is `completing`,
`failing` or `cancelling`, after its outcome (`failing` also covers
`on_failure`). `RunStatus.Outcome()` maps these to `completed`, `failed`
or `cancelled`. The terminal command (`RunCompleted`, `RunFailed` or
`RunCancelled`) comes when the last finally step is done, so a host learns
that the run finished only after its cleanup.

**Cancelling during finally.** A cancel that arrives while finally steps
run stops them: the step in flight gets `CancelStep`, the rest never run,
and the run finishes with the outcome it already had (a run that completed
still completes, with its outputs). Each finally step that did not run to
the end is in `FinallyErrors` with type `cancelled`. So the first cancel of
a running run starts its cleanup, and a second one stops it.

The finally steps' behaviour depends only on the outcome and the values
that settled, never on the order events arrived in.

## Limits

A run keeps step values in its state because expressions read them. The
host sets the caps in `Definition.Limits`:

- `MaxOutputBytes` (default 256 KiB) caps one value: a step's output or a
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
from the start step; steps without a name or sharing one; an edge, switch
case or catch with no target, and a target listed twice in one edge list,
case or default; a join naming a step that is not upstream of it in the same
loop pass; a join in a loop with more than one entry step; a cycle with no
`max_visits` or `sleep` step; modifiers on kinds that do not take them;
`on_failure` and `finally` steps with `next`, `catch`, `each`, `join` or
`sleep`; a `finally` step reading `error` or a finally step that runs after
it; and kind config errors from `Kinds.ValidateConfig`.
