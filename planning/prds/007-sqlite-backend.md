# Run the durable host on SQLite in development and Postgres in production

Status: Draft
PRD PR: #TBD  Implementation PR: #TBD  Updated: 2026-10-28
Issues: #53

## Problem

A Go developer builds a service on the postgres host: `postgres.Store`
starts runs and stores them, and `postgres.Worker` runs their steps. In
development and in tests, the same service runs on SQLite, so that a
developer needs no extra services. Many Go stacks work this way, for
example Ent with SQLite in development and Postgres in production.

Today that developer cannot embed the durable host without Postgres in
development. The choices are bad:

- Run Postgres on every laptop and in every test job.
- Use `local.Runner` in development. It has a different API (`Runner.Start`
  returns a `*local.Run`; `run.Deliver(ev)`), so the service has two code
  paths. It also skips the behaviour that fails in production: claims and
  `abandoned`, `definition_unavailable`, `invalid_output` for values
  Postgres cannot store, `WithOnFinished`, step records and the event log.

Issue #53 asks for a SQLite backend. The engine before #42 had one
(`experimental/store/sqlite`, removed in the rewrite), so the need is not
new. If we do nothing, each such service writes its own adapter between
local and postgres, and the dev path keeps hiding production failures.

## Context

**Our code.** The postgres module is about 1,600 lines: `store.go`
(persistence) and `worker.go` (claim loop, heartbeat, settle, give-back,
abort). About 60% of it is policy that never names a table. Four tracks add
policy to the same files before this one starts: `Shutdown`, `MayClaim`
and `Active` (#50, #51), database/sql transactions (#52), `Observer` and
`QueueStats` (#54), `RunOnce` and `Execute` (#55, #56).

**Does local with Save and Resume cover the need?** No. It gives one
process crash recovery: `Save` stores each state, and `Resume` dispatches
in-flight work again. It lacks what the brief asks for: several processes
on one database (no leases, no fences), step records, an event log, the
read API (`GetRun`, `Steps`, `Events`), runs that start or take results
inside the application's transaction, and, above all, the postgres API. The
docs will say that local is for unit tests, CLIs and single-process
programs, and that the SQLite host is for dev/prod parity.

**Comparable libraries** (full notes in discovery):

- **River** keeps all worker policy in `river.Client` and puts storage
  behind an internal driver interface ("API is not stable. DO NOT
  IMPLEMENT"). Drivers `riverpgxv5`, `riverdatabasesql` and `riversqlite`
  each have their own SQL. A driver without LISTEN runs in poll-only mode.
  One driver test suite runs against every driver. River added
  `AUTOINCREMENT` to SQLite job IDs so that a deleted job's ID is never
  reused.
- **goqite** has one queue type with a `switch` on SQL flavor (SQLite or
  Postgres) in each method, on `*sql.DB`, "Bring your own SQL driver".
- **backlite** is SQLite only. It wakes its dispatcher in-process after a
  commit, and polls for the rest.
- **neoq** implements its full API once per backend. The policy is written
  three times, and it drifts.

We follow River's split: policy once, storage behind an internal
interface, one test suite for every backend.

**SQLite facts that shape the design.** One writer at a time. A
transaction that reads and then writes can fail with `SQLITE_BUSY`, and
the busy timeout does not retry it, so every write transaction must take
the write lock first. `busy_timeout` is per connection, so it must be in
the DSN. WAL works for several processes on one host, never on a network
file system. `UPDATE ... RETURNING` exists since SQLite 3.35, and
`STRICT` tables since 3.37.

## Users and job

**Primary user:** a Go developer whose service uses SQLite in development
and tests, and Postgres in production. The job: write the workflow code
once, run it on a laptop with a SQLite file, and trust that it behaves the
same on Postgres. When tradeoffs arise, we optimize for this user.

**Secondary user:** a developer who ships a small single-host service on
SQLite (for example a web process and a worker process on one machine).

**Maintainers** of this repo are a third user: one worker and one
persistence policy to keep correct, not two.

## Concepts

- **Backend**: the database the durable host keeps runs in. There are two:
  Postgres (`postgres` module) and SQLite (`sqlite` module). Both have the
  same API names and the same behaviour, except where the table under
  [Differences](#differences-between-the-backends) says otherwise.

No other new concept. Store, Worker, run, job, claim, lease and Result
keep their meaning from `documentation/hosts.md`.

## Use cases

### UC-1: One code path, backend chosen at startup

The developer writes the service against a small interface. Both stores
satisfy it, because `sqlite.NewRun`, `sqlite.Result` and the other shared
types are the same Go types as `postgres.NewRun` and `postgres.Result`.

```go
type runStore interface {
    Start(ctx context.Context, run sqlite.NewRun) (*sqlite.Result, error)
    Deliver(ctx context.Context, runID string, ev workflow.Event) (*sqlite.Result, error)
    GetRun(ctx context.Context, runID string) (*sqlite.Run, error)
}

var store runStore
if cfg.PostgresURL != "" {
    store, err = postgres.New(pool, kinds)
} else {
    store, err = sqlite.New(db, kinds)
}
```

Acceptance:

- [ ] `*postgres.Store` and `*sqlite.Store` both satisfy an interface
      written with either package's type names. A compile-time test
      proves it for every shared method.
- [ ] `errors.Is(err, postgres.ErrRunNotFound)` is true for the error
      `sqlite.Store.GetRun` returns for a missing run, and the other way
      round. The same holds for every shared sentinel.
- [ ] The same handlers run on `sqlite.Worker` and `postgres.Worker`.

### UC-2: A service on a laptop, one process

```go
db, _ := sql.Open("sqlite", "file:dev.db?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate")
store, _ := sqlite.New(db, kinds)
_ = store.Migrate(ctx)

w := &sqlite.Worker{Store: store, Handlers: handlers}
go w.Run(ctx)

res, err := store.Start(ctx, sqlite.NewRun{ID: "order-42", Definition: def, Input: input})
```

The DSN works with modernc.org/sqlite and with mattn/go-sqlite3. The
developer imports the driver; the `sqlite` module imports none.

Acceptance:

- [ ] The run reaches the same status, outputs and errors as on Postgres,
      and dispatches the same step attempts (the conformance suite).
- [ ] A step whose job was written through the same `Store` starts without
      waiting for `Poll`.
- [ ] `Steps` and `Events` return the same records as on Postgres for the
      same run, except for timestamps.
- [ ] An `Example` test of 10 to 20 lines runs this flow on a temporary
      file and prints the outcome. It runs in CI.

### UC-3: A web process and a worker process share one file

The web process calls `store.Start` and, for a deferred step,
`store.Deliver`. The worker process runs `sqlite.Worker`. Both open the
same file with the DSN above.

Acceptance:

- [ ] A job that one process writes is claimed by the other within
      `Worker.Poll`.
- [ ] Two workers on separate `*sql.DB` handles on one file never run the
      same attempt twice at once. Fences reject a stale result, as on
      Postgres (the shared concurrency suite runs on both backends).
- [ ] No `SQLITE_BUSY` error reaches a caller or a log under the
      concurrency suite, with `busy_timeout` set.

### UC-4: Start a run in the application's own transaction

```go
tx, _ := db.BeginTx(ctx, nil)          // or an Ent transaction
_, _ = tx.ExecContext(ctx, `INSERT INTO orders ...`)
res, err := store.StartSQLTx(ctx, tx, sqlite.NewRun{ID: "order-42", Definition: def})
_ = tx.Commit()                        // a rollback leaves no run and no job
```

`DeliverSQLTx` and `CancelSQLTx` work the same way. `WithOnFinishedSQL`
runs a hook in the transaction that finishes a run. The names and the
`SQLTx` type are the ones #52 gives postgres, so the same call compiles
against either store.

Acceptance:

- [ ] A rollback leaves no run, step record, event or job.
- [ ] A commit makes the run's first steps claimable.
- [ ] An error from the `WithOnFinishedSQL` hook rolls the event back, as
      on Postgres.

### UC-5: A misconfigured database fails fast, with the fix

Acceptance:

- [ ] `Migrate` and `Worker.Run` return an error when the connection has
      `busy_timeout` 0. The message names the DSN parameter
      `_busy_timeout=5000`.
- [ ] They return an error for an in-memory database on a pool that can
      open more than one connection. The message says to use a file or
      `db.SetMaxOpenConns(1)`.
- [ ] They return an error for SQLite older than 3.37 (`RETURNING` and
      `STRICT` tables). The message names the version found.

### UC-6: Postgres users see no change

Acceptance:

- [ ] Every postgres test that exists when this track starts passes after
      the extraction (PR 1) with no change to what it asserts. A test that
      calls a private helper that moved (such as `attemptOutcome`) calls it
      at its new place.
- [ ] Every exported name in the postgres package keeps its name, fields,
      methods and behaviour.

## Requirements

- R-1: A new Go module `github.com/deepnoodle-ai/workflow/sqlite` in
  `sqlite/`. It depends on the root module and the standard library. Its
  tests may also use modernc.org/sqlite. It takes a `*sql.DB` and imports
  no driver.
- R-2: The root module keeps only the standard library and expr. The
  postgres module stays on pgx.
- R-3: `sqlite` exports the postgres API names that do not depend on pgx
  or LISTEN: `New`, `Option`, `WithSchema`, `WithLimits`, `WithRunIDs`,
  `WithOnFinishedSQL`, `Store` (`Migrate`, `Start`, `Deliver`, `Cancel`,
  `StartSQLTx`, `DeliverSQLTx`, `CancelSQLTx`, `GetRun`, `Steps`,
  `Events`, `QueueStats`), `Worker` (`Store`, `Handlers`, `ID`,
  `Concurrency`, `Lease`, `Poll`, `MaxClaims`, `Logger`, `MayClaim`,
  `Observer`; `Run`, `Shutdown`, `Active`, `RunOnce`, `Execute`), and the
  shared types and sentinels. Names and signatures follow what #50 to #56
  ship. Each default is the same as in postgres.
- R-4: Types and sentinels that both packages export are one Go type or
  value each: `NewRun`, `Result`, `Run`, `StepRecord`, `StepStatus` and its
  constants, `Finished`, `SQLTx`, `JobRef`, `Execution`, `Observer` and its
  event types, `QueueStat`, `EventAborted`,
  `ErrorTypeDefinitionUnavailable`, `ErrRunNotFound`, `ErrRunExists`,
  `ErrUnstorable`, `ErrDefinitionUnavailable` and `ErrNotClaimable`.
- R-5: Both backends run one worker policy and one persistence policy. A
  rule such as "a claim given back on shutdown does not count toward
  `MaxClaims`" exists once in the code.
- R-6: The SQLite backend applies Postgres's storage rules. An output with
  a NUL character, or one JSON cannot encode, fails the step with
  `invalid_output`. `Start` returns `ErrUnstorable` for such an input or
  metadata. So a run that works on SQLite does not fail on Postgres for a
  value Postgres cannot store.
- R-7: The SQLite job ID is never reused, so a fence can never match a
  newer job.
- R-8: Every transaction the SQLite store opens takes the write lock before
  it reads. It does not depend on the DSN's `_txlock`.
- R-9: One test suite (conformance, fence, race, ABA, shutdown, atomic,
  deferred, definition-unavailable, MaxClaims) runs against both backends.
- R-10: `WithSchema(name)` puts the SQLite tables under the prefix
  `name_` (default `workflow_`), so they do not collide with an
  application's own `jobs` or `runs` table.
- R-11: Docs: `README.md` (packages table, a SQLite snippet),
  `documentation/hosts.md` (a `sqlite` section with the DSN and the
  differences table, and a line on when to use local), `llms.txt` and
  `doc.go`. Docs name no product.
- R-12: CI and `make test-all` run the `sqlite` module's tests, with
  `-race`.

### Differences between the backends

| | Postgres | SQLite |
| --- | --- | --- |
| Caller transactions | `StartTx` etc. with `pgx.Tx`, and the `SQLTx` forms | the `SQLTx` forms only |
| Run finished signal | `WithOnFinished`, `WithOnFinishedSQL`, `RunsChannel` | `WithOnFinishedSQL` |
| Wake-up | `LISTEN` on `NotifyChannel`, else `Poll` | at once for jobs written through the same `Store`, else `Poll` |
| `ListenConfig` | yes | no |
| Clock for leases and due times | the database's | the process's (one host) |
| `WithSchema` | a Postgres schema | a table-name prefix |
| Where it runs | any number of hosts | processes on one host; not a network file system |

## Not in scope

- A public storage interface for third-party backends. It stays in
  `internal/` until two backends have used it for a while (River marks
  its driver interface "DO NOT IMPLEMENT" for the same reason).
- MySQL, libSQL, Turso, LiteFS, or SQLite on a network file system. Not
  tested; WAL needs one host.
- Instant wake-ups across processes (a notification table, or polling
  `PRAGMA data_version`). `Poll` covers development. A later issue if a
  user needs it.
- Moving data between a SQLite and a Postgres database.
- Run listing, search and retention. Not in postgres either.
- Changes to the postgres public API.
- A mattn/go-sqlite3 test run in CI. It needs cgo. The store uses only
  `TEXT` and `INTEGER` columns through plain `database/sql` calls.

## Decisions

- **Build a SQLite backend, not only docs.** Discovery shows local with
  Save and Resume lacks the postgres API, several processes, step records,
  the event log, the read API and caller transactions. Rejected: a docs
  change that points dev users at local.
- **Share the policy, not the SQL.** One worker and one persistence policy
  in `internal/durable` (root module). Each backend implements a narrow
  storage interface with its own SQL. Rejected: goqite's flavor switch
  (pgx and database/sql cannot share query code); neoq's independent
  backends (policy drift, which is the bug class this work exists to
  avoid).
- **Shared types are aliases of one type.** `postgres.Result` and
  `sqlite.Result` are both `= durable.Result`. Then one interface fits both
  stores (UC-1). Rejected: a copy of each type per package (every app
  needs an adapter); a new public package for the types (a second public
  home before the design settles; aliases let us add one later without a
  break).
- **Sentinel errors are shared values with a `workflow:` prefix.**
  `errors.Is` then works across backends. The only visible postgres change
  is the message prefix, `postgres:` to `workflow:`. Rejected: one error
  value per package (backend-agnostic code would test two values); a
  `postgres:` prefix on SQLite errors (misleading).
- **The SQLite caller-transaction methods use #52's names and `SQLTx`
  type.** One call compiles against both stores. Rejected: `StartTx(ctx,
  *sql.Tx, ...)` in sqlite, which reads better alone but breaks UC-1 and
  UC-4 parity. If #52 changes these names before it merges, sqlite follows.
- **The store takes the write lock as its first statement.** Locking a run
  is `UPDATE ... RETURNING`, and a claim is one `UPDATE ... RETURNING`.
  This works with any DSN. Rejected: raw `BEGIN IMMEDIATE` on a `*sql.Conn`
  (a failed `COMMIT` can return a connection to the pool inside a
  transaction); requiring `_txlock=immediate` (the store cannot check it).
  The docs still recommend `_txlock=immediate` for the application's own
  transactions that it passes to `StartSQLTx`.
- **`busy_timeout` is required and checked; WAL is recommended, not
  required.** Without a busy timeout, any overlap of two writers fails at
  once. Rollback-journal mode is correct, only slower. Rejected: River's
  `SetMaxOpenConns(1)` advice (it serializes one process but not two,
  and UC-3 has two).
- **Postgres's storage rules apply on SQLite.** The job is dev/prod
  parity, so development must fail where production fails. Rejected:
  accept what SQLite can store (a NUL output would pass in development and
  fail in production).
- **The SQLite host uses the process clock and stores times as fixed-width
  UTC text with microseconds.** All processes are on one host, so they
  share a clock. Text in one format sorts by time and reads well in the
  `sqlite3` shell. Microseconds match Postgres's `timestamptz`. Rejected:
  integer epochs (unreadable while debugging); driver time types (modernc
  and mattn read and write them differently).
- **`WithSchema` is a table prefix on SQLite.** SQLite has no schemas, and
  `jobs` and `runs` are common table names. The same option name keeps
  setup code parallel. Rejected: no option (collisions); a new
  `WithTablePrefix` (a second name for the same idea).
- **Wake-ups: in-process for the same `Store`, else `Poll` (default 1s, as
  postgres).** The common development setup is one process and one
  `Store`. Rejected: a notification table (River) or `data_version`
  polling, which add a table or a held connection for a case `Poll`
  already covers.
- **Checks run in `Migrate` and `Worker.Run`, not in `New`.** Both take a
  context and run at startup; `New` does no I/O in postgres either.
- **Three stacked PRs:** (1) extract the shared core, postgres unchanged;
  (2) one test suite for every backend; (3) the SQLite module. PR 1 is a
  pure refactor that reviewers check against unchanged tests. The tech
  spec gives the details.
- **The modules release together.** The backend modules import
  `internal/durable` from the root module, so that package is a contract
  between versions. Each release of `postgres` and `sqlite` requires the
  matching root version. Rejected: a separate module for the core (a third
  module to version, for no user benefit yet).

## Success

- A service that keeps one code path and switches between SQLite and
  Postgres by configuration. The `Example` test and UC-1's compile test
  show it.
- The shared suite is green on both backends in CI, with `-race`.
- Postgres behaviour does not change: its existing tests pass, and its
  claim query plan is unchanged.
- The root module gains no dependency.

## Risks and open questions

- **The extraction collides with four tracks that change `worker.go` and
  `store.go`.** Mitigation: implementation starts only after they merge,
  and PR 1 moves their code as it is. Not blocking.
- **Aliases change what `%T` and `reflect` print** for postgres types
  (`durable.Result`, not `postgres.Result`). Code that switches on type
  names breaks. We judge this rare and accept it; the PR notes it.
- **A cross-module internal package can break an old backend module
  against a new root module.** Mitigation: lockstep releases, and the
  release notes say to upgrade the modules together.
- **SQLite write throughput.** One writer serializes claims, heartbeats
  and settles. Fine for development and small hosts. We publish no
  numbers and do not claim it for large fleets.
- **#52's names may change in its review.** Not blocking: sqlite follows
  whatever merges, and its implementation starts after #52 merges.
