# Run the durable host on SQLite in development and Postgres in production

Status: Approved
PRD PR: #63  Implementation PR: #TBD  Updated: 2026-10-28
Issues: #53, #58
Tech spec: [specs/007-sqlite-backend.md](../specs/007-sqlite-backend.md)

## Problem

A Go developer keeps the application's own data in SQLite in development
and in tests, and in Postgres in production. Many Go stacks work this way,
for example Ent with SQLite in development. The developer wants to create
an order and start its run in one transaction, in both places. Only a
SQLite backend can do that in development: the run must commit in the same
database, and the same transaction, as the order. A Postgres container or
an embedded Postgres binary gives dev parity for the workflow tables, but
not for this transaction. This use case (UC-4) is the one we protect in a
tradeoff.

Today that developer cannot embed the durable host without Postgres in
development. The choices are bad:

- Run Postgres on every laptop and in every test job, and still lose the
  shared transaction with the application's SQLite data.
- Use `local.Runner` in development. It has a different API (`Runner.Start`
  returns a `*local.Run`; `run.Deliver(ev)`), so the service has two code
  paths. It also skips the behaviour that fails in production: claims and
  `abandoned`, `definition_unavailable`, `invalid_output` for values
  Postgres cannot store, the finish hook, step records and the event log.

Issue #53 asks for a SQLite backend. The engine before #42 had one
(`experimental/store/sqlite`, removed in the rewrite), so the need is not
new. If we do nothing, each such service writes its own adapter between
local and postgres, and the development path keeps hiding production
failures.

## Context

