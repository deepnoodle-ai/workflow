# A shared durable-host core, and a SQLite backend on it

**Status:** Draft
**Author:** Curtis Myzie
**Date:** 2026-10-28
**Workflow:** Spec, then wait for feedback, then build. The build starts
only after #50 to #56 merge, because they change the files this work moves.
**PRD:** [007-sqlite-backend.md](../prds/007-sqlite-backend.md)

## Context

The PRD asks for a SQLite backend with the same API and behaviour as the
postgres host (#53). The postgres module mixes two things in `store.go`
and `worker.go`: the policy (what to do with a claim, when to retry, how
an event becomes state, step records and jobs) and the SQL. To share the
policy, we pull it into `internal/durable` in the root module, behind a
narrow storage interface, and implement that interface twice. This spec
fixes the interface, the SQLite storage design, the test suite that runs
on both backends, and the order of three stacked PRs.

Discovery (notes, not committed) covers local with Save and Resume, River,
goqite, backlite, neoq, and the SQLite locking facts this spec relies on.

## Goals

- One copy of the worker policy and the persistence policy. Checked by
  review: `postgres/` and `sqlite/` contain no claim loop, no settle
  backoff, no `attemptOutcome`, no `storable`.
- Postgres behaviour and public API unchanged. Checked by the existing
  postgres tests (no assertion changes) and by `TestClaimUsesItsIndex`.
- One test suite runs on both backends, with `-race`, in CI.
- The SQLite backend passes that suite with two `*sql.DB` handles on one
  file, and no `SQLITE_BUSY` error escapes.
- The root module keeps only the standard library and expr.

## Non-goals

- A public storage interface. `internal/durable` is not importable outside
  this repository's modules, on purpose.
- Changing claim SQL, schema or indexes on Postgres.
- Cross-process instant wake-ups on SQLite.

## Proposal

### Shape

```text
             app code (one code path: shared types)
                 |                         |
     postgres.Store / Worker       sqlite.Store / Worker      thin wrappers:
     (pgx, LISTEN, Migrate)        (*sql.DB, Migrate,         options, Tx entry
                 |                  checks, hub)              points, docs
                 v                         v
           +-------------- internal/durable ---------------+
           | types.go    NewRun, Result, Run, StepRecord, ... |
           | store.go    Start, Deliver, persist, abort,     |
           |             definitions, storable, outcomes     |
           | worker.go   claim loop, heartbeat, settle,      |
           |             give-back, drain, observer,         |
           |             RunOnce, Execute                    |
           | backend.go  Backend, Tx (the storage contract)  |
           +-------------------------------------------------+
                 ^                         ^
     postgres/backend.go           sqlite/backend.go          the SQL
```

### The storage contract (`internal/durable/backend.go`)

Two interfaces. `Backend` is the database outside a transaction. `Tx` is
one transaction that has locked what it writes.

```go
// Lease names one claim of a job: its row and the fence it took.
type Lease struct{ JobID, Fence int64 }

// Claimed is a job a claim leased.
type Claimed struct {
    Lease
    workflow.Job          // RunID, Claims, Command
    DefHash string
    At      time.Time     // when the claim was made; the lease runs from here
}

type ClaimRequest struct {
    Owner  string
    Queues []string       // "timer", "step:<kind>"
    Limit  int
    Lease  time.Duration
    DueBy  time.Time      // zero: now. RunOnce passes its T0.
}

type Backend interface {
    // Write runs fn in a transaction and commits if fn returns nil.
    Write(ctx context.Context, fn func(Tx) error) error

    Claim(ctx context.Context, req ClaimRequest) ([]Claimed, error)
    // ClaimKey leases one job by its natural key if it is due and its
    // lease is free. It returns nil, nil when it cannot.
    ClaimKey(ctx context.Context, ref JobRef, req ClaimRequest) (*Claimed, error)
    // JobStatus says why ClaimKey could not claim: gone, leased until,
    // due at. Execute uses it on the rare path.
    JobStatus(ctx context.Context, ref JobRef) (JobStatus, error)
    Now(ctx context.Context) (time.Time, error) // the clock leases use

    // Fenced writes. Zero rows affected returns ErrLeaseLost.
    Renew(ctx context.Context, l Lease, d time.Duration) error
    Release(ctx context.Context, l Lease) error   // clear lease, claims - 1
    Postpone(ctx context.Context, l Lease, d time.Duration) error

    // Wake calls notify when jobs may be due, until ctx is done.
    Wake(ctx context.Context, notify func())
    // Unstorable reports a database error that means the value cannot be
    // stored, so the worker fails the step with invalid_output.
    Unstorable(err error) bool

    DefinitionDoc(ctx context.Context, hash string) ([]byte, error) // nil, nil: missing
    GetRun(ctx context.Context, runID string) (*Run, error)
    Steps(ctx context.Context, runID string) ([]StepRecord, error)
    Events(ctx context.Context, runID string) ([]workflow.Event, error)
    QueueStats(ctx context.Context) ([]QueueStat, error)
}

type Tx interface {
    // LockRun locks the run's row until commit. ErrRunNotFound if absent.
    LockRun(ctx context.Context, runID string) (st *workflow.State, defHash string, err error)
    DefinitionDoc(ctx context.Context, hash string) ([]byte, error)
    InsertDefinition(ctx context.Context, hash, name string, doc []byte) error
    // InsertRun returns false when a run with this ID exists.
    InsertRun(ctx context.Context, r RunRow) (bool, error)
    UpdateRun(ctx context.Context, runID string, next *workflow.State, at time.Time) (seq int64, err error)
    InsertEvent(ctx context.Context, runID string, seq int64, ev workflow.Event, stepID workflow.StepID) error
    InsertStep(ctx context.Context, runID string, c workflow.Command, at time.Time) error // skip a duplicate
    // SettleStep changes a record only while it is dispatched.
    SettleStep(ctx context.Context, runID string, id workflow.StepID, s StepStatus,
        output any, stepErr *workflow.StepError, at time.Time) error
    CancelSteps(ctx context.Context, runID string, id workflow.StepID, at time.Time) error // "" = all
    InsertJob(ctx context.Context, runID, kind string, c workflow.Command, runAt time.Time) error // zero runAt: now
    DeleteJobs(ctx context.Context, runID string, id workflow.StepID) error // "" = all
    DeleteJob(ctx context.Context, l Lease) error // fenced: ErrLeaseLost
    JobsWritten(ctx context.Context) error
    StatusChanged(ctx context.Context, runID string, status workflow.RunStatus) error
    // OnFinished calls the application's hook with the native transaction.
    OnFinished(ctx context.Context, f Finished) error
}
```

The brief's seven operations map onto this. Claim, Renew, Release and
Postpone are `Backend` methods: one statement each. Settle, Finish and
Abort are compositions in the core, so their order of locks is written
once:

| Operation | Core code |
| --- | --- |
| Settle | `Write`: `LockRun`, `DeleteJob(lease)`, `deliverLocked` |
| Finish (hand-off) | `Write`: `DeleteJob(lease)` |
| Abort | `Write`: `LockRun`, `DeleteJob(lease)`, `abort` |

Contract rules, each tested in the suite:

1. A fenced write that affects zero rows returns `ErrLeaseLost`.
2. Every claim adds 1 to `claims` and to `fence`. A fence never shrinks.
   A job ID is never reused.
3. `Claim` returns jobs with `run_at <= min(now, DueBy)` whose lease is
   free, earliest `run_at` then ID first, at most `Limit`, only from
   `Queues`. Two concurrent claims never return the same job.
4. `LockRun` serializes every writer of one run until commit.
5. `InsertStep` and `InsertJob` skip a row whose key exists.
6. A reader inside `Write` (`DefinitionDoc`) goes through the transaction,
   never the pool. With `SetMaxOpenConns(1)` the pool would deadlock.

### The shared core

**Types** (`types.go`). `NewRun`, `Result`, `Run`, `StepRecord`,
`StepStatus` and its constants, `Finished`, `EventAborted`,
`ErrorTypeDefinitionUnavailable`, the sentinels, and the types #52, #54
and #56 add (`SQLTx`, `Observer` and its event types, `QueueStat`,
`JobRef`, `Execution`, `ErrNotClaimable`). Each backend package declares
`type Result = durable.Result`, `const StepCompleted = durable.StepCompleted`,
`var ErrRunNotFound = durable.ErrRunNotFound`, and so on. Methods on these
types (`Result.Jobs()` from #56) move with them. The sentinel messages
change prefix from `postgres:` to `workflow:`.

**Store** (`store.go`). `durable.Store` holds the backend, kinds, limits,
run-ID function and the definition cache. It has `Start(ctx, tx, run)`,
`Deliver(ctx, tx, runID, ev)`, `Abort(ctx, tx, runID, st, err, at)` and
`Definition(ctx, reader, hash)`. `persist`, `finished`, `storable`,
`hasNUL`, `attemptOutcome` and `nextAttempt` move here unchanged, except
that each `tx.Exec` becomes a `Tx` method call. The order of writes in
`persist` stays as it is.

**Worker** (`worker.go`). `durable.Worker` holds the configuration fields
(`Handlers`, `ID`, `Concurrency`, `Lease`, `Poll`, `MaxClaims`, `Logger`,
`MayClaim`, `Observer`), a `Backend`, a `*Store`, and an exported `Hooks`
struct for tests (`Finish`, `Heartbeat`, `Settle`, `HandlerDone`). Every
method of `postgres.Worker` as #50, #51, #54, #55 and #56 leave it moves
here: `Run`, `Shutdown`, `Active`, `RunOnce`, `Execute`, `process`,
`unloadable`, `abort`, `heartbeat`, `settle`, `finishDeferred`,
`release`, `interrupted`, `isPermanent`, and the drain model of
worker-drain's spec (the `runs` counter, `stopped` and `drained`
channels, the `cut` context). `listen` becomes `Backend.Wake`.
`isUnstorable` becomes `Backend.Unstorable`.

