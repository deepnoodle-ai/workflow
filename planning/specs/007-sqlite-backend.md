# A shared durable-host core, and a SQLite backend on it

**Status:** Accepted
**Author:** Curtis Myzie
**Date:** 2026-10-28
**Workflow:** Spec, then wait for feedback, then build. The build starts
only after #50 to #56 merge, because they change the files this work moves.
**PRD:** [007-sqlite-backend.md](../prds/007-sqlite-backend.md)
**Builds on:** [003-worker-drain.md](003-worker-drain.md),
[006-run-once-execute.md](006-run-once-execute.md), PRD 004 and PRD 005.

## Context

The PRD asks for a SQLite backend with the same API and behaviour as the
postgres host (#53), and one finish hook on both backends (#58). The
postgres module mixes two things in `store.go` and `worker.go`: the policy
(what to do with a claim, when to retry, how an event becomes state, step
records and jobs) and the SQL. To share the policy, we pull it into
`internal/hostcore` behind a narrow storage contract, and implement that
contract twice. The types users see move to a public package, `durable`.
This spec fixes the contract, the SQLite storage design, the finish-hook
path, the test suite that runs on both backends, and the order of three
stacked PRs.

Discovery (notes, not committed) covers local with Save and Resume, River,
goqite, backlite, neoq, and the SQLite locking facts this spec relies on.

## Goals

- One copy of the worker policy and the persistence policy. Checked by
  review: `postgres/` and `sqlite/` contain no claim loop, no settle
  backoff, no `attemptOutcome`, no `storable`, no call to a finish hook
  outside `Tx.OnFinished`.
- Postgres behaviour and public API unchanged except for the list in PRD
  UC-7. Checked by the existing postgres tests (no assertion changes) and
  by `TestClaimUsesItsIndex`.
- One test suite runs on Postgres, on SQLite with modernc and on SQLite
  with mattn, with `-race`, in CI.
- The SQLite backend passes that suite with two `*sql.DB` handles on one
  file, and no `SQLITE_BUSY` error escapes.
- The root module keeps only the standard library and expr.

## Non-goals

- A public storage contract. `internal/hostcore` is not importable outside
  this repository's modules, on purpose.
- Changing claim SQL, schema or indexes on Postgres.
- Cross-process instant wake-ups on SQLite.

## Proposal

### Shape

```text
                     app code (one code path)
                                |
                       package durable           public: NewRun, Result, Run,
                     (types, sentinels only)     StepRecord, Finished, SQLTx,
                                |                JobRef, Execution, Observer,
               +----------------+----------------+  QueueStat, Err*
               |                                 |
     postgres.Store / Worker            sqlite.Store / Worker      thin wrappers:
     (pgx, LISTEN, Migrate,             (*sql.DB, Migrate,         options, entry
      stdlib for the SQL hook)           checks, hub)              points, aliases
               |                                 |
               v                                 v
          +------------------- internal/hostcore --------------------+
          | store.go    Start, Deliver, persist, abort, definitions, |
          |             storable, outcomes, the finish-hook call     |
          | worker.go   claim loop, heartbeat, settle, give-back,    |
          |             shutdown gate, observer, RunOnce, Execute    |
          | backend.go  Backend, Tx: the storage contract            |
          +----------------------------------------------------------+
               ^                                 ^
     postgres/backend.go                 sqlite/backend.go          the SQL
```

`durable` imports the standard library and the root `workflow` package.
`internal/hostcore` imports those and `durable`. The backend modules import
all three.

### Package `durable` (public)

It holds the types, constants and sentinels in PRD R-2, as #52, #54 and
#56 define them when they merge: for example `QueueStat{Kind
workflow.CommandKind, StepKind string, Due int, OldestDue time.Duration}`,
`Observer{Claimed, Settled, Cancelled, LeaseLost, GivenBack, Abandoned,
ClaimError}`, `Settlement{Outcome, Duration, Error}`, and `JobRef` as the
natural key (run ID, StepID, timer) with `StepKind` and `RunAt`. Methods on
these types (`Result.Jobs()`) move with them. The package doc says: "You
need this package only for code that runs on either backend. Each backend
re-exports these names."

Each backend has an `aliases.go` with `type Result = durable.Result`,
`const StepCompleted = durable.StepCompleted`,
`var ErrRunNotFound = durable.ErrRunNotFound`, and so on, one line each.
The sentinel messages use the prefix `workflow:`.

### The storage contract (`internal/hostcore/backend.go`)

Two interfaces. `Backend` is the database outside a transaction. `Tx` is
one transaction that has locked what it writes.

```go
// Lease names one claim of a job: its row and the fence it took.
type Lease struct{ JobID, Fence int64 }

