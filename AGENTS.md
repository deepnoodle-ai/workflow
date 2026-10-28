# workflow

A deterministic progression engine for step graphs. The whole engine is
`Apply(def, state, event) -> (state, commands, error)`; hosts do the I/O.
`llms.txt` is the API reference, `documentation/definitions.md` the language,
and `documentation/hosts.md` the host contract. hosts.md is the only full
statement of that contract: keep README, doc.go and llms.txt consistent
with it. The library is general-purpose; docs and examples name no product.

## Invariants of the root package

- No I/O, goroutines, clocks or randomness. `Event.At` is the only time.
- Sort every map iteration that can reach output or state.
- Outputs are JSON-normalized on the way in, so a state reloaded from JSON
  behaves exactly like the one in memory. `fakeHost` (host_test.go) checks
  this on every event, and that Apply never mutates its input.
- Apply returns an error only for a host mistake (sentinels, `errors.Is`).
  A failed run is a `RunFailed` command carrying a `RunError`.
- Load parses with encoding/json/v2, so duplicate keys fail. Hashes and
  state use v1, which sorts map keys. Keep it that way.

## How progression works

- A `StepID` (`main/verify@2[3]#1`) names one attempt.
  `StepID.Unit()` drops `#attempt` and is the idempotency key for side
  effects. A path ID with `~N` (`main.tests~2`) is a re-forked path.
- One Apply drains a FIFO of transitions, then fires joins, then checks
  completion, and repeats until nothing changes.
- Only successes are order-independent: completions in flight together
  give the same state in any order. A failure can end the run before a
  sibling's success dispatches anything.
- Loops come from the graph alone (`computeLoops`). A join fires once per
  pass, in cohorts keyed by pass. `canStillArrive` only goes from true to
  false; that is what makes arrival order irrelevant. Don't weaken it.
- A join's names are an assertion, not a wait list. Load rejects a name a
  path can skip (joincheck.go). Each `when` is judged alone, so branches
  on `x` and `!x` may both count as skippable. Tests reach the run-time
  join failure with `lenientJoins` (`mustLoadLenient`).
- Lifecycle: `on_failure` steps run on path `failure`, then `finally`
  steps on path `finally` over the union of scopes. Finally errors go to
  `State.FinallyErrors`, never the outcome. A second cancel is a no-op;
  only `Event.Force` stops finally. `Abort` fails a run without its
  definition and runs neither.

## Types: static and runtime must agree

- `typecheck.go` types every expression with expr's `Program.Check`.
  `computeFacts` decides which step reads may be nil. Runtime matches it:
  every step name is nil in the env until settled, and `Schema.complete`
  fills absent properties with nil. A property with a default is typed
  non-nil. Change both sides together.
- Step names are expression globals: identifiers that don't shadow
  globals or functions (`split`, `first`, `count`, ...).
- The first step is the start. Every cycle needs `max_visits` or `sleep`.

## Tests

- `make test` (root), `make test-pg` (needs `WORKFLOW_PG_DSN`),
  `make test-all` (both, plus vet). Use `internal/require`, not testify.
- `property_test.go` (order independence over random graphs) and
  `soundness_fuzz_test.go` (Load's type promise) must stay green.
- `postgres/` is its own module with `replace ../`. go.work is
  gitignored; in a worktree run with `GOWORK=off`.

## Code and examples

- Struct literals go on one line only when under 80 characters; otherwise
  one field per line. One statement per line. Lines at most 100 columns.
  Blank lines only between the phases of a long function.
- Examples show one or two concepts in about 60 to 120 lines. They must
  not add dependencies.
- Don't tag or release without the human approval.
