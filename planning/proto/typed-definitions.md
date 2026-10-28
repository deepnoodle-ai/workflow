# Typed definitions: expressions, routing and data

_2026-10-28. Follows `flow-progression-findings.md`. Branch
`proto/flow-progression`, with expr v1.3.0, which shipped the checker._

## The problem

Expressions did two jobs with one untyped mechanism. Routing ("the
classifier said bug_fix; go there") and data passing ("give the reviewer this
finding") were both strings of expr, checked only for syntax and for names.
A misspelled field (`classify.knd`), a value outside an enum
(`kind == "bugfix"`), a `with:` that did not fit a tool, or a read of a step
that only ran on another branch all failed at run time, usually hours into a
run, after an agent turn had been paid for.

Definitions must stay data: assistants write them, people edit them, a UI
draws them, and a run resumes months later on another build. That rules out
Go callbacks and keeps expressions. The change is to give them types and to
take routing away from them where a structure says it better.

## Decisions

1. **Types come from schemas.** A step's `output:` is a JSON Schema subset
   (inline, a basic type, or a name under `schemas:`), matching the
   proposal's `output: findings`. Hosts supply what they know through the
   optional `KindSchemas` interface: a tool's input and output schemas, an
   agent's structured output. Inputs and `event_schema` type the rest. `set`
   steps are inferred. Untyped steps stay unchecked, so typing is
   incremental.
2. **The checker lives in expr.** `Program.Check(env)` walks the same AST
   the evaluator runs (after the `?.`, JSON-literal, pipe and keyword
   rewrites), so the checker and runtime cannot disagree about syntax. It is
   useful to every expr host, not only workflows.
3. **Nil is tracked, and the graph decides it.** A dataflow pass over the
   forward graph computes what has settled before each step, as clauses
   ("at least one of these settled"). Steps before a fork, steps a named join
   waits for, and steps every arrival saw are certain; anything else reads
   as maybe-nil and needs `?.`. Clauses make converging alternatives work:
   after `report` or `approve`, `coalesce(report, approve)` is known non-nil.
   This replaces the old run-time "undefined identifier" and `try()` pattern.
4. **`switch` for routing on a value, `when` for conditions.** `next:
   {switch, cases, default}` is checked against the value's enum: unknown
   cases and unhandled values are errors, and a value without a fixed set
   needs a default. A graph view can label its edges. `when` must be a
   boolean, statically and at run time, so `when: verify` (truthiness of a
   list) is gone.
5. **Runtime enforces the same contract.** Inputs and trigger are validated
   at Start, params before dispatch (`invalid_params`; the host never runs a
   bad call), outputs on completion (`invalid_output`, not retried). Absent
   declared properties are filled with nil, and every step name is in the
   environment as nil until it settles, so runtime behavior matches the
   types exactly.
6. **Keyword field names.** `error.type`, `event.sender.type` now parse.

## Template syntax

Templates are `${{ expr }}`, as in GitHub Actions and Mobius routines; `${...}`
is plain text, and `$${{` writes a literal `${{`. There is no delimiter
option. A definition is stored by hash and resumed months later, so its
meaning must not depend on host configuration, and two dialects would be one
more rule for authors to learn. `${{ }}` also leaves shell `${HOME}` and
JavaScript template literals in `with:` values alone.

## Review

An independent review set out to break the promise (a validated definition
whose step outputs match their schemas must never fail on an expression) and
found ten counterexamples. All are fixed, and each is a case in
`soundness_test.go`:

- `&&` and `||` return operands, not booleans; they now type as the join of
  their operands, and joins of different kinds are unions that operations
  needing one kind reject.
- The settled analysis assumed single-entry loops. It is now a fixpoint over
  every edge, which is also simpler.
- `coalesce` treated "settled" as "not nil"; it now counts only steps whose
  value cannot be nil.
- Builtins that fail on nil now require non-nil arguments; `try`/`if` arity
  matches the runtime; `==` on lists and objects is rejected; fields allowed
  by `additionalProperties` are maybe-absent; enums with `null` are
  nullable; input defaults are validated and completed; `outputs` and
  `on_failure` see a caught step's `{error}` value.

The same review found a false positive (`a.n != nil && a.n > 3`), now
accepted through nil narrowing.

## Trade-offs

- **Closed objects by default.** Expressions can read only declared
  properties unless `additionalProperties` says otherwise. That is what
  catches typos, at the cost of declaring what you read. At run time extra
  properties are still accepted.
- **Maybe-nil is conservative.** Loops are analyzed on their first pass, and
  an unnamed join guarantees only what every arrival saw. `outputs` are
  checked against what every completing run must have settled (a fixpoint
  over the ways a run can avoid each step) together with what held at every
  step a path can end on, so a start step or both sides of an
  unconditional fork read as certain there. Authors write `?.`
  or `coalesce` in a few places they could argue are safe.
- **Narrowing is syntactic.** `x != nil && x.a`, `x == nil || x.a` and
  `if(x != nil, x.a, b)` narrow `x`; a check through a helper or a `set`
  step does not.
- **Invalid output is not retried.** Retrying a mutating action because its
  result did not parse would repeat the mutation. A host that wants retries
  for agents validates the output itself and reports a retryable failure.

## Not done

- **A generative soundness test.** `soundness_test.go` is a list of known
  counterexamples. A generator of random typed definitions and conforming
  outputs, asserting that validated runs never fail on an expression, would
  hold the promise the way the shuffle test holds order independence.
- **Dataflow-inferred joins.** The settled analysis knows what each step
  reads, so a join could be implied by data dependencies (`verify` reads all
  three reviewers). That changes the model, so it is not done yet; today the
  analysis only reports maybe-nil reads.
- **Typing `event` from the trigger declaration.** Mobius's trigger
  (`{event: github.pull_request}`) could supply `event_schema` itself.
- **Schema `$defs`, `oneOf`, formats.** Not needed by the examples; `oneOf`
  would map naturally to expr's Join.
- **Per-function signatures for host functions.** `WithFuncType` exists in
  expr; the library does not yet let hosts register typed functions.