var (
    ErrLeaseLost = errors.New("hostcore: the job is held by a newer claim")
    ErrJobGone   = errors.New("hostcore: the job no longer exists")
)

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
    ClaimKey(ctx context.Context, ref durable.JobRef, req ClaimRequest) (*Claimed, error)
    // JobStatus says why ClaimKey could not claim: gone, leased until,
    // due at. Execute uses it on the rare path.
    JobStatus(ctx context.Context, ref durable.JobRef) (JobStatus, error)
    Now(ctx context.Context) (time.Time, error) // the clock leases use

    // Fenced writes. See rule 1.
    Renew(ctx context.Context, l Lease, d time.Duration) error
    Release(ctx context.Context, l Lease, refund bool) error // refund: claims - 1
    Postpone(ctx context.Context, l Lease, d time.Duration) error

    // Wake calls notify when jobs may be due, until ctx is done.
    Wake(ctx context.Context, notify func())
    // Unstorable reports a database error that means the value cannot be
    // stored, so the worker fails the step with invalid_output.
    Unstorable(err error) bool

    DefinitionDoc(ctx context.Context, hash string) ([]byte, error) // nil, nil: missing
    GetRun(ctx context.Context, runID string) (*durable.Run, error)
    Steps(ctx context.Context, runID string) ([]durable.StepRecord, error)
    Events(ctx context.Context, runID string) ([]workflow.Event, error)
    QueueStats(ctx context.Context) ([]durable.QueueStat, error)
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
    SettleStep(ctx context.Context, runID string, id workflow.StepID, s durable.StepStatus,
        output any, stepErr *workflow.StepError, at time.Time) error
    CancelSteps(ctx context.Context, runID string, id workflow.StepID, at time.Time) error // "" = all
    InsertJob(ctx context.Context, runID, kind string, c workflow.Command, runAt time.Time) error // zero runAt: now
    DeleteJobs(ctx context.Context, runID string, id workflow.StepID) error // "" = all
    DeleteJob(ctx context.Context, l Lease) error // fenced: see rule 1
    JobsWritten(ctx context.Context) error
    StatusChanged(ctx context.Context, runID string, status workflow.RunStatus) error
    // OnFinished calls the application's finish hook, if any, with the
    // transaction this Tx runs on.
    OnFinished(ctx context.Context, f durable.Finished) error
}
```

The brief's seven operations map onto this. Claim, Renew, Release and
Postpone are `Backend` methods: one atomic step each. Settle, Finish and
Abort are compositions in the core, so their order of locks is written
once:

| Operation | Core code |
| --- | --- |
| Settle | `Write`: `LockRun`, `DeleteJob(lease)`, `deliverLocked` |
| Finish (hand-off) | `Write`: `DeleteJob(lease)` |
| Abort | `Write`: `LockRun`, `DeleteJob(lease)`, `abort` |

Contract rules, each tested in the suite:

1. **A fenced write that changes no row returns `ErrJobGone` when no row
   has the job ID, and `ErrLeaseLost` when the row has another fence.**
   The existence check is atomic with the write. Postgres does both in one
   statement: `WITH w AS (UPDATE ... WHERE id = $1 AND fence = $2
   RETURNING 1) SELECT EXISTS (SELECT 1 FROM w), EXISTS (SELECT 1 FROM jobs
   WHERE id = $1)`, which reads one snapshot. SQLite runs the write and
   the check in one `Write`, under the write lock. Inside a settle, the
   check runs in the settle's transaction, which holds the run lock (PRD
   005, R-4). The core maps `ErrJobGone` to `Observer.Cancelled` and
   `ErrLeaseLost` to `Observer.LeaseLost`.
2. Every claim adds 1 to `claims` and to `fence`. A fence never shrinks. A
   job ID is never reused, so the existence check in rule 1 always means
   the same job.
3. `Claim` returns jobs with `run_at <= min(now, DueBy)` whose lease is
   free, earliest `run_at` then ID first, at most `Limit`, only from
   `Queues`. Two concurrent claims never return the same job. `ClaimKey`
   is exclusive with `Claim` and with itself.
4. `LockRun` serializes every writer of one run until commit.
5. `InsertStep` and `InsertJob` skip a row whose key exists.
6. A reader inside `Write` (`DefinitionDoc`) goes through the transaction,
   never the pool. With `SetMaxOpenConns(1)` the pool would deadlock.
7. `Release` with `refund` true subtracts 1 from `claims`; with `refund`
   false it only clears the lease (PRD 006's counted give-back).

### The shared core (`internal/hostcore`)

**Store** (`store.go`). `hostcore.Store` holds the backend, kinds, limits,
run-ID function and the definition cache. It has `Start(ctx, tx, run)`,
`Deliver(ctx, tx, runID, ev)`, `Abort(ctx, tx, runID, st, err, at)` and
`Definition(ctx, reader, hash)`. `persist`, `finished`, `storable`,
`hasNUL`, `attemptOutcome` and `nextAttempt` move here unchanged, except
that each `tx.Exec` becomes a `Tx` method call. The order of writes in
`persist` stays as it is. `finished` calls `Tx.OnFinished`; no other code
calls a finish hook.

**Worker** (`worker.go`). `hostcore.Worker` holds the configuration fields
(`Handlers`, `ID`, `Concurrency`, `Lease`, `Poll`, `MaxClaims`, `Logger`,
`MayClaim`, `Observer`), a `Backend`, a `*Store`, and an exported `Hooks`
struct for tests (`Finish`, `Heartbeat`, `Settle`, `HandlerDone`). Every
method of `postgres.Worker` as #50, #51, #54, #55 and #56 leave it moves
here, with its lifecycle as specs 003 and 006 define it: `init`, the
`runs` counter, `stopped` and `drained`, `cut`, `mayClaim`, `admit`,
`settled`, `hctx`, `process` with its `refund` callback and its outcome,
`Run`, `Shutdown`, `Active`, `RunOnce`, `Execute`, `unloadable`, `abort`,
`heartbeat`, `settle`, `finishDeferred`, `release`, `interrupted` and
`isPermanent`. The observer calls stay where PRD 005 puts them. `listen`
becomes `Backend.Wake`. `isUnstorable` becomes `Backend.Unstorable`.

### The postgres wrappers

`postgres.Store` keeps its fields (`pool`, `schema`, hooks) and gains a
`core *hostcore.Store`. Each public method opens the right transaction and
calls the core:

```go
func (s *Store) StartTx(ctx context.Context, tx pgx.Tx, run NewRun) (*Result, error) {
    if err := s.allowPgxTx(); err != nil { // PR 3: refuse when only WithOnFinishedSQL is set
        return nil, err
    }
    return s.core.Start(ctx, s.txOn(pgxConn(tx)), run)
}
func (s *Store) Start(ctx context.Context, run NewRun) (*Result, error) {
    var res *Result
    err := s.backend.Write(ctx, func(tx hostcore.Tx) (err error) {
        res, err = s.core.Start(ctx, tx, run)
        return err
    })
    return res, err
}
```

`postgres/backend.go` (new) holds `pgBackend` and `pgTx`. `pgTx` runs on
the internal `conn` interface that #52 adds in `postgres/conn.go`, so the
`pgx.Tx` path and the `SQLTx` path (`sqlConn`) share every statement. The
SQL strings move into these methods unchanged: `claimSQL` (with #55's
cutoff), the claim-by-key query, the heartbeat, release and postpone
updates (now with rule 1's existence check), the inserts in `persist`.
`pg_notify` stays inside the transaction, so `NotifyChannel` and
`RunsChannel` fire on commit, as now.

`postgres.Worker` keeps its exported fields and builds a private
`core *hostcore.Worker` in `init` (spec 003). Its methods forward.
`listen` and `ListenConfig` stay in postgres and become `pgBackend.Wake`.
The private test hooks keep their names and are copied into `Hooks`.

### One finish hook (#58, PR 3)

`pgTx.OnFinished` calls the hook that fits the transaction it runs on:

| The transaction is | Hook called |
| --- | --- |
| the caller's `SQLTx` | `WithOnFinishedSQL` with the caller's value (PRD 004, R-4) |
| the caller's `pgx.Tx` | `WithOnFinished` |
| one the store opened, and the store has `WithOnFinished` | `WithOnFinished` with a `pgx.Tx` (as today) |
| one the store opened, and the store has only `WithOnFinishedSQL` | `WithOnFinishedSQL` with the `*sql.Tx` the store opened |

For the last row, `New` builds `s.sqlDB = stdlib.OpenDBFromPool(pool)`
once, and `pgBackend.Write` opens its transactions with
`s.sqlDB.BeginTx` and runs `pgTx` on `sqlConn`. That covers the worker's
settles and aborts, and `Start`, `Deliver` and `Cancel`. The claim,
heartbeat, release and postpone statements stay on the pgx pool: they
finish no run. `stdlib` is part of the pgx module, so the postgres module
gains no dependency. `New` stops rejecting `WithOnFinishedSQL` alone.
`StartTx`, `DeliverTx` and `CancelTx` on such a store return an error that
names `WithOnFinished`, before any statement (the mirror of PRD 004's
rule).

`sqliteTx.OnFinished` always calls `WithOnFinishedSQL`: with the caller's
value on the `SQLTx` path, and with the store's own `*sql.Tx` otherwise.

So the hook call is in one place (`hostcore.Store.finished` through
`Tx.OnFinished`), and with `WithOnFinishedSQL` both backends run the same
hook on every path.

### The SQLite backend (`sqlite/`)

**Module.** `github.com/deepnoodle-ai/workflow/sqlite`, `go 1.27.0`,
`replace github.com/deepnoodle-ai/workflow => ../`. It requires the root
module and, for tests only, `modernc.org/sqlite` and
`github.com/mattn/go-sqlite3`. The mattn harness is in a test file with
the build tag `sqlite_mattn`, so a default `go test` needs no cgo.

**Schema** (`sqlite/schema.sql`, migration 1). The same tables and columns
as Postgres, with `{{prefix}}` before each name (`workflow_` by default),
and these differences:

- `STRICT` tables, so a value of the wrong type fails at the insert
  instead of being stored in another form. Needs SQLite 3.37.
- JSON columns are `TEXT`. Times are `TEXT` in the fixed UTC form
  `2006-01-02T15:04:05.000000Z`, so text order is time order. One helper
  pair, `fmtTime` and `parseTime`, formats every time the store binds and
  parses every time it reads. No `time.Time` goes to a query directly: a
  driver writes its own layout (mattn writes `2026-10-29 02:17:53+00:00`),
  and a space sorts before `T`, so a bound in that layout is less than
  every stored time of the same day. A due job would then wait for the next
  UTC day, and an expired lease would not be reclaimed. Every statement
  goes through one exec/query helper, and it returns an internal error for
  a `time.Time` or `*time.Time` argument, so a missed call fails every test
  at once, as the read guard does.
- `jobs.id INTEGER PRIMARY KEY AUTOINCREMENT`. Plain `INTEGER PRIMARY KEY`
  can reuse the largest deleted ID; then `(id, fence = 1)` could match a
  new job, and rule 1's existence check would lie.
- `jobs.queue` is a `STORED` generated column, as on Postgres, with the
  index `jobs_claim (queue, run_at, id)`.
- No GIN index on metadata.
- Foreign keys are declared. The store does not depend on them being
  enforced (`PRAGMA foreign_keys` is off by default).

**Locks.** SQLite has one writer for the whole database. A transaction
that reads first and then writes can fail with `SQLITE_BUSY` (or
`SQLITE_BUSY_SNAPSHOT` in WAL) without calling the busy handler. So every
transaction the store opens must write first. Then it takes the write lock
under the busy handler and reads the newest data. This is the rule:

- `LockRun` is `UPDATE {{p}}runs SET event_seq = event_seq WHERE id = ?
  RETURNING state, definition_hash`. It is a write, so it takes the lock,
  and it reads the row in the same statement.
- Every `Write` callback in the core begins with `LockRun`,
  `InsertDefinition` (Start) or `DeleteJob` (Finish). All are writes.
- The fenced writes with rule 1's check (`Renew`, `Release`, `Postpone`)
  run as one `Write`: the `UPDATE` first, then the existence `SELECT`.
- `sqliteTx` records whether a write has run. A read before the first
  write returns an internal error. A regression then fails every test at
  once, not one run in a thousand under load.
- `Claim` and `ClaimKey` are one statement each, in autocommit.

The rule does not cover the application's own transaction in
`StartSQLTx`. If it has read before, its first write can fail with
`SQLITE_BUSY`. The docs say to open it with `_txlock=immediate`.

**Ending a lock wait.** Measured on 2026-10-28, with one connection
holding the write lock and another writing with `busy_timeout` 5000 and a
300 ms context:

| Driver | Result |
| --- | --- |
| modernc.org/sqlite v1.61.0 (SQLite 3.54.1) | `context deadline exceeded` after 300 ms, for an autocommit `UPDATE` and for `BeginTx` with `_txlock=immediate`. The connection works afterwards. |
| mattn/go-sqlite3 v1.14.52 (SQLite 3.53.4) | `database is locked` after 5.37 s, for both. The context does not end the wait. The connection works afterwards. |

So with modernc, the 1 s bound on each write after a `Shutdown` deadline
(spec 003) holds. With mattn, each such write waits up to `busy_timeout`.
The docs say: with mattn, set `_busy_timeout=1000` or less if you rely on
`Shutdown` deadlines, or use modernc. The store does not work around it:
a Go-side retry loop would need `busy_timeout` 0 on the pool, which also
removes it from the application's own writes.

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
is built as placeholders, so the JSON functions are not needed. The due
bound is `min(now, DueBy)`, computed in Go. Both time bounds go through
`fmtTime`. `ClaimKey` is the same
`UPDATE` with `WHERE run_id = ? AND step_id = ? AND kind = ?` and the due
and lease conditions. One writer at a time makes both exclusive without
`SKIP LOCKED`.

**Clock.** The process clock. `now` is `time.Now().UTC()` truncated to
microseconds. `InsertJob` with a zero `runAt` stores `now`. Lease expiry,
due times, `QueueStats.OldestDue`, `Backend.Now` (RunOnce's T0) all use
it. All processes are on one host, so they share the clock.

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
for the names in PRD R-4, as thin wrappers like those above, plus
`aliases.go`. There is no `StartTx`, `DeliverTx`, `CancelTx`,
`WithOnFinished`, `NotifyChannel`, `RunsChannel` or `ListenConfig`.
`WithSchema` sets the table prefix and accepts the same names as postgres
(`^[a-z_][a-z0-9_]*$`).

### One test suite for every backend (`internal/hosttest`)

A package in the root module, compiled only by the backend modules'
tests. It cannot import `postgres` or `sqlite` (the root module does not
depend on them), so it drives `hostcore.Store` and `hostcore.Worker`
through a harness:

```go
// Caps are what a backend declares where it differs (PRD differences table).
type Caps struct {
    // RowLocks: a run lock blocks only that run (Postgres). False: it
    // blocks every write to the database (SQLite).
    RowLocks bool
    // CancelEndsLockWait: a cancelled context ends a wait for a lock.
    // False for mattn/go-sqlite3, where a write waits up to BusyTimeout.
    CancelEndsLockWait bool
    BusyTimeout        time.Duration
}