### The postgres wrappers

`postgres.Store` keeps its fields (`pool`, `schema`, hooks) and gains a
`core *durable.Store`. Each public method opens the right transaction and
calls the core:

```go
func (s *Store) StartTx(ctx context.Context, tx pgx.Tx, run NewRun) (*Result, error) {
    return s.core.Start(ctx, s.pgxTx(tx), run)
}
func (s *Store) Start(ctx context.Context, run NewRun) (*Result, error) {
    var res *Result
    err := s.backend.Write(ctx, func(tx durable.Tx) (err error) {
        res, err = s.core.Start(ctx, tx, run)
        return err
    })
    return res, err
}
```

`postgres/backend.go` (new) holds `pgBackend` and `pgTx`. `pgTx` runs on
the narrow Exec/QueryRow interface #52 introduces, so the `pgx.Tx` and
`SQLTx` paths share it; its `OnFinished` calls `WithOnFinished` or
`WithOnFinishedSQL`, by the path that opened it. The SQL strings move
into these methods unchanged: `claimSQL`, the claim-by-key query, the
heartbeat, release and postpone updates, the inserts in `persist`.
`pg_notify` stays inside the transaction, so `NotifyChannel` and
`RunsChannel` fire on commit, as now.

`postgres.Worker` keeps its exported fields and adds a private
`core *durable.Worker`, built once on first use from the fields (the
`sync.Once` that #56 adds). Its methods forward. `listen` and
`ListenConfig` stay in postgres and become `pgBackend.Wake`. The private
test hooks keep their names and are copied into `Hooks`.

### The SQLite backend (`sqlite/`)

**Module.** `github.com/deepnoodle-ai/workflow/sqlite`, `go 1.27.0`,
`replace github.com/deepnoodle-ai/workflow => ../`. It requires the root
module and, for tests only, `modernc.org/sqlite`.

**Schema** (`sqlite/schema.sql`, migration 1). The same tables and columns
as Postgres, with `{{prefix}}` before each name (`workflow_` by default),
and these differences:

- `STRICT` tables, so a value of the wrong type fails at the insert
  instead of being stored in another form. Needs SQLite 3.37.
- JSON columns are `TEXT`. Times are `TEXT` in the fixed UTC form
  `2006-01-02T15:04:05.000000Z`, so text order is time order.
- `jobs.id INTEGER PRIMARY KEY AUTOINCREMENT`. Plain `INTEGER PRIMARY KEY`
  can reuse the largest deleted ID, and `(id, fence = 1)` would then
  match a new job.
- `jobs.queue` is a `STORED` generated column, as on Postgres, with the
  index `jobs_claim (queue, run_at, id)`.
- No GIN index on metadata.
- Foreign keys are declared. The store does not depend on them being
  enforced (`PRAGMA foreign_keys` is off by default).

**Locks.** SQLite has one writer. A transaction that reads first and then
writes can fail with `SQLITE_BUSY` (or `SQLITE_BUSY_SNAPSHOT` in WAL)
without calling the busy handler. So every transaction the store opens
must write first. Then it takes the write lock under the busy handler and
reads the newest data. This is the rule:

- `LockRun` is `UPDATE {{p}}runs SET event_seq = event_seq WHERE id = ?
  RETURNING state, definition_hash`. It is a write, so it takes the lock,
  and it reads the row in the same statement.
- Every `Write` callback in the core begins with `LockRun`,
  `InsertDefinition` (Start) or `DeleteJob` (Finish). All are writes.
- `sqliteTx` records whether a write has run. A read before the first
  write returns an internal error. A regression then fails every test at
  once, not one run in a thousand under load.
- `Claim`, `ClaimKey`, `Renew`, `Release` and `Postpone` are one statement
  each, in autocommit.

The rule does not cover the application's own transaction in
`StartSQLTx`. If it has read before, its first write can fail with
`SQLITE_BUSY`. The docs say to open it with `_txlock=immediate`.

**Claim.** One statement:

```sql
UPDATE {{p}}jobs
SET lease_owner = ?, lease_expires = ?, claims = claims + 1, fence = fence + 1
WHERE id IN (
    SELECT id FROM {{p}}jobs
    WHERE queue IN (?, ?, ...) AND run_at <= ?
      AND (lease_expires IS NULL OR lease_expires < ?)
    ORDER BY run_at, id
    LIMIT ?)
RETURNING id, run_id, command, claims, fence,
    (SELECT definition_hash FROM {{p}}runs r WHERE r.id = run_id)
```

SQLite allows a subquery in `RETURNING`; it reads `runs`, not the table
being changed, so its result is defined. `RETURNING` row order is
arbitrary, so the core sorts claims by `run_at`, then ID. The queue list
is built as placeholders, so the JSON functions are not needed.
`ClaimKey` is the same `UPDATE` with `WHERE run_id = ? AND step_id = ? AND
kind = ?` and the due and lease conditions. One writer at a time makes
both exclusive without `SKIP LOCKED`.

**Clock.** The process clock. `now` is `time.Now().UTC()` truncated to
microseconds. `InsertJob` with a zero `runAt` stores `now`. Lease expiry,
due times, `QueueStats` and RunOnce's T0 all use it. All processes are on
one host, so they share the clock.

**Wake-ups.** Each `sqlite.Store` has a hub: a list of buffered channels.
`Tx.JobsWritten` signals the hub at once, before commit. A worker that
wakes runs its claim, which waits on the write lock until the writer
commits, then sees the new jobs. A rollback leaves nothing to claim, which
is harmless. `Backend.Wake` subscribes to the hub until its context ends.
Other processes see new jobs at the next `Poll`.

**Checks** (`Migrate` and `Worker.Run`, once per store):

| Check | Query | Error message names |
| --- | --- | --- |
| SQLite 3.37 or newer | `SELECT sqlite_version()` | the version found |
| busy timeout set | `PRAGMA busy_timeout` > 0 | `_busy_timeout=5000` in the DSN |
| in-memory needs one connection | `PRAGMA database_list` file is empty and `db.Stats().MaxOpenConnections != 1` | `SetMaxOpenConns(1)`, or a file |

**Migrate.** No advisory lock exists. `Migrate` runs
`CREATE TABLE IF NOT EXISTS {{p}}schema_migrations` in autocommit, which
is safe to repeat. Then it opens a `Write` whose first statement is
`UPDATE {{p}}schema_migrations SET version = version WHERE version < 0`.
That takes the write lock and changes no row. It then reads the version
and applies the missing migrations, as postgres does. DDL in SQLite is
transactional.

**Unstorable.** `storable` and the NUL checks in `Start` run in the core
for both backends (PRD R-6). SQLite has no further value it refuses below
the state limits, so `Unstorable` returns false.

**Public API.** `sqlite.go` mirrors `postgres/store.go` and `worker.go`
for the names in PRD R-3, as thin wrappers like those above. There is no
`StartTx`, `DeliverTx`, `CancelTx`, `WithOnFinished`, `NotifyChannel`,
`RunsChannel` or `ListenConfig`. `WithSchema` sets the table prefix and
accepts the same names as postgres (`^[a-z_][a-z0-9_]*$`).

### One test suite for every backend (`internal/durabletest`)

A package in the root module, compiled only by the backend modules'
tests. It cannot import `postgres` or `sqlite` (the root module does not
depend on them), so it drives `durable.Store` and `durable.Worker`
through a harness:

```go
type Harness interface {
    Name() string
    // Open returns a migrated backend on a fresh database: a new schema on
    // Postgres, a new file in t.TempDir() on SQLite.
    Open(t *testing.T, kinds workflow.Kinds, opt Options) *Env
    // Another handle on the same database: a second pool or *sql.DB.
    // It stands for a second process.
    Reopen(t *testing.T, env *Env) *Env
    // Test access to rows the API does not show.
    Jobs(t *testing.T, env *Env, runID string) []JobRow
    ExpireLeases(t *testing.T, env *Env, runID string)
    // CallerTx opens an application transaction and the durable.Tx on it,
    // for the atomic tests.
    CallerTx(t *testing.T, env *Env) (durable.Tx, commit, rollback func() error)
}

func Run(t *testing.T, h Harness) // runs every test as a subtest
```

Each backend module adds `func TestSuite(t *testing.T) {
durabletest.Run(t, harness{}) }`. Postgres's harness skips without
`WORKFLOW_PG_DSN`, as the tests do today.

What moves into the suite: conformance (apply, local and the backend),
`aba`, `fence`, `race`, `shutdown`, `store` (except index and LISTEN
checks), `result`, `settle_record`, `sideeffect`, `deploy`,
`definition`, `loop`, `start`, `versions`, `specialised`, `caller`, the
commit and rollback parts of `atomic`, and the tests #50 to #56 add
(drain, once, execute, observer). New in the suite: two handles on one
database (UC-3), and a fence test across handles.

What stays in `postgres/`: `claim_test` (query plan), `listen_test`,
`migrate_test` (advisory lock), the pg error codes in `output_test`, the
notification parts of `atomic_test`, the `pgx.Tx` entry points, and the
Example tests. New in `sqlite/`: the checks table above, concurrent
`Migrate` from four handles, the read-before-write guard, the UC-1
compile test (both stores assigned to one interface), and the Example.

The public wrappers are thin. Each module tests them with the compile
test, one end-to-end run through the public API, and the Example.

### Order and PRs

Three PRs, stacked on `docs/sqlite-backend-prd`. Each builds and passes
alone.

1. `refactor(postgres): run the worker and store on a shared core`.
   `internal/durable` and the postgres wrappers. No postgres test changes
   what it asserts. A test that reaches a moved private helper
   (`attemptOutcome`, `hasNUL`, `interrupted`, `w.claim`, `s.defs`) reaches
   it at its new place. Reviewers check: `TestClaimUsesItsIndex` passes,
   and `git diff --stat postgres/*_test.go` is small.
2. `test: run the durable host suite on any backend`. `internal/durabletest`
   and the postgres harness. Tests move; the number of postgres tests that
   run does not drop.
3. `feat(sqlite): run the durable host on SQLite`. The module, its
   harness, its own tests, the Example, docs (README, hosts.md, llms.txt,
   doc.go), Makefile `test-sqlite`, and the CI job. Closes #53.

Rejected: one PR. It would mix a refactor that must change nothing with a
feature that adds a module, and a reviewer could not tell which diff
lines carry risk.

## Alternatives considered

- **Move the store to database/sql and add a SQLite flavor** (#52's option
  3). One store type, `switch` per query, as goqite does. Rejected: the
  brief keeps Postgres on pgx; `LISTEN` needs a pgx connection anyway;
  and about 25 statements with two forms each is harder to read than two
  small backend files.
- **A data plan instead of a `Tx` interface.** `persist` returns a list of
  writes (`UpdateRun`, `InsertJob`, ...) and each backend executes it.
  Easier to unit-test. Rejected: `UpdateRun` returns the event sequence
  that `InsertEvent` needs, and the `OnFinished` hook must run in the
  transaction after the writes. A plan would need placeholders for both.
  The interface keeps the code in today's order.
- **`BEGIN IMMEDIATE` on a dedicated `*sql.Conn`.** Explicit and
  well-known. Rejected: `database/sql` does not know the connection is in
  a transaction. If `COMMIT` fails on a cancelled context, the connection
  goes back to the pool with the transaction open, unless we discard it
  through `Conn.Raw`. The write-first rule needs none of this.
- **River-style `SetMaxOpenConns(1)`.** It removes in-process lock waits.
  Rejected as the rule: it does nothing for a second process (UC-3), and
  it serializes reads. It stays the advice for `:memory:`.
- **Types per package, with a conversion helper.** Rejected in the PRD:
  every application would write the adapter.

## Tradeoffs and consequences

- `internal/durable` is a contract between modules. A root release that
  changes it can break an older `postgres` or `sqlite` module. We release
  the three modules together, and the release notes say to upgrade them
  together.
- Postgres code gets one more layer: a public method calls the core,
  which calls a `Tx` method that runs the SQL. A reader looking for
  "what SQL does Deliver run" now opens two files.
- Type aliases make `%T` print `durable.Result` for a `postgres.Result`.
  The sentinel messages change prefix. These are the only visible postgres
  changes.
- The write-first rule is a convention. The read guard in `sqliteTx`
  makes a break fail loudly, but only on SQLite.
- SQLite serializes all writes. Heartbeats from `Concurrency` jobs, claims
  and settles all queue on one lock. Fine for development; we claim
  nothing about throughput.
- A job written in another process waits up to `Poll` (default 1s).

## Failure modes

| Failure | Effect | Detection |
| --- | --- | --- |
| A write transaction reads first (regression) | `SQLITE_BUSY` under contention | read guard fails every test |
| `busy_timeout` missing | any overlap of writers fails | `Migrate`/`Run` refuse to start |
| App holds its own transaction longer than `busy_timeout` | worker claims and heartbeats get `SQLITE_BUSY`; a heartbeat failing for a whole lease cancels the handler | logged, as on Postgres when the database is unreachable |
| Clock jumps back on the host | leases last longer; due jobs wait | none; the same as Postgres with a skewed server clock |
| Database on a network file system | WAL locking breaks; possible corruption | not detectable; the docs forbid it |
| Old `postgres` module with new root module | build fails in `internal/durable` | lockstep releases |

## Rollout

No migration on Postgres; no change in behaviour. The `sqlite` module is
new, so it has no users to migrate. PR 1 can merge alone if PR 3 slips.
The README's packages table and `hosts.md` gain the `sqlite` entries only
in PR 3.

## Open questions

- **The exact shapes from #50 to #56.** `JobStatus`, the `Observer`
  events, and the drain model are written here as their PRDs and specs
  describe them on 2026-10-28. When they merge, PR 1 moves whatever
  merged, and this spec is updated if a shape changes the `Backend`
  interface. Not blocking: the build waits for them.
- **Whether `jobs_claim` serves the SQLite claim well with several
  queues.** SQLite may scan each queue's range and sort, rather than stop
  at the limit. Harmless at development scale. PR 3 checks
  `EXPLAIN QUERY PLAN` in a test, and splits the claim per queue if the
  plan scans the table.
