# workflow

A deterministic progression engine for step graphs. The whole engine is
`Apply(def, state, event) -> (state, commands, error)`; hosts do the I/O:
`local/` (in-process), `postgres/` and `sqlite/` (durable, each its own
module). `llms.txt` is the API reference, `documentation/definitions.md` the
language, and `documentation/hosts.md` the host contract, the only full
statement of it: keep README, doc.go and llms.txt in line. `planning/` is
design history; where it disagrees, the code wins. Docs and examples never
name an application or company that uses the library; they may name the
platforms it runs on, such as Cloud Run, Cloud Tasks and Postgres. A run's
graph never changes: generated work runs as a child run.

## Invariants of the root package

- No I/O, goroutines, clocks or randomness. `Event.At` is the only time.
- Sort every map iteration that can reach output or state.
- Input, outputs and set values are JSON-normalized on the way in, so a
  reloaded state behaves like the one in memory. `fakeHost` round-trips
  state through JSON after every Apply and checks Apply did not mutate it.
- Apply returns an error only for a host mistake (sentinels, `errors.Is`).
  A failed run is a `RunFailed` command carrying a `RunError`.
- Load parses with encoding/json/v2, so duplicate keys fail. Hashes and
  state use v1, which sorts map keys. Keep it that way.

## How progression works

- A `StepID` (`main/verify@2[3]#1`) names one attempt. `StepID.Unit()`
  drops `#attempt` and is the idempotency key for side effects. A path ID
  with `~N` (`main.tests~2`) is a re-forked path. The parent's `Seq`
  numbers it, never a probe of the records, because `prune` drops ended
  path records and an ID must never come back.
- One Apply drains a FIFO of transitions, then fires joins, then checks
  completion, and repeats until nothing changes.
- Only successes are order-independent: completions in flight together
  reach the same final state and dispatches in any order. A failure can
  end the run before a sibling's success dispatches anything.
- Loops come from the graph alone (`computeLoops`). A join fires once per
  pass, in cohorts keyed by pass. `canStillArrive` only goes from true to
  false; that is what makes arrival order irrelevant. Don't weaken it.
- A join's names are an assertion, not a wait list. Load rejects a name a
  path can skip (joincheck.go). It judges each `when` alone (`x` and `!x`
  may both skip) and ignores back edges, so a join in a loop can still
  fail at run time. Tests reach that failure with `mustLoadLenient`.
- Lifecycle: `on_failure` steps run on path `failure`, then `finally`
  steps on path `finally` over the union of scopes. Finally errors go to
  `State.FinallyErrors`, never the outcome. A second cancel is a no-op;
  only `Event.Force` stops finally. `Abort` fails a run without its
  definition and runs neither.

## Types: static and runtime must agree

- `typecheck.go` types every expression with expr's `Program.Check`.
  `computeFacts` decides which step reads may be nil. Runtime matches it:
  a step name is nil in the env until settled, and `Schema.complete` sets
  an absent property to its default, else nil (so a property with a
  default is typed non-nil). Change both sides together.
- Step names are identifiers that don't shadow expression globals or
  functions (`input`, `split`, ...). The first step is the start, and
  every cycle needs `max_visits` or `sleep`.

## Tests and the postgres module

- `make test` (root module), `make test-pg` (needs `WORKFLOW_PG_DSN`),
  `make test-all` (both, plus vet); CI adds `-race` outside the engine
  package. Use `internal/require`, not testify. `property_test.go` (order
  independence) and `soundness_fuzz_test.go` (Load's type promise) must
  stay green. Default tests stay fast: they run a small sample of random
  graphs, and `make test-long` (`WORKFLOW_LONG=1`) runs them at full size
  after an engine change. Suite tests in `internal/hosttest` run one at
  a time: many depend on lease timing, which parallel runs break.
- `postgres/` has `replace ../`. go.work is gitignored; in a worktree run
  with `GOWORK=off`. Change its schema by appending to `migrations`
  (store.go); schema.sql is migration 1, editable only until released.
- `sqlite/` is the same host on SQLite, its own module with `replace ../`;
  `make test-sqlite` (modernc) and `make test-sqlite-mattn` (cgo). Both
  backends implement `internal/hostcore`'s storage contract and share its
  policy. `internal/hosttest` is the suite every backend runs: a new
  behaviour test goes there and into `list.go` (a test fails otherwise).
  `internal/parity` is a module that checks both compile against one
  interface.

## Code and examples

- Struct literals: one line if under 80 characters, else one field per
  line. One statement per line; at most 100 columns unless a string runs
  over. A blank line ends each phase of a function, not each block.
- Examples: one or two concepts in about 60 to 120 lines, JSON in files,
  no new dependencies. Don't tag or release without the human approval.