type Harness interface {
    Name() string
    Caps() Caps
    // Open returns a migrated backend on a fresh database: a new schema on
    // Postgres, a new file in t.TempDir() on SQLite.
    Open(t *testing.T, kinds workflow.Kinds, opt Options) *Env
    // Reopen returns another handle on the same database: a second pool
    // or *sql.DB. It stands for a second process.
    Reopen(t *testing.T, env *Env) *Env
    // Test access to rows the API does not show.
    Jobs(t *testing.T, env *Env, runID string) []JobRow
    ExpireLeases(t *testing.T, env *Env, runID string)
    // HoldRunLock locks the run on a separate connection, as a slow
    // application transaction would, until release is called. On SQLite
    // that holds the database's write lock.
    HoldRunLock(t *testing.T, env *Env, runID string) (release func())
    // CallerTx opens an application transaction (a SQLTx) and the
    // hostcore.Tx on it, for the atomic and finish-hook tests.
    CallerTx(t *testing.T, env *Env) (tx durable.SQLTx, htx hostcore.Tx, commit, rollback func() error)
}

func Run(t *testing.T, h Harness) // runs every test as a subtest
```

Each backend module adds `func TestSuite(t *testing.T) {
hosttest.Run(t, harness{}) }`. Postgres's harness skips without
`WORKFLOW_PG_DSN`, as its tests do today. SQLite has two harnesses:
modernc (always) and mattn (`sqlite_mattn` tag).

What moves into the suite: conformance (apply, local and the backend),
`aba`, `fence`, `race`, `shutdown`, `store` (except index and LISTEN
checks), `result`, `settle_record`, `sideeffect`, `deploy`,
`definition`, `loop`, `start`, `versions`, `specialised`, `caller`, the
commit and rollback parts of `atomic`, and the tests #50 to #56 add
(`graceful`, `once`, `execute`, observer). New in the suite:

- **Two handles on one database** (UC-3), and a fence test across them.
- **Time bounds.** A job inserted with a zero `runAt` (so `now`) is
  claimed by the next claim, not the next UTC day. A claim with a 1 s
  lease is reclaimed by a second worker within 1 s plus `Poll` of the
  expiry. Both run on every backend.
- **Rule 1.** For each fenced write: the job deleted gives `ErrJobGone`
  and `Cancelled`; the job reclaimed gives `ErrLeaseLost` and `LeaseLost`.
- **Finish hook** (UC-5, PR 3): a parent resumed through
  `WithOnFinishedSQL` when its child completes through a worker, is
  cancelled, or is aborted; a hook error rolls the event back.
- **Shutdown under a held lock.** Spec 003's test, on every backend: a
  1-minute `Lease`, a handler that returns a result, `HoldRunLock` on its
  run before the result arrives, then `Shutdown` with a 300 ms deadline.
  The suite asserts from `Caps`:

  | Caps | `Shutdown` returns within | The claim |
  | --- | --- | --- |
  | `RowLocks` | 2 s | given back: `claims` refunded, lease cleared (spec 003) |
  | not `RowLocks`, `CancelEndsLockWait` | 300 ms + 2 × 1 s + 0.5 s slack | left to its lease: the settle and the give-back both wait on the held lock |
  | not `RowLocks`, not `CancelEndsLockWait` | 300 ms + 2 × `BusyTimeout` + 0.5 s slack | left to its lease |

  The mattn harness sets `_busy_timeout=1000`, so its bound is 2.8 s. In
  every case, `Shutdown` returns `context.DeadlineExceeded`, the handler
  has returned, and after `release()` another worker claims the job when
  its lease expires.

What stays in `postgres/`: `claim_test` (query plan), `listen_test`,
`migrate_test` (advisory lock), the pg error codes in `output_test`, the
notification parts of `atomic_test`, the `pgx.Tx` entry points, the
hook-selection table above, and the Example tests. New in `sqlite/`: the
checks table above, concurrent `Migrate` from four handles, the
read-before-write guard, the UC-1 compile test (both stores to one
interface, both workers to one interface, one `durable.Observer` value in
both), and the Example.

The public wrappers are thin. Each module tests them with the compile
test, one end-to-end run through the public API, and the Example.

### Order and PRs

Three PRs, stacked on `docs/sqlite-backend-prd`. Each builds and passes
alone.

1. `refactor(postgres): share the worker and store with a durable core`.
   Package `durable`, `internal/hostcore`, the postgres wrappers and
   aliases, and rule 1 in the postgres backend (it replaces #54's own
   existence check, which moves into the contract). No postgres test
   changes what it asserts. A test that reaches a moved private helper
   (`attemptOutcome`, `hasNUL`, `interrupted`, `w.claim`, `s.defs`) reaches
   it at its new place. Reviewers check: `TestClaimUsesItsIndex` passes,
   `git diff --stat postgres/*_test.go` is small, and the visible changes
   are exactly PRD UC-7's first three items.
2. `test: run the durable host suite on any backend`. `internal/hosttest`,
   the postgres harness with `HoldRunLock` and `Caps`. Tests move; the
   number of postgres tests that run does not drop.
3. `feat(sqlite): run the durable host on SQLite, with one finish hook`.
   The `sqlite` module, its two harnesses, its own tests, the Example; the
   postgres change for #58 (hook table above) and its tests; docs (README,
   hosts.md with PRD UC-4's note to keep a caller transaction short and
   free of network calls, llms.txt, doc.go), Makefile `test-sqlite`, and two CI jobs
   (modernc; mattn with cgo). Closes #53 and #58.

Rejected: one PR. It would mix a refactor that must change nothing with a
feature that adds a module, and a reviewer could not tell which diff
lines carry risk. Rejected: #58 as a fourth PR. Its parity test (UC-5)
needs both backends, which exist first in PR 3, and PR 3's docs would
otherwise show a two-hook pattern for one PR's time.

## Alternatives considered

- **Move the store to database/sql and add a SQLite flavor** (#52's option
  3). One store type, `switch` per query, as goqite does. Rejected: the
  brief keeps Postgres on pgx; `LISTEN` needs a pgx connection anyway;
  and about 25 statements with two forms each is harder to read than two
  small backend files.
- **A data plan instead of a `Tx` interface.** `persist` returns a list of
  writes (`UpdateRun`, `InsertJob`, ...) and each backend executes it.
  Easier to unit-test. Rejected: `UpdateRun` returns the event sequence
  that `InsertEvent` needs, and the finish hook must run in the
  transaction after the writes. A plan would need placeholders for both.
  The interface keeps the code in today's order.
- **Shared types as aliases to an internal package.** Rejected in the PRD
  review: users meet an internal name in `%T`, and pkg.go.dev shows no
  fields on the backend's page.
- **`BEGIN IMMEDIATE` on a dedicated `*sql.Conn`.** Explicit and
  well-known. Rejected: `database/sql` does not know the connection is in
  a transaction. If `COMMIT` fails on a cancelled context, the connection
  goes back to the pool with the transaction open, unless we discard it
  through `Conn.Raw`. The write-first rule needs none of this.
- **River-style `SetMaxOpenConns(1)`.** It removes in-process lock waits.
  Rejected as the rule: it does nothing for a second process (UC-3), and
  it serializes reads. It stays the advice for `:memory:`.
- **Give a SQLite claim back after the lock is released, past the
  `Shutdown` deadline.** It would refund the claim. Rejected: it breaks
  spec 003's bound, and on Cloud Run the platform kills the process at
  that point anyway.

## Tradeoffs and consequences

- `internal/hostcore` is a contract between modules. A root release that
  changes it can break an older `postgres` or `sqlite` module. We release
  the three modules together, and the release notes say to upgrade them
  together.
- Package `durable` is new public API in the root module. Its types are
  the ones postgres already exports, so it adds a home, not a concept.
- Postgres code gets one more layer: a public method calls the core,
  which calls a `Tx` method that runs the SQL. A reader looking for
  "what SQL does Deliver run" now opens two files.
- Rule 1 adds an existence check to fenced writes. It runs only when the
  write changed no row, which is the rare path (on Postgres it is part of
  the same statement).
- A postgres store with only `WithOnFinishedSQL` settles through
  `database/sql`. That path is a little slower than pgx's native one.
  Users who need the native path keep `WithOnFinished`.
- The write-first rule is a convention. The read guard in `sqliteTx`
  makes a break fail loudly, but only on SQLite.
- SQLite serializes all writes. Heartbeats from `Concurrency` jobs, claims
  and settles all queue on one lock. Fine for development; we claim
  nothing about throughput.
- A job written in another process waits up to `Poll` (default 1 s).
- CI gains two jobs, one with cgo.

## Failure modes

| Failure | Effect | Detection |
| --- | --- | --- |
| A write transaction reads first (regression) | `SQLITE_BUSY` under contention | read guard fails every test |
| `busy_timeout` missing | any overlap of writers fails | `Migrate`/`Run` refuse to start |
| App holds its own transaction longer than `busy_timeout` | worker claims and heartbeats get `SQLITE_BUSY`; a heartbeat failing for a whole lease cancels the handler | logged, as on Postgres when the database is unreachable |
| App holds its own transaction during `Shutdown` | results and give-backs cannot be written; claims left to their lease | the held-lock suite test pins the bound |
| mattn with a long `busy_timeout` | each write after a `Shutdown` deadline waits up to `busy_timeout` | the mattn harness's timing test; docs |
| Clock jumps back on the host | leases last longer; due jobs wait | none; the same as Postgres with a skewed server clock |
| Database on a network file system | WAL locking breaks; possible corruption | not detectable; the docs forbid it |
| Old `postgres` module with new root module | build fails in `internal/hostcore` | lockstep releases |
| A finish hook that delivers to a parent fails | the child's finishing event rolls back; a worker retries the settle | the hook's error in the log; UC-5's test |

## Rollout

No migration on Postgres. The `sqlite` module is new, so it has no users
to migrate. PR 1 can merge alone if PR 3 slips. Postgres's behaviour
changes only for a configuration it rejects today (`WithOnFinishedSQL`
alone), in PR 3. The README's packages table and `hosts.md` gain the
`durable` and `sqlite` entries in PR 1 and PR 3.

## Open questions

- **The exact shapes from #50 to #56.** This spec uses the approved PRDs
  and specs of 2026-10-28 (003 to 006). When they merge, PR 1 moves
  whatever merged, and this spec is updated if a shape changes the
  `Backend` interface. Not blocking: the build waits for them.
- **Whether `jobs_claim` serves the SQLite claim well with several
  queues.** SQLite may scan each queue's range and sort, rather than stop
  at the limit. Harmless at development scale. PR 3 checks
  `EXPLAIN QUERY PLAN` in a test, and splits the claim per queue if the
  plan scans the table.