**Our code.** The postgres module is about 1,600 lines: `store.go`
(persistence) and `worker.go` (claim loop, heartbeat, settle, give-back,
abort). About 60% of it is policy that never names a table. Four tracks add
policy and public types to the same files before this one starts:
`Shutdown`, `MayClaim` and `Active` (#50, #51, PRD 003), `StartSQLTx`,
`DeliverSQLTx`, `CancelSQLTx`, `SQLTx` and `WithOnFinishedSQL` (#52, PRD
004), `Observer`, `Settlement`, `Outcome`, `QueueStats` and `QueueStat`
(#54, PRD 005), `RunOnce`, `Execute`, `Execution`, `JobRef` and
`ErrNotClaimable` (#55, #56, PRD 006).

**Does local with Save and Resume cover the need?** No. It gives one
process crash recovery: `Save` stores each state, and `Resume` dispatches
in-flight work again. It lacks what the brief asks for: several processes
on one database (no leases, no fences), step records, an event log, the
read API (`GetRun`, `Steps`, `Events`), runs that start or take results
inside the application's transaction, and, above all, the postgres API. The
docs will say that local is for unit tests, CLIs and single-process
programs, and that the SQLite backend is for dev/prod parity.

**The finish hook today.** On Postgres, a worker that finishes a run calls
`WithOnFinished` with a `pgx.Tx`. After #52, a run that finishes in the
caller's `database/sql` transaction calls `WithOnFinishedSQL` instead.
hosts.md tells users to resume a parent run from this hook when its child
run finishes. With two hooks, that code differs between backends, and
development never runs the production hook. Issue #58 asks for one hook and
says to revisit it when the SQLite backend settles through `database/sql`.
This PRD decides #58.

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

**SQLite facts that shape the design.** One writer at a time, for the whole
database. A transaction that reads and then writes can fail with
`SQLITE_BUSY`, and the busy timeout does not retry it, so every write
transaction must take the write lock first. `busy_timeout` is per
connection, so it must be in the DSN. WAL works for several processes on
one host, never on a network file system. `UPDATE ... RETURNING` exists
since SQLite 3.35, and `STRICT` tables since 3.37.

We measured how a cancelled context ends a wait for the write lock, with
another connection holding it and `busy_timeout` 5000. modernc.org/sqlite
v1.61 (SQLite 3.54.1) returned `context deadline exceeded` after 300 ms,
when the context ended. mattn/go-sqlite3 v1.14.52 (SQLite 3.53.4) ignored
the context and returned `database is locked` after 5.37 s. Both
connections were usable afterwards.

## Users and job

**Primary user:** a Go developer whose service keeps its data in SQLite in
development and tests, and in Postgres in production. The job: write the
workflow code once, run it on a laptop with a SQLite file, and trust that
it behaves the same on Postgres, including runs started in the same
transaction as the application's own writes. When tradeoffs arise, we
optimize for this user.

**Secondary user:** a developer who ships a small single-host service on
SQLite (for example a web process and a worker process on one machine).

**Maintainers** of this repo are a third user: one worker and one
persistence policy to keep correct, not two.

## Concepts

- **Backend**: the database the durable host keeps runs in. There are two:
  Postgres (`postgres` module) and SQLite (`sqlite` module). Both have the
  same API names and the same behaviour, except where the table under
  [Differences](#differences-between-the-backends) says otherwise.
- **Package `durable`**: the types that both backends share, such as
  `durable.NewRun`, `durable.Result` and `durable.Observer`. Each backend
  re-exports them under its own name (`postgres.Result` and
  `sqlite.Result` are `durable.Result`), so a user needs this package only
  for code that runs on either backend.

No other new concept. Store, Worker, run, job, claim, lease, Result and
finish hook keep their meaning from `documentation/hosts.md`.

## Use cases

### UC-1: One code path, backend chosen at startup

The developer writes the service against small interfaces with the
`durable` types. Both stores and both workers satisfy them.

```go
type runStore interface {
    Start(ctx context.Context, run durable.NewRun) (*durable.Result, error)
    DeliverSQLTx(ctx context.Context, tx durable.SQLTx, runID string, ev workflow.Event) (*durable.Result, error)
    GetRun(ctx context.Context, runID string) (*durable.Run, error)
}
type worker interface {
    Run(ctx context.Context) error
    Shutdown(ctx context.Context) error
    Execute(ctx context.Context, ref durable.JobRef) (durable.Execution, error)
}

metrics := durable.Observer{Settled: countSettle}
var store runStore
var w worker
if cfg.PostgresURL != "" {
    s, _ := postgres.New(pool, kinds, postgres.WithOnFinishedSQL(resume))
    store, w = s, &postgres.Worker{Store: s, Handlers: handlers, Observer: metrics}
} else {
    s, _ := sqlite.New(db, kinds, sqlite.WithOnFinishedSQL(resume))
    store, w = s, &sqlite.Worker{Store: s, Handlers: handlers, Observer: metrics}
}
```

Acceptance:

- [ ] A compile-time test assigns `*postgres.Store` and `*sqlite.Store` to
      one interface with every shared store method, and `*postgres.Worker`
      and `*sqlite.Worker` to one interface with `Run`, `Shutdown`,
      `Active`, `RunOnce` and `Execute`.
- [ ] One `durable.Observer` value is accepted by both workers' `Observer`
      field.
- [ ] `errors.Is(err, durable.ErrRunNotFound)` is true for the error
      either store's `GetRun` returns for a missing run, and so is
      `errors.Is(err, postgres.ErrRunNotFound)` for the sqlite error. The
      same holds for every shared sentinel.
- [ ] The same handlers run on `sqlite.Worker` and `postgres.Worker`.
- [ ] `durable` imports only the standard library and the root package.

### UC-2: A service on a laptop, one process

```go
db, _ := sql.Open("sqlite", "file:dev.db?_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate")
store, _ := sqlite.New(db, kinds)
_ = store.Migrate(ctx)

w := &sqlite.Worker{Store: store, Handlers: handlers}
go w.Run(context.Background())

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
- [ ] `Observer.Cancelled` and `Observer.LeaseLost` fire for the same
      cases on both backends (PRD 005, R-4).

### UC-4: Start a run in the application's own transaction

```go
tx, _ := db.BeginTx(ctx, nil)          // or an Ent transaction
_, _ = tx.ExecContext(ctx, `INSERT INTO orders ...`)
res, err := store.StartSQLTx(ctx, tx, sqlite.NewRun{ID: "order-42", Definition: def})
_ = tx.Commit()                        // a rollback leaves no run and no job
```

`DeliverSQLTx` and `CancelSQLTx` work the same way. The names and the
`SQLTx` type are the ones #52 gives postgres, so the same call compiles
against either store.

Acceptance:

- [ ] A rollback leaves no run, step record, event or job.
- [ ] A commit makes the run's first steps claimable.

### UC-5: A parent run resumes when its child run finishes

A host kind starts a child run, records the child's parent in the
application's own table, and returns `workflow.ErrDeferred`. One finish
hook delivers the child's outcome to the parent. The same hook runs on
both backends, on every path that finishes a run: a worker's settle, a
worker's abort, `Start`, `Deliver`, `Cancel` and the `SQLTx` methods.

```go
resume := func(ctx context.Context, tx durable.SQLTx, f durable.Finished) error {
    parent, ok, err := lookupParent(ctx, tx, f.RunID) // the app's table, same tx
    if err != nil || !ok {
        return err
    }
    ev := workflow.Event{Kind: workflow.EventStepCompleted, StepID: parent.StepID, Output: f.Outputs}
    if f.Status != workflow.StatusCompleted {
        ev = workflow.Event{Kind: workflow.EventStepFailed, StepID: parent.StepID,
            Error: &workflow.StepError{Type: "child_" + string(f.Status)}}
    }
    _, err = store.DeliverSQLTx(ctx, tx, parent.RunID, ev)
    return err
}
```

Acceptance:

- [ ] On both backends, a child run that a worker completes resumes its
      parent through `resume`, and the parent completes. So does a child
      that is cancelled with `Cancel`, and one that a worker aborts.
- [ ] On both backends, `tx` in the hook is the caller's value when a
      `SQLTx` method finished the run (PRD 004, R-4), and a `*sql.Tx` the
      store opened otherwise.
- [ ] An error from the hook rolls the finishing event back, and a worker
      retries its settle, on both backends.
- [ ] A postgres store with `WithOnFinishedSQL` and no `WithOnFinished` is
      accepted by `New`. Its `StartTx`, `DeliverTx` and `CancelTx`
      (`pgx.Tx`) return an error that names `WithOnFinished`, before any
      statement runs.
- [ ] A postgres store with only `WithOnFinished`, or with both hooks,
      behaves as PRD 004 says.

### UC-6: A misconfigured database fails fast, with the fix

Acceptance:

- [ ] `Migrate` and `Worker.Run` return an error when the connection has
      `busy_timeout` 0. The message names the DSN parameter
      `_busy_timeout=5000`.
- [ ] They return an error for an in-memory database on a pool that can
      open more than one connection. The message says to use a file or
      `db.SetMaxOpenConns(1)`.
- [ ] They return an error for SQLite older than 3.37 (`RETURNING` and
      `STRICT` tables). The message names the version found.

### UC-7: Postgres users see only the listed changes

Acceptance:

- [ ] Every postgres test that exists when this track starts passes after
      the extraction (PR 1) with no change to what it asserts. A test that
      calls a private helper that moved (such as `attemptOutcome`) calls it
      at its new place.
- [ ] Every exported name in the postgres package keeps its name, fields,
      methods and behaviour, with exactly these visible changes:
  - [ ] `%T` and `reflect` print the shared types and the sentinels' types
        as `durable.X` (for example `*durable.Result`, not
        `*postgres.Result`).
  - [ ] The shared sentinels' messages start with `workflow:`, not
        `postgres:` (for example `workflow: run not found`). A run failed
        as `definition_unavailable` stores that message in its error.
  - [ ] On pkg.go.dev, the postgres page shows `type Result =
        durable.Result`, and the fields and methods are on the `durable`
        page.
  - [ ] `New` accepts `WithOnFinishedSQL` without `WithOnFinished` (UC-5).
        Today it rejects that configuration.
- [ ] The postgres claim query plan does not change
      (`TestClaimUsesItsIndex`).

## Requirements

- R-1: A new Go module `github.com/deepnoodle-ai/workflow/sqlite` in
  `sqlite/`. It depends on the root module and the standard library. Its
  tests also use modernc.org/sqlite and mattn/go-sqlite3. It takes a
  `*sql.DB` and imports no driver.
- R-2: A new public package `github.com/deepnoodle-ai/workflow/durable` in
  the root module. It imports only the standard library and the root
  package. It holds every type, constant and sentinel that both backends
  export: `NewRun`, `Result` (with `Jobs()`), `Run`, `StepRecord`,
  `StepStatus` and its constants, `Finished`, `SQLTx`, `JobRef`,
  `Execution`, `Observer`, `Settlement`, `Outcome` and its constants,
  `QueueStat`, `EventAborted`, `ErrorTypeDefinitionUnavailable`,
  `ErrRunNotFound`, `ErrRunExists`, `ErrUnstorable`,
  `ErrDefinitionUnavailable` and `ErrNotClaimable`. Both backends declare
  each one as an alias (`type Result = durable.Result`) or a copy of the
  value (`var ErrRunNotFound = durable.ErrRunNotFound`).
- R-3: The root module keeps only the standard library and expr. The
  postgres module stays on pgx. The storage contract and the shared worker
  and store code stay in `internal/`.
- R-4: `sqlite` exports the postgres API names that do not depend on pgx
  or LISTEN: `New`, `Option`, `WithSchema`, `WithLimits`, `WithRunIDs`,
  `WithOnFinishedSQL`, `Store` (`Migrate`, `Start`, `Deliver`, `Cancel`,
  `StartSQLTx`, `DeliverSQLTx`, `CancelSQLTx`, `GetRun`, `Steps`,
  `Events`, `QueueStats`), `Worker` (`Store`, `Handlers`, `ID`,
  `Concurrency`, `Lease`, `Poll`, `MaxClaims`, `Logger`, `MayClaim`,
  `Observer`; `Run`, `Shutdown`, `Active`, `RunOnce`, `Execute`), and the
  names in R-2. Names and signatures follow what #50 to #56 ship. Each
  default is the same as in postgres.
- R-5: Both backends run one worker policy and one persistence policy. A
  rule such as "a claim given back on shutdown does not count toward
  `MaxClaims`" exists once in the code. So does the call to the finish
  hook.
- R-6: The SQLite backend applies Postgres's storage rules. An output with
  a NUL character, or one JSON cannot encode, fails the step with
  `invalid_output`. `Start` returns `ErrUnstorable` for such an input or
  metadata. So a run that works on SQLite does not fail on Postgres for a
  value Postgres cannot store.
- R-7: The SQLite job ID is never reused, so a fence can never match a
  newer job, and "a row with this job ID exists" means the same job.
- R-8: Every transaction the SQLite store opens takes the write lock before
  it reads. It does not depend on the DSN's `_txlock`.
- R-9: When a fenced write changes no row, both backends tell "the job is
  gone" from "another claim holds the job", with the same rule and the same
  atomicity. This drives `Observer.Cancelled` and `Observer.LeaseLost`.
- R-10: A postgres store whose only finish hook is `WithOnFinishedSQL`
  runs every transaction it opens itself through `database/sql` on its own
  pool (`stdlib.OpenDBFromPool`). The hook then runs on every path that
  finishes a run, with a `SQLTx`. This closes #58.
- R-11: One test suite (conformance, fence, race, ABA, shutdown, graceful
  shutdown, atomic, deferred, definition-unavailable, MaxClaims, observer,
  RunOnce, Execute, finish hook) runs against both backends. A backend
  that differs (table below) declares it to the suite, and the suite
  asserts the declared behaviour.
- R-12: `WithSchema(name)` puts the SQLite tables under the prefix
  `name_` (default `workflow_`), so they do not collide with an
  application's own `jobs` or `runs` table.
- R-13: Docs: `README.md` (packages table, a SQLite snippet),
  `documentation/hosts.md` (a `sqlite` section with the DSN, the
  differences table, the driver note, and a line on when to use local;
  UC-5's hook in "Generated workflows"), `llms.txt` and `doc.go`. Docs
  name no product.
- R-14: CI and `make test-all` run the `sqlite` module's tests with
  `-race` on modernc.org/sqlite, and in a second job on mattn/go-sqlite3
  (cgo).

### Differences between the backends

| | Postgres | SQLite |
| --- | --- | --- |
| Caller transactions | `StartTx` etc. with `pgx.Tx`, and the `SQLTx` forms | the `SQLTx` forms only |
| Finish hook | `WithOnFinishedSQL` (same code as SQLite), or `WithOnFinished` with a `pgx.Tx` | `WithOnFinishedSQL` |
| Run status notifications | `RunsChannel` | none; use the finish hook |
| Wake-up | `LISTEN` on `NotifyChannel`, else `Poll` | at once for jobs written through the same `Store`, else `Poll` |
| `ListenConfig` | yes | no |
| Clock for leases and due times | the database's | the process's (one host) |
| `WithSchema` | a Postgres schema | a table-name prefix |
| What a run lock blocks | that run's row | every write to the database |
| `Shutdown` while another connection holds a lock past the deadline | the result is given back; returns within the deadline plus 1 s per write | nothing can be written: the claim is left to its lease (it counts toward `MaxClaims`); returns within the deadline plus, per write, 1 s on modernc and `busy_timeout` on mattn (set `_busy_timeout=1000` on mattn if you rely on the deadline) |
| A cancelled context during a wait for a lock | ends the wait | modernc: ends the wait. mattn: does not; the write waits up to `busy_timeout`, so each write after a `Shutdown` deadline is bounded by `max(1 s, busy_timeout)` |
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
- Making mattn/go-sqlite3 end a lock wait when the context ends. The docs
  give the workaround: a `busy_timeout` of 1000 or less, or modernc.
- Moving data between a SQLite and a Postgres database.
- Run listing, search and retention. Not in postgres either.
- Removing `WithOnFinished` or the `pgx.Tx` methods. Both stay.

## Decisions

- **Build a SQLite backend, not only docs.** Discovery shows local with
  Save and Resume lacks the postgres API, several processes, step records,
  the event log, the read API and caller transactions. Rejected: a docs
  change that points dev users at local.
- **SQLite, not an embedded Postgres, for development.** Only SQLite lets
  a run commit in the same transaction as the application's own SQLite
  data (UC-4). Rejected: document a Postgres container or embedded binary
  for development. It gives parity for the workflow tables, but the
  application's data stays in SQLite, so UC-4 cannot work in development.
- **Share the policy, not the SQL.** One worker and one persistence policy
  in `internal/hostcore` (root module). Each backend implements a narrow
  storage contract with its own SQL. Rejected: goqite's flavor switch
  (pgx and database/sql cannot share query code); neoq's independent
  backends (policy drift, which is the bug class this work exists to
  avoid).
- **Shared public types live in a public package named `durable`.** The
  README and hosts.md already call these hosts "durable hosts", so
  `durable.Result` reads as "a durable host's result" without knowing our
  internals. Users meet a browsable name in `%T`, in errors and on
  pkg.go.dev, and backend-neutral code names neither backend. After #50 to
  #56 these types are public and fixed, so a public home costs no design
  freedom. The storage contract stays internal. Rejected: aliases to an
  internal package (users meet an internal name, and pkg.go.dev hides the
  fields); a copy of each type per backend (every app needs an adapter);
  the names `host` or `store` (too generic next to `local` and
  `postgres.Store`); `hostapi` (jargon).
- **Sentinel errors are shared values with a `workflow:` prefix.**
  `errors.Is` then works across backends. Rejected: one error value per
  package (backend-neutral code would test two values); a `postgres:`
  prefix on SQLite errors (misleading).
- **One finish hook on both backends, decided here (#58).** A postgres
  store with only `WithOnFinishedSQL` opens its own transactions through
  `database/sql` on its own pool, so the hook runs on every path. Then
  UC-5's code is the same in development and production, and development
  runs the production hook. It removes no API; it accepts a configuration
  that `New` rejects today. It ships with the SQLite module (PR 3),
  because the parity test needs both backends. Rejected: defer #58 and
  document a two-hook pattern (development would not run the production
  hook); route the pgx hook through `database/sql` too (changes the
  behaviour of existing `WithOnFinished` users).
- **The storage contract tells "job gone" from "lease lost".** A fenced
  write that changes no row checks whether the job ID exists, atomically
  with the write. PRD 005's `Cancelled` and `LeaseLost` then mean the same
  on both backends. Rejected: one "lease lost" error (it cannot carry
  PRD 005's events).
- **The SQLite caller-transaction methods use #52's names and `SQLTx`
  type.** One call compiles against both stores. Rejected: `StartTx(ctx,
  *sql.Tx, ...)` in sqlite, which reads better alone but breaks UC-1 and
  UC-4 parity.
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
  `SetMaxOpenConns(1)` advice (it serializes one process but not two, and
  UC-3 has two).
- **Both drivers are supported and tested; the docs state the one
  difference we measured.** mattn does not end a lock wait when the context
  ends. The suite runs on both, and the mattn harness declares that
  difference, so the timing tests assert the right bound for each driver.
  Rejected: modernc only (many users already have mattn); a Go-side retry
  loop on `SQLITE_BUSY` with `busy_timeout` 0 (it would also turn off the
  busy timeout for the application's own writes on the same pool).
- **Under a held SQLite lock, `Shutdown` leaves an unrecorded result to its
  lease.** One write lock covers the whole database, so the give-back
  waits on the same lock as the result. The claim counts. On modernc the
  shutdown bound holds (the deadline plus 1 s per write). On mattn each
  write waits up to `busy_timeout`, so the bound is the deadline plus
  `busy_timeout` per write, and the docs advise `_busy_timeout=1000`. Rejected: wait for the lock past the deadline (breaks
  PRD 003's bound, and the platform kills the process anyway).
- **Postgres's storage rules apply on SQLite.** The job is dev/prod
  parity, so development must fail where production fails. Rejected:
  accept what SQLite can store (a NUL output would pass in development and
  fail in production).
- **The SQLite backend uses the process clock and stores times as
  fixed-width UTC text with microseconds.** All processes are on one host,
  so they share a clock. Text in one format sorts by time and reads well in
  the `sqlite3` shell. Microseconds match Postgres's `timestamptz`.
  Rejected: integer epochs (unreadable while debugging); driver time types
  (modernc and mattn read and write them differently).
- **`WithSchema` is a table prefix on SQLite.** SQLite has no schemas, and
  `jobs` and `runs` are common table names. The same option name keeps
  setup code parallel. Rejected: no option (collisions); a new
  `WithTablePrefix` (a second name for the same idea).
- **Wake-ups: in-process for the same `Store`, else `Poll` (default 1 s,
  as postgres).** The common development setup is one process and one
  `Store`. Rejected: a notification table (River) or `data_version`
  polling, which add a table or a held connection for a case `Poll`
  already covers.
- **Checks run in `Migrate` and `Worker.Run`, not in `New`.** Both take a
  context and run at startup; `New` does no I/O in postgres either.
- **Three stacked PRs:** (1) extract the shared core and add package
  `durable`, postgres otherwise unchanged; (2) one test suite for every
  backend; (3) the SQLite module and the one finish hook (#58). PR 1 is a
  refactor that reviewers check against unchanged tests. The tech spec
  gives the details.
- **The modules release together.** The backend modules import
  `internal/hostcore` from the root module, so that package is a contract
  between versions. Each release of `postgres` and `sqlite` requires the
  matching root version. Rejected: a separate module for the core (a third
  module to version, for no user benefit yet).

## Success

- A service that keeps one code path, finish hook included, and switches
  between SQLite and Postgres by configuration. The `Example` test, UC-1's
  compile test and UC-5's suite test show it.
- The shared suite is green on both backends and both SQLite drivers in
  CI, with `-race`.
- Postgres behaviour changes only as UC-7 lists: its existing tests pass,
  and its claim query plan is unchanged.
- The root module gains no dependency.
- User signal: #53 and #58 close, and hosts.md shows one hook where it
  showed two.

## Risks and open questions

- **The extraction collides with four tracks that change `worker.go` and
  `store.go`.** Mitigation: implementation starts only after they merge,
  and PR 1 moves their code as it is. Not blocking.
- **A cross-module internal package can break an old backend module
  against a new root module.** Mitigation: lockstep releases, and the
  release notes say to upgrade the modules together.
- **SQLite write throughput.** One writer serializes claims, heartbeats
  and settles. Fine for development and small hosts. We publish no
  numbers and do not claim it for large fleets.
- **A finish hook that delivers to a parent in the same transaction locks
  two runs.** On Postgres that is the child's row, then the parent's. A
  parent's own transaction never locks a child's row, so no cycle exists.
  The suite runs UC-5 under concurrency. Not blocking.
- **#52's names may change in its review.** Not blocking: sqlite follows
  whatever merges, and its implementation starts after #52 merges.
